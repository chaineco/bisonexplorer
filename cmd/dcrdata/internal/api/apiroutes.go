// Copyright (c) 2018-2022, The Decred developers
// Copyright (c) 2017, The dcrdata developers
// See LICENSE for details.

package api

import (
	"context"
	"encoding/binary"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"math"
	"net/http"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	btcClient "github.com/btcsuite/btcd/rpcclient"
	"github.com/decred/dcrd/blockchain/standalone/v2"
	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrd/dcrutil/v4"
	chainjson "github.com/decred/dcrd/rpc/jsonrpc/types/v4"
	"github.com/decred/dcrd/rpcclient/v8"
	"github.com/decred/dcrd/txscript/v4/stdaddr"
	"github.com/decred/dcrd/txscript/v4/stdscript"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrdata/exchanges/v3"
	"github.com/decred/dcrdata/gov/v6/agendas"
	"github.com/decred/dcrdata/gov/v6/politeia"
	apitypes "github.com/decred/dcrdata/v8/api/types"
	"github.com/decred/dcrdata/v8/db/cache"
	"github.com/decred/dcrdata/v8/db/dbtypes"
	"github.com/decred/dcrdata/v8/mutilchain"
	"github.com/decred/dcrdata/v8/mutilchain/externalapi"
	"github.com/decred/dcrdata/v8/txhelpers"
	"github.com/decred/dcrdata/v8/utils"
	"github.com/go-chi/chi/v5"
	ltcClient "github.com/ltcsuite/ltcd/rpcclient"
	agents "github.com/monperrus/crawler-user-agents"
	"github.com/x-way/crawlerdetect"

	m "github.com/decred/dcrdata/cmd/dcrdata/internal/middleware"
)

// maxBlockRangeCount is the maximum number of blocks that can be requested at
// once.
const maxBlockRangeCount = 1000

// DataSource specifies an interface for advanced data collection using the
// auxiliary DB (e.g. PostgreSQL).
type DataSource interface {
	GetHeight() (int64, error)
	GetBestBlockHash() (string, error)
	GetBlockHash(idx int64) (string, error)
	GetBlockHeight(hash string) (int64, error)
	GetBlockByHash(string) (*wire.MsgBlock, error)
	SpendingTransaction(fundingTx string, vout uint32) (string, uint32, int8, error)
	SpendingTransactions(fundingTxID string) ([]string, []uint32, []uint32, error)
	AddressHistory(address string, N, offset int64, txnType dbtypes.AddrTxnViewType, year int64, month int64) ([]*dbtypes.AddressRow, *dbtypes.AddressBalance, error)
	FillAddressTransactions(addrInfo *dbtypes.AddressInfo) error
	AddressTransactionDetails(addr string, count, skip int64,
		txnType dbtypes.AddrTxnViewType) (*apitypes.Address, error)
	MutilchainAddressTransactionDetails(addr, chainType string, count, skip int64) (*apitypes.Address, error)
	MutilchainDBAddressTransactionDetails(addr, chainType string, count, skip int64) (*apitypes.MultichainAddress, error)
	AddressTotals(address string) (*apitypes.AddressTotals, error)
	MultichainAddressTotals(chainType, address string) (*apitypes.MultichainAddressTotals, error)
	VotesInBlock(hash string) (int16, error)
	TxHistoryData(address string, addrChart dbtypes.HistoryChart,
		chartGroupings dbtypes.TimeBasedGrouping) (*dbtypes.ChartsData, error)
	SwapsChartData(swapChart dbtypes.AtomicSwapChart,
		chartGroupings dbtypes.TimeBasedGrouping) (*dbtypes.ChartsData, error)
	TreasuryBalance() (*dbtypes.TreasuryBalance, error)
	BinnedTreasuryIO(chartGroupings dbtypes.TimeBasedGrouping) (*dbtypes.ChartsData, error)
	TicketPoolVisualization(interval dbtypes.TimeBasedGrouping) (
		*dbtypes.PoolTicketsData, *dbtypes.PoolTicketsData, *dbtypes.PoolTicketsData, int64, error)
	AgendaVotes(agendaID string, chartType int) (*dbtypes.AgendaVoteChoices, error)
	TSpendTransactionVotes(tspendHash string, chartType int) (*dbtypes.AgendaVoteChoices, error)
	AddressRowsCompact(address string) ([]*dbtypes.AddressRowCompact, error)
	Height() int64
	IsDCP0010Active(height int64) bool
	IsDCP0011Active(height int64) bool
	IsDCP0012Active(height int64) bool
	AllAgendas() (map[string]dbtypes.MileStone, error)
	GetTicketInfo(txid string) (*apitypes.TicketInfo, error)
	PowerlessTickets() (*apitypes.PowerlessTickets, error)
	GetStakeInfoExtendedByHash(hash string) *apitypes.StakeInfoExtended
	GetStakeInfoExtendedByHeight(idx int) *apitypes.StakeInfoExtended
	GetPoolInfo(idx int) *apitypes.TicketPoolInfo
	GetPoolInfoByHash(hash string) *apitypes.TicketPoolInfo
	GetPoolInfoRange(idx0, idx1 int) []apitypes.TicketPoolInfo
	GetPoolValAndSizeRange(idx0, idx1 int) ([]float64, []uint32)
	GetPool(idx int64) ([]string, error)
	CurrentCoinSupply() *apitypes.CoinSupply
	GetHeader(idx int) *chainjson.GetBlockHeaderVerboseResult
	GetBlockHeaderByHash(hash string) (*wire.BlockHeader, error)
	GetBlockVerboseByHash(hash string, verboseTx bool) *chainjson.GetBlockVerboseResult
	GetAPITransaction(txid *chainhash.Hash) *apitypes.Tx
	GetTransactionHex(txid *chainhash.Hash) string
	GetTrimmedTransaction(txid *chainhash.Hash) *apitypes.TrimmedTx
	GetVoteInfo(txid *chainhash.Hash) (*apitypes.VoteInfo, error)
	GetVoteVersionInfo(ver uint32) (*chainjson.GetVoteInfoResult, error)
	GetStakeVersionsLatest() (*chainjson.StakeVersions, error)
	GetAllTxIn(txid *chainhash.Hash) []*apitypes.TxIn
	GetAllTxOut(txid *chainhash.Hash) []*apitypes.TxOut
	GetMultichainAllTxIn(chainType string, txid string) ([]*apitypes.MultichainTxIn, error)
	GetMultichainAllTxOut(chainType string, txid string) ([]*apitypes.MultichainTxOut, error)
	GetTransactionsForBlockByHash(hash string) *apitypes.BlockTransactions
	GetStakeDiffEstimates() *apitypes.StakeDiff
	GetSummary(idx int) *apitypes.BlockDataBasic
	GetSummaryRange(idx0, idx1 int) []*apitypes.BlockDataBasic
	GetSummaryRangeStepped(idx0, idx1, step int) []*apitypes.BlockDataBasic
	GetSummaryByHash(hash string, withTxTotals bool) *apitypes.BlockDataBasic
	GetBestBlockSummary() *apitypes.BlockDataBasic
	GetBlockSize(idx int) (int32, error)
	GetBlockSizeRange(idx0, idx1 int) ([]int32, error)
	GetSDiff(idx int) float64
	GetSDiffRange(idx0, idx1 int) []float64
	GetMempoolSSTxSummary() *apitypes.MempoolTicketFeeInfo
	GetMempoolSSTxFeeRates(N int) *apitypes.MempoolTicketFees
	GetMempoolSSTxDetails(N int) *apitypes.MempoolTicketDetails
	GetAddressTransactionsRawWithSkip(addr string, count, skip int) []*apitypes.AddressTxRaw
	GetMempoolPriceCountTime() *apitypes.PriceCountTime
	GetAllProposalMeta(searchKey string) (list []map[string]string, err error)
	GetProposalByToken(token string) (proposalMeta map[string]string, err error)
	GetProposalByDomain(domain string) (proposalMetaList []map[string]string, err error)
	GetTreasurySummary() ([]*dbtypes.TreasurySummary, error)
	GetLegacySummary() ([]*dbtypes.TreasurySummary, error)
	GetProposalMetaByMonth(year int, month int) (list []map[string]string, err error)
	GetProposalMetaByYear(year int) (list []map[string]string, err error)
	GetTreasurySummaryByMonth(year int, month int) (*dbtypes.TreasurySummary, error)
	GetLegacySummaryByMonth(year int, month int) (*dbtypes.TreasurySummary, error)
	GetTreasurySummaryByYear(year int) (*dbtypes.TreasurySummary, error)
	GetTreasurySummaryGroupByMonth(year int) ([]dbtypes.TreasurySummary, error)
	GetLegacySummaryByYear(year int) (*dbtypes.TreasurySummary, error)
	GetAllProposalDomains() []string
	GetAllProposalOwners() []string
	GetAllProposalTokens() []string
	GetProposalByOwner(name string) (proposalMetaList []map[string]string, err error)
	SendRawTransaction(txhex string) (string, error)
	GetCurrencyPriceMapByPeriod(from time.Time, to time.Time, isSync bool) map[string]float64
	GetTreasuryTimeRange() (int64, int64, error)
	GetLegacyTimeRange() (int64, int64, error)
	GetMonthlyPrice(year, month int) (float64, error)
	GetLegacySummaryGroupByMonth(year int) ([]dbtypes.TreasurySummary, error)
	GetTreasurySummaryAllYear() ([]dbtypes.TreasurySummary, error)
	GetLegacySummaryAllYear() ([]dbtypes.TreasurySummary, error)
	GetMultichainTransactionHex(txid, chainType string) string
	GetMultichainTransactionVerbose(txid, chainType string) (*apitypes.MultichainTxRaw, error)
	GetMultichainSwapInfoData(txid, chainType string) (swapsInfo *txhelpers.TxAtomicSwaps, err error)
	InsertIPRangeToBlackList(iprange, note string) error
	CheckIPRangeOnBlackList(ipRange string) (bool, error)
	MoneroDecodeOutputs(txid, address, viewkey string) ([]externalapi.TxOutput, error)
	MoneroProveOutputs(txid, address, txkey string) ([]externalapi.TxOutput, error)
	GetMoneroTransaction(txhash string) (any, error)
	GetMoneroLastestTransactions() (any, error)
	GetMoneroBlockDetail(heightOrHash string) (any, error)
	GetMoneroMempoolDetail() (any, error)
	GetMoneroNetworkInfo() (any, error)
	GetMoneroRawTransaction(txhash string) (any, error)
	MutilchainAPIAddressTransactionDetails(addr, chainType string, count, skip int64) (*externalapi.APIAddressInfo, error)
}

// dcrdata application context used by all route handlers
type appContext struct {
	nodeClient       *rpcclient.Client
	btcNodeClient    *btcClient.Client
	ltcNodeClient    *ltcClient.Client
	Params           *chaincfg.Params
	DataSource       DataSource
	Status           *apitypes.Status
	xcBot            *exchanges.ExchangeBot
	AgendaDB         *agendas.AgendaDB
	ProposalsDB      *politeia.ProposalsDB
	maxCSVAddrs      int
	charts           *cache.ChartData
	LtcCharts        *cache.MutilchainChartData
	BtcCharts        *cache.MutilchainChartData
	XmrCharts        *cache.MutilchainChartData
	ChainDisabledMap map[string]bool
	CoinCaps         []string
	CoinCapDataList  []*dbtypes.MarketCapData
}

// AppContextConfig is the configuration for the appContext and the only
// argument to its constructor.
type AppContextConfig struct {
	Client            *rpcclient.Client
	BtcClient         *btcClient.Client
	LtcClient         *ltcClient.Client
	Params            *chaincfg.Params
	DataSource        DataSource
	XcBot             *exchanges.ExchangeBot
	AgendasDBInstance *agendas.AgendaDB
	ProposalsDB       *politeia.ProposalsDB
	MaxAddrs          int
	Charts            *cache.ChartData
	LtcCharts         *cache.MutilchainChartData
	BtcCharts         *cache.MutilchainChartData
	AppVer            string
	ChainDisabledMap  map[string]bool
	CoinCaps          []string
}

type simulationRow struct {
	SimBlock         float64 `json:"height"`
	SimDay           int     `json:"day"`
	TicketPrice      float64 `json:"ticket_price"`
	MatrueTickets    float64 `json:"matured_tickets"`
	DCRBalance       float64 `json:"dcr_balance"`
	TicketsPurchased float64 `json:"tickets_purchased"`
	Reward           float64 `json:"reward"`
	ReturnedFund     float64 `json:"returned_fund"`
}

// NewContext constructs a new appContext from the RPC client and database, and
// JSON indentation string.
func NewContext(cfg *AppContextConfig) *appContext {
	conns, _ := cfg.Client.GetConnectionCount(context.TODO())
	nodeHeight, _ := cfg.Client.GetBlockCount(context.TODO())

	// DataSource is an interface that could have a value of pointer type.
	if cfg.DataSource == nil || reflect.ValueOf(cfg.DataSource).IsNil() {
		log.Errorf("NewContext: a DataSource is required.")
		return nil
	}

	return &appContext{
		nodeClient:       cfg.Client,
		btcNodeClient:    cfg.BtcClient,
		ltcNodeClient:    cfg.LtcClient,
		Params:           cfg.Params,
		DataSource:       cfg.DataSource,
		xcBot:            cfg.XcBot,
		AgendaDB:         cfg.AgendasDBInstance,
		ProposalsDB:      cfg.ProposalsDB,
		Status:           apitypes.NewStatus(uint32(nodeHeight), conns, APIVersion, cfg.AppVer, cfg.Params.Name),
		maxCSVAddrs:      cfg.MaxAddrs,
		charts:           cfg.Charts,
		ChainDisabledMap: cfg.ChainDisabledMap,
		CoinCaps:         cfg.CoinCaps,
	}
}

func (c *appContext) updateNodeConnections() error {
	nodeConnections, err := c.nodeClient.GetConnectionCount(context.TODO())
	if err != nil {
		// Assume there arr no connections if RPC had an error.
		c.Status.SetConnections(0)
		return fmt.Errorf("failed to get connection count: %v", err)
	}

	// Before updating connections, get the previous connection count.
	prevConnections := c.Status.NodeConnections()

	c.Status.SetConnections(nodeConnections)
	if nodeConnections == 0 {
		return nil
	}

	// Detect if the node's peer connections were just restored.
	if prevConnections != 0 {
		// Status.ready may be false, but since connections were not lost and
		// then recovered, it is not our job to check other readiness factors.
		return nil
	}

	// Check the reconnected node's best block, and update Status.height.
	_, nodeHeight, err := c.nodeClient.GetBestBlock(context.TODO())
	if err != nil {
		c.Status.SetReady(false)
		return fmt.Errorf("node: getbestblock failed: %v", err)
	}

	// Update Status.height with current node height. This also sets
	// Status.ready according to the previously-set Status.dbHeight.
	c.Status.SetHeight(uint32(nodeHeight))

	return nil
}

// UpdateNodeHeight updates the Status height. This method satisfies
// notification.BlockHandlerLite.
func (c *appContext) UpdateNodeHeight(height uint32, _ string) error {
	c.Status.SetHeight(height)
	return nil
}

// StatusNtfnHandler keeps the appContext's Status up-to-date with changes in
// node and DB status.
func (c *appContext) StatusNtfnHandler(ctx context.Context, wg *sync.WaitGroup, wireHeightChan chan uint32) {
	defer wg.Done()
	// Check the node connection count periodically.
	rpcCheckTicker := time.NewTicker(5 * time.Second)
out:
	for {
	keepon:
		select {
		case <-rpcCheckTicker.C:
			if err := c.updateNodeConnections(); err != nil {
				log.Warn("updateNodeConnections: ", err)
				break keepon
			}

		case height, ok := <-wireHeightChan:
			if !ok {
				log.Warnf("Block connected channel closed.")
				break out
			}

			if c.DataSource == nil {
				panic("BlockData DataSourceLite is nil")
			}

			summary := c.DataSource.GetBestBlockSummary()
			if summary == nil {
				log.Errorf("BlockData summary is nil for height %d.", height)
				break keepon
			}

			c.Status.DBUpdate(height, summary.Time.UNIX())

			bdHeight, err := c.DataSource.GetHeight()
			// Catch certain pathological conditions.
			switch {
			case err != nil:
				log.Errorf("GetHeight failed: %v", err)
			case (height != uint32(bdHeight)) || (height != summary.Height):
				log.Errorf("New DB height (%d) and stored block data (%d, %d) are not consistent.",
					height, bdHeight, summary.Height)
			case bdHeight < 0:
				log.Warnf("DB empty (height = %d)", bdHeight)
			default:
				// If DB height agrees with node height, then we're ready.
				break keepon
			}

			c.Status.SetReady(false)

		case <-ctx.Done():
			log.Debugf("Got quit signal. Exiting block connected handler for STATUS monitor.")
			rpcCheckTicker.Stop()
			break out
		}
	}
}

// root is a http.Handler intended for the API root path. This essentially
// provides a heartbeat, and no information about the application status.
func (c *appContext) root(w http.ResponseWriter, _ *http.Request) {
	fmt.Fprint(w, "dcrdata api running")
}

func writeJSON(w http.ResponseWriter, thing interface{}, indent string) {
	writeJSONWithStatus(w, thing, http.StatusOK, indent)
}

func writeJSONWithStatus(w http.ResponseWriter, thing interface{}, code int, indent string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", indent)
	if err := encoder.Encode(thing); err != nil {
		apiLog.Infof("JSON encode error: %v", err)
	}
}

// writeJSONBytes prepares the headers for pre-encoded JSON and writes the JSON
// bytes.
func writeJSONBytes(w http.ResponseWriter, data []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, err := w.Write(data)
	if err != nil {
		apiLog.Warnf("ResponseWriter.Write error: %v", err)
	}
}

func getVoteVersionQuery(r *http.Request) (int32, string, error) {
	verLatest := int64(m.GetLatestVoteVersionCtx(r))
	voteVersion := r.URL.Query().Get("version")
	if voteVersion == "" {
		return int32(verLatest), voteVersion, nil
	}

	ver, err := strconv.ParseInt(voteVersion, 10, 0)
	if err != nil {
		return -1, voteVersion, err
	}
	if ver > verLatest {
		ver = verLatest
	}

	return int32(ver), voteVersion, nil
}

func (c *appContext) status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, c.Status.API(), m.GetIndentCtx(r))
}

func (c *appContext) statusHappy(w http.ResponseWriter, r *http.Request) {
	happy := c.Status.Happy()
	statusCode := http.StatusOK
	if !happy.Happy {
		// For very simple health checks, set the status code.
		statusCode = http.StatusServiceUnavailable
	}
	writeJSONWithStatus(w, happy, statusCode, m.GetIndentCtx(r))
}

func (c *appContext) coinSupply(w http.ResponseWriter, r *http.Request) {
	supply := c.DataSource.CurrentCoinSupply()
	if supply == nil {
		apiLog.Error("Unable to get coin supply.")
		http.Error(w, http.StatusText(422), 422)
		return
	}

	writeJSON(w, supply, m.GetIndentCtx(r))
}

func (c *appContext) coinSupplyCirculating(w http.ResponseWriter, r *http.Request) {
	var dcr bool
	if dcrParam := r.URL.Query().Get("dcr"); dcrParam != "" {
		var err error
		dcr, err = strconv.ParseBool(dcrParam)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
	}

	supply := c.DataSource.CurrentCoinSupply()
	if supply == nil {
		apiLog.Error("Unable to get coin supply.")
		http.Error(w, http.StatusText(422), 422)
		return
	}

	if dcr {
		coinSupply := dcrutil.Amount(supply.Mined).ToCoin()
		writeJSONBytes(w, []byte(strconv.FormatFloat(coinSupply, 'f', 8, 64)))
		return
	}

	writeJSONBytes(w, []byte(strconv.FormatInt(supply.Mined, 10)))
}

func (c *appContext) currentHeight(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if _, err := io.WriteString(w, strconv.Itoa(int(c.Status.Height()))); err != nil {
		apiLog.Infof("failed to write height response: %v", err)
	}
}

func (c *appContext) getBlockHeight(w http.ResponseWriter, r *http.Request) {
	idx, err := c.getBlockHeightCtx(r)
	if err != nil {
		apiLog.Debugf("getBlockHeight: getBlockHeightCtx failed: %v", err)
		http.Error(w, http.StatusText(422), 422)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if _, err := io.WriteString(w, strconv.Itoa(int(idx))); err != nil {
		apiLog.Infof("failed to write height response: %v", err)
	}
}

func (c *appContext) getBlockHash(w http.ResponseWriter, r *http.Request) {
	hash, err := c.getBlockHashCtx(r)
	if err != nil {
		apiLog.Debugf("getBlockHash: %v", err)
		http.Error(w, http.StatusText(422), 422)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if _, err := io.WriteString(w, hash); err != nil {
		apiLog.Infof("failed to write height response: %v", err)
	}
}

func (c *appContext) getBlockSummary(w http.ResponseWriter, r *http.Request) {
	var withTxTotals bool
	if txTotalsParam := r.URL.Query().Get("txtotals"); txTotalsParam != "" {
		b, err := strconv.ParseBool(txTotalsParam)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}

		withTxTotals = b
	}

	// Attempt to get hash of block set by hash or (fallback) height set on
	// path.
	hash, err := c.getBlockHashCtx(r)
	if err != nil {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	blockSummary := c.DataSource.GetSummaryByHash(hash, withTxTotals)
	if blockSummary == nil {
		apiLog.Errorf("Unable to get block %s summary", hash)
		http.Error(w, http.StatusText(422), 422)
		return
	}

	writeJSON(w, blockSummary, m.GetIndentCtx(r))
}

func (c *appContext) getBlockTransactions(w http.ResponseWriter, r *http.Request) {
	hash, err := c.getBlockHashCtx(r)
	if err != nil {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	blockTransactions := c.DataSource.GetTransactionsForBlockByHash(hash)
	if blockTransactions == nil {
		apiLog.Errorf("Unable to get block %s transactions", hash)
		http.Error(w, http.StatusText(422), 422)
		return
	}

	writeJSON(w, blockTransactions, m.GetIndentCtx(r))
}

func (c *appContext) getBlockTransactionsCount(w http.ResponseWriter, r *http.Request) {
	hash, err := c.getBlockHashCtx(r)
	if err != nil {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	blockTransactions := c.DataSource.GetTransactionsForBlockByHash(hash)
	if blockTransactions == nil {
		apiLog.Errorf("Unable to get block %s transactions", hash)
		http.Error(w, http.StatusText(http.StatusInternalServerError),
			http.StatusInternalServerError)
		return
	}

	counts := &apitypes.BlockTransactionCounts{
		Tx:  len(blockTransactions.Tx),
		STx: len(blockTransactions.STx),
	}
	writeJSON(w, counts, m.GetIndentCtx(r))
}

func (c *appContext) getBlockHeader(w http.ResponseWriter, r *http.Request) {
	idx, err := c.getBlockHeightCtx(r)
	if err != nil {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	blockHeader := c.DataSource.GetHeader(int(idx))
	if blockHeader == nil {
		apiLog.Errorf("Unable to get block %d header", idx)
		http.Error(w, http.StatusText(422), 422)
		return
	}

	writeJSON(w, blockHeader, m.GetIndentCtx(r))
}

func (c *appContext) getBlockRaw(w http.ResponseWriter, r *http.Request) {
	hash, err := c.getBlockHashCtx(r)
	if err != nil {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	msgBlock, err := c.DataSource.GetBlockByHash(hash)
	if err != nil {
		apiLog.Errorf("Unable to get block %s: %v", hash, err)
		http.Error(w, http.StatusText(422), 422)
		return
	}

	var hexString strings.Builder
	hexString.Grow(msgBlock.SerializeSize())
	err = msgBlock.Serialize(hex.NewEncoder(&hexString))
	if err != nil {
		apiLog.Errorf("Unable to serialize block %s: %v", hash, err)
		http.Error(w, http.StatusText(422), 422)
		return
	}

	blockRaw := &apitypes.BlockRaw{
		Height: msgBlock.Header.Height,
		Hash:   hash,
		Hex:    hexString.String(),
	}

	writeJSON(w, blockRaw, m.GetIndentCtx(r))
}

func (c *appContext) getBlockHeaderRaw(w http.ResponseWriter, r *http.Request) {
	hash, err := c.getBlockHashCtx(r)
	if err != nil {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	blockHeader, err := c.DataSource.GetBlockHeaderByHash(hash)
	if err != nil {
		apiLog.Errorf("Unable to get block %s: %v", hash, err)
		http.Error(w, http.StatusText(422), 422)
		return
	}

	var hexString strings.Builder
	err = blockHeader.Serialize(hex.NewEncoder(&hexString))
	if err != nil {
		apiLog.Errorf("Unable to serialize block %s: %v", hash, err)
		http.Error(w, http.StatusText(422), 422)
		return
	}

	blockRaw := &apitypes.BlockRaw{
		Height: blockHeader.Height,
		Hash:   hash,
		Hex:    hexString.String(),
	}

	writeJSON(w, blockRaw, m.GetIndentCtx(r))
}

func (c *appContext) getBlockVerbose(w http.ResponseWriter, r *http.Request) {
	hash, err := c.getBlockHashCtx(r)
	if err != nil {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	blockVerbose := c.DataSource.GetBlockVerboseByHash(hash, false)
	if blockVerbose == nil {
		apiLog.Errorf("Unable to get block %s", hash)
		http.Error(w, http.StatusText(422), 422)
		return
	}

	writeJSON(w, blockVerbose, m.GetIndentCtx(r))
}

func (c *appContext) getVoteInfo(w http.ResponseWriter, r *http.Request) {
	ver, verStr, err := getVoteVersionQuery(r)
	if err != nil || ver < 0 {
		apiLog.Errorf("Unable to get vote info for stake version %s", verStr)
		http.Error(w, "Unable to get vote info for stake version "+html.EscapeString(verStr), 422)
		return
	}
	voteVersionInfo, err := c.DataSource.GetVoteVersionInfo(uint32(ver))
	if err != nil || voteVersionInfo == nil {
		apiLog.Errorf("Unable to get vote version %d info: %v", ver, err)
		http.Error(w, "Unable to get vote info for stake version "+html.EscapeString(verStr), 422)
		return
	}
	writeJSON(w, voteVersionInfo, m.GetIndentCtx(r))
}

// setOutputSpends retrieves spending transaction information for each output of
// the specified transaction. This sets the vouts[i].Spend fields for each
// output that is spent. For unspent outputs, the Spend field remains a nil
// pointer.
func (c *appContext) setOutputSpends(txid string, vouts []apitypes.Vout) error {
	// For each output of this transaction, look up any spending transactions,
	// and the index of the spending transaction input.
	spendHashes, spendVinInds, voutInds, err := c.DataSource.SpendingTransactions(txid)
	if dbtypes.IsTimeoutErr(err) {
		return fmt.Errorf("SpendingTransactions: %v", err)
	}
	if err != nil && !errors.Is(err, dbtypes.ErrNoResult) {
		return fmt.Errorf("unable to get spending transaction info for outputs of %s", txid)
	}
	if len(voutInds) > len(vouts) {
		return fmt.Errorf("invalid spending transaction data for %s", txid)
	}
	for i, vout := range voutInds {
		if int(vout) >= len(vouts) {
			return fmt.Errorf("invalid spending transaction data (%s:%d)", txid, vout)
		}
		vouts[vout].Spend = &apitypes.TxInputID{
			Hash:  spendHashes[i],
			Index: spendVinInds[i],
		}
	}
	return nil
}

// setTxSpends retrieves spending transaction information for each output of the
// given transaction. This sets the tx.Vout[i].Spend fields for each output that
// is spent. For unspent outputs, the Spend field remains a nil pointer.
func (c *appContext) setTxSpends(tx *apitypes.Tx) error {
	return c.setOutputSpends(tx.TxID, tx.Vout)
}

// setTrimmedTxSpends is like setTxSpends except that it operates on a TrimmedTx
// instead of a Tx.
func (c *appContext) setTrimmedTxSpends(tx *apitypes.TrimmedTx) error {
	return c.setOutputSpends(tx.TxID, tx.Vout)
}

// getTransaction handles the /tx/{txid} API endpoint.
func (c *appContext) getTransaction(w http.ResponseWriter, r *http.Request) {
	// Look up any spending transactions for each output of this transaction
	// when the client requests spends with the URL query ?spends=true.
	var withSpends bool
	if spendParam := r.URL.Query().Get("spends"); spendParam != "" {
		b, err := strconv.ParseBool(spendParam)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}

		withSpends = b
	}

	txid, err := m.GetTxIDCtx(r)
	if err != nil {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	tx := c.DataSource.GetAPITransaction(txid)
	if tx == nil {
		apiLog.Errorf("Unable to get transaction %s", txid)
		http.Error(w, http.StatusText(422), 422)
		return
	}

	if withSpends {
		if err := c.setTxSpends(tx); err != nil {
			errStr := html.EscapeString(err.Error())
			apiLog.Errorf("Unable to get spending transaction info for outputs of %s: %q", txid, errStr)
			http.Error(w, http.StatusText(http.StatusInternalServerError),
				http.StatusInternalServerError)
			return
		}
	}

	writeJSON(w, tx, m.GetIndentCtx(r))
}

func (c *appContext) getTransactionHex(w http.ResponseWriter, r *http.Request) {
	txid, err := m.GetTxIDCtx(r)
	if err != nil {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	hex := c.DataSource.GetTransactionHex(txid)

	fmt.Fprint(w, hex)
}

func (c *appContext) getMultichainTransactionHex(w http.ResponseWriter, r *http.Request) {
	chainType, txid := m.GetMultichainTxID(r)
	if chainType == "" || txid == "" {
		http.Error(w, http.StatusText(422), 422)
		return
	}
	hex := c.DataSource.GetMultichainTransactionHex(txid, chainType)
	fmt.Fprint(w, hex)
}

// getTxSwapsInfo checks the inputs and outputs of the specified transaction for
// information about completed atomic swaps that were created and/or redeemed in
// the transaction.
func (c *appContext) getMultichainTxSwapsInfo(w http.ResponseWriter, r *http.Request) {
	chainType, txid := m.GetMultichainTxID(r)
	if chainType == "" || txid == "" {
		http.Error(w, http.StatusText(422), 422)
		return
	}
	swapsInfo, err := c.DataSource.GetMultichainSwapInfoData(txid, chainType)
	swapsInfo.TxID = txid
	if err != nil {
		apiLog.Errorf("getMultichainTxSwapsInfo get swapsInfo failed: Type: %s, txid: %s", chainType, txid)
		http.Error(w, http.StatusText(422), 422)
		return
	}
	writeJSON(w, swapsInfo, m.GetIndentCtx(r))
}

func (c *appContext) getMultichainDecodedTx(w http.ResponseWriter, r *http.Request) {
	chainType, txid := m.GetMultichainTxID(r)
	if chainType == "" || txid == "" {
		http.Error(w, http.StatusText(422), 422)
		return
	}
	// get monero tx detail
	if chainType == mutilchain.TYPEXMR {
		apiLog.Errorf("This path not support for %s", chainType)
		http.Error(w, http.StatusText(422), 422)
		return
	}
	tx, err := c.DataSource.GetMultichainTransactionVerbose(txid, chainType)
	if err != nil {
		apiLog.Errorf("Unable to get multichain transaction: Type: %s, txid: %s", chainType, txid)
		http.Error(w, http.StatusText(422), 422)
		return
	}
	writeJSON(w, *tx, m.GetIndentCtx(r))
}

func (c *appContext) getProposalTimeMinMax() (int64, int64, error) {
	//Get All Proposal Metadata for Report
	proposalMetaList, err := c.DataSource.GetAllProposalMeta("")
	minTimeUnix := int64(-1)
	maxTimeUnix := int64(-1)
	if err != nil {
		log.Errorf("Get proposals all meta data failed: %v", err)
		return minTimeUnix, maxTimeUnix, err
	}
	for _, proposalMeta := range proposalMetaList {
		var startDate = proposalMeta["StartDate"]
		var endDate = proposalMeta["EndDate"]
		startInt, err := strconv.ParseInt(startDate, 0, 32)
		endInt, err2 := strconv.ParseInt(endDate, 0, 32)
		if err != nil || err2 != nil {
			continue
		}
		if startInt > 0 {
			if minTimeUnix == -1 {
				minTimeUnix = startInt
			} else if minTimeUnix > startInt {
				minTimeUnix = startInt
			}
		}
		if endInt > 0 {
			if maxTimeUnix < endInt {
				maxTimeUnix = endInt
			}
		}
	}
	return minTimeUnix, maxTimeUnix, nil
}

// updated: 27/01/2024 - Algorithm change. Displays all months containing that proposal, excluding future months
func (c *appContext) getProposalReportData(searchKey string) ([]apitypes.MonthReportObject, []apitypes.ProposalReportData, []string, []string, map[string]string, float64, float64, []apitypes.AuthorDataObject) {
	//Get All Proposal Metadata for Report
	proposalMetaList, err := c.DataSource.GetAllProposalMeta(searchKey)
	if err != nil {
		log.Errorf("Get proposals all meta data failed: %v", err)
		return nil, nil, nil, nil, nil, 0.0, 0.0, nil
	}
	// create report data map
	report := make(map[string][]apitypes.MonthReportData)
	proposalReportData := make([]apitypes.ProposalReportData, 0)
	domainList := make([]string, 0)
	authorList := make([]string, 0)
	proposalList := make([]string, 0)
	proposalTokenMap := make(map[string]string)
	now := time.Now()
	var nowCompare = now.Year()*12 + int(now.Month())
	var count = 0
	minDate := time.Now()
	var totalAllSpent = 0.0
	var totalAllBudget = 0.0
	var lastTime = time.Now()
	//author report map
	authorReportMap := make(map[string]apitypes.AuthorDataObject)
	for _, proposalMeta := range proposalMetaList {
		var amount = proposalMeta["Amount"]
		var token = proposalMeta["Token"]
		var name = proposalMeta["Name"]
		var author = proposalMeta["Username"]
		var startDate = proposalMeta["StartDate"]
		var endDate = proposalMeta["EndDate"]
		var domain = proposalMeta["Domain"]
		//parse date time of startDate and endDate
		startInt, err := strconv.ParseInt(startDate, 0, 32)
		endInt, err2 := strconv.ParseInt(endDate, 0, 32)
		amountFloat, err3 := strconv.ParseFloat(amount, 64)
		if amountFloat == 0.0 || err != nil || err2 != nil || err3 != nil {
			continue
		}
		amountFloat = amountFloat / 100
		startTime := time.Unix(startInt, 0)
		//If the proposal's starting month is after this month (including this month), then ignore it and not include it in the report
		//comment at 27/01/2024
		// if startTime.After(now) || (startTime.Month() == now.Month() && startTime.Year() == now.Year()) {
		// 	continue
		// }

		proposalTokenMap[name] = token
		if !slices.Contains(proposalList, name) {
			proposalList = append(proposalList, name)
		}
		if !slices.Contains(domainList, domain) {
			domainList = append(domainList, domain)
		}
		if !slices.Contains(authorList, author) {
			authorList = append(authorList, author)
		}
		if minDate.After(startTime) {
			minDate = startTime
		}
		count++
		endTime := time.Unix(endInt, 0)
		if endTime.After(lastTime) {
			lastTime = endTime
		}
		//count month from startTime to endTime
		difference := endTime.Sub(startTime)
		var countMonths = 12*(endTime.Year()-startTime.Year()) + (int(endTime.Month()) - int(startTime.Month())) + 1
		//count day from startTime to endTime
		countDays := int16(difference.Hours()/24) + 1
		costPerDay := amountFloat / float64(countDays)
		tempTime := time.Unix(startInt, 0)
		proposalInfo := apitypes.ProposalReportData{}
		proposalInfo.Name = name
		proposalInfo.Token = token
		proposalInfo.Author = author
		proposalInfo.Budget = amountFloat
		proposalInfo.Domain = domain
		totalAllBudget += amountFloat
		proposalInfo.Start = startTime.Format("2006-01-02")
		proposalInfo.End = endTime.Format("2006-01-02")
		var totalSpent = 0.0

		//if start month and end month are the same, month data is proposal data
		if startTime.Month() == endTime.Month() && startTime.Year() == endTime.Year() {
			varMonthData := apitypes.MonthReportData{}
			varMonthData.Token = token
			varMonthData.Name = name
			varMonthData.Author = author
			varMonthData.Expense = amountFloat
			varMonthData.Domain = domain
			var timeCompare = startTime.Year()*12 + int(startTime.Month())
			if timeCompare < nowCompare {
				totalSpent += amountFloat
			}
			key := fmt.Sprintf("%d/%s", startTime.Year(), apitypes.GetFullMonthDisplay(int(startTime.Month())))
			val, ok := report[key]
			//if map has month key
			if ok {
				val = append(val, varMonthData)
				report[key] = val
				//if don't have month key
			} else {
				newMonthDataArr := make([]apitypes.MonthReportData, 0)
				newMonthDataArr = append(newMonthDataArr, varMonthData)
				report[key] = newMonthDataArr
			}
		} else {
			//calculate cost every month
			for i := 0; i < int(countMonths); i++ {
				handlerTime := tempTime.AddDate(0, i, 0)
				//if month is this month or future months, break loop
				//comment at 27/01/2024
				// if handlerTime.After(now) || (handlerTime.Month() == now.Month() && handlerTime.Year() == now.Year()) {
				// 	break
				// }
				key := fmt.Sprintf("%d/%s", handlerTime.Year(), apitypes.GetFullMonthDisplay(int(handlerTime.Month())))
				val, ok := report[key]
				var costOfMonth float64
				//if start month
				if i == 0 {
					//get end day of month
					endDay := startTime.AddDate(0, 1, -startTime.Day())
					countToEndMonth := endDay.Day() - startTime.Day() + 1
					costOfMonth = float64(countToEndMonth) * costPerDay
				} else if i == int(countMonths)-1 {
					//get start day of month
					startDay := endTime.AddDate(0, 0, -endTime.Day()+1)
					countFromStartMonth := endTime.Day() - startDay.Day() + 1
					costOfMonth = float64(countFromStartMonth) * costPerDay
					//if other
				} else {
					startDay := handlerTime.AddDate(0, 0, -handlerTime.Day()+1)
					endDay := handlerTime.AddDate(0, 1, -handlerTime.Day())
					countDaysOfMonth := endDay.Day() - startDay.Day() + 1
					costOfMonth = float64(countDaysOfMonth) * costPerDay
				}
				costOfMonth = math.Ceil(costOfMonth*100) / 100
				varMonthData := apitypes.MonthReportData{}
				varMonthData.Token = token
				varMonthData.Name = name
				varMonthData.Author = author
				varMonthData.Expense = costOfMonth
				varMonthData.Domain = domain
				var timeCompare = handlerTime.Year()*12 + int(handlerTime.Month())
				if timeCompare < nowCompare {
					totalSpent += costOfMonth
				}
				if ok {
					val = append(val, varMonthData)
					report[key] = val
					//if don't have month key
				} else {
					newMonthDataArr := make([]apitypes.MonthReportData, 0)
					newMonthDataArr = append(newMonthDataArr, varMonthData)
					report[key] = newMonthDataArr
				}
			}
		}
		if now.After(endTime) {
			totalSpent = amountFloat
		}
		totalAllSpent += totalSpent
		proposalInfo.TotalSpent = math.Ceil(totalSpent*100) / 100
		proposalInfo.TotalRemaining = math.Ceil((amountFloat-totalSpent)*100) / 100
		proposalReportData = append(proposalReportData, proposalInfo)
		authorInfo, authorExist := authorReportMap[author]
		if authorExist {
			authorInfo.Budget += amountFloat
			authorInfo.Proposals += 1
			authorInfo.TotalReceived += proposalInfo.TotalSpent
			authorInfo.TotalRemaining += proposalInfo.TotalRemaining
		} else {
			authorInfo = apitypes.AuthorDataObject{
				Name:           author,
				Proposals:      1,
				Budget:         amountFloat,
				TotalReceived:  proposalInfo.TotalSpent,
				TotalRemaining: proposalInfo.TotalRemaining,
			}
		}
		authorReportMap[author] = authorInfo
	}

	//recalc author report array
	authorReportArray := make([]apitypes.AuthorDataObject, 0)
	for _, authorInfo := range authorReportMap {
		authorReportArray = append(authorReportArray, authorInfo)
	}

	monthReportList := make([]apitypes.MonthReportObject, 0)
	//get monthly USD rate by lastest month
	monthlyPriceMap := c.DataSource.GetCurrencyPriceMapByPeriod(minDate, now, false)
	var countMonthFromStart = 12*(lastTime.Year()-minDate.Year()) + (int(lastTime.Month()) - int(minDate.Month())) + 1
	for i := int(countMonthFromStart) - 1; i >= 0; i-- {
		compareTime := minDate.AddDate(0, i, 0)
		key := fmt.Sprintf("%d/%s", compareTime.Year(), apitypes.GetFullMonthDisplay(int(compareTime.Month())))
		monthlyPriceMapKey := strings.ReplaceAll(key, "/", "-")
		val, ok := report[key]
		monthAllData := make([]apitypes.MonthReportData, 0)
		if ok {
			//count by domain
			domainMap := make(map[string]float64)
			var total = 0.0
			for _, data := range val {
				dataObj, ok := domainMap[data.Domain]
				total += data.Expense
				if ok {
					domainMap[data.Domain] = dataObj + data.Expense
				} else {
					domainMap[data.Domain] = data.Expense
				}
			}
			domainDataArr := make([]apitypes.DomainReportData, 0)
			for _, domain := range domainList {
				domainValue, domainHas := domainMap[domain]
				domainData := apitypes.DomainReportData{}
				if domainHas {
					domainData.Domain = domain
					domainData.Expense = math.Ceil(domainValue*100) / 100
				} else {
					domainData.Domain = ""
					domainData.Expense = 0.0
				}
				domainDataArr = append(domainDataArr, domainData)
			}
			//count by author
			authorMap := make(map[string]float64)
			for _, data := range val {
				dataObj, ok := authorMap[data.Author]
				if ok {
					authorMap[data.Author] = dataObj + data.Expense
				} else {
					authorMap[data.Author] = data.Expense
				}
			}
			//hanlder author list for month object data
			authorDataArr := make([]apitypes.AuthorReportData, 0)
			for _, author := range authorList {
				authorData := apitypes.AuthorReportData{}
				authorValue, authorHas := authorMap[author]
				if authorHas {
					authorData.Author = author
					authorData.Expense = math.Ceil(authorValue*100) / 100
				} else {
					authorData.Author = ""
					authorData.Expense = 0.0
				}
				authorDataArr = append(authorDataArr, authorData)
			}
			for _, proposal := range proposalList {
				var hasData = false
				var putData apitypes.MonthReportData
				for _, data := range val {
					if data.Name == proposal {
						hasData = true
						putData = data
						break
					}
				}
				if hasData {
					monthAllData = append(monthAllData, putData)
				} else {
					tempData := apitypes.MonthReportData{
						Expense: 0,
					}
					monthAllData = append(monthAllData, tempData)
				}
			}
			monthPrice, ok := monthlyPriceMap[monthlyPriceMapKey]
			if !ok {
				monthPrice = 0
			}
			reportMonthObj := apitypes.MonthReportObject{
				Month:      key,
				AllData:    monthAllData,
				DomainData: domainDataArr,
				AuthorData: authorDataArr,
				Total:      math.Ceil(total*100) / 100,
				UsdRate:    monthPrice,
			}
			monthReportList = append(monthReportList, reportMonthObj)
		}
	}
	return monthReportList, proposalReportData, domainList, proposalList, proposalTokenMap, totalAllSpent, totalAllBudget, authorReportArray
}

func (c *appContext) getProposalReport(w http.ResponseWriter, r *http.Request) {
	searchKey := r.URL.Query().Get("search")
	report, summary, domainList, proposalList, proposalTokenMap, allSpent, allBudget, authorReport := c.getProposalReportData(searchKey)
	treasurySummary, err := c.DataSource.GetTreasurySummary()
	legacySummary, legacyErr := c.DataSource.GetLegacySummary()
	if err != nil || legacyErr != nil {
		log.Errorf("Get treasury/legacy summary data failed: %v, %v", err, legacyErr)
		writeJSON(w, nil, m.GetIndentCtx(r))
		return
	}
	timeArr := make([]string, 0)
	combinedTreasurySummary := make([]*dbtypes.TreasurySummary, 0)
	combinedDataMap := make(map[string]*dbtypes.TreasurySummary)
	if treasurySummary != nil {
		for _, treasury := range treasurySummary {
			timeArr = append(timeArr, treasury.Month)
			combinedDataMap[treasury.Month] = treasury
		}
	}

	if legacySummary != nil {
		for _, legacy := range legacySummary {
			if !slices.Contains(timeArr, legacy.Month) {
				timeArr = append(timeArr, legacy.Month)
				combinedDataMap[legacy.Month] = legacy
			} else {
				existInMap, exist := combinedDataMap[legacy.Month]
				if exist {
					var treasuryInvalue = existInMap.Invalue
					var legacyOutValue = legacy.Outvalue
					var treasuryInvalueUSD = existInMap.InvalueUSD
					var legacyOutValueUSD = legacy.OutvalueUSD
					if existInMap.TaddValue > 0 {
						treasuryInvalue = existInMap.Invalue - existInMap.TaddValue
						legacyOutValue = legacy.Outvalue - existInMap.TaddValue
						treasuryInvalueUSD = existInMap.InvalueUSD - existInMap.TaddValueUSD
						legacyOutValueUSD = legacy.OutvalueUSD - existInMap.TaddValueUSD
					}
					existInMap.Invalue = treasuryInvalue + legacy.Invalue
					existInMap.InvalueUSD = treasuryInvalueUSD + legacy.InvalueUSD
					existInMap.Outvalue += legacyOutValue
					existInMap.OutvalueUSD += legacyOutValueUSD
					existInMap.Total += legacy.Total
					existInMap.TotalUSD += legacy.TotalUSD
					existInMap.Difference = int64(math.Abs(float64(existInMap.Invalue - existInMap.Outvalue)))
					existInMap.DifferenceUSD = existInMap.MonthPrice * float64(existInMap.Difference) / 1e8
					combinedDataMap[legacy.Month] = existInMap
				}
			}
		}
	}

	for _, value := range combinedDataMap {
		combinedTreasurySummary = append(combinedTreasurySummary, value)
	}

	//Get coin supply value
	writeJSON(w, struct {
		Report           []apitypes.MonthReportObject  `json:"report"`
		Summary          []apitypes.ProposalReportData `json:"summary"`
		DomainList       []string                      `json:"domainList"`
		ProposalList     []string                      `json:"proposalList"`
		ProposalTokenMap map[string]string             `json:"proposalTokenMap"`
		AllSpent         float64                       `json:"allSpent"`
		AllBudget        float64                       `json:"allBudget"`
		AuthorReport     []apitypes.AuthorDataObject   `json:"authorReport"`
		TreasurySummary  []*dbtypes.TreasurySummary    `json:"treasurySummary"`
	}{
		Report:           report,
		Summary:          summary,
		DomainList:       domainList,
		ProposalList:     proposalList,
		ProposalTokenMap: proposalTokenMap,
		AllSpent:         allSpent,
		AllBudget:        allBudget,
		AuthorReport:     authorReport,
		TreasurySummary:  combinedTreasurySummary,
	}, m.GetIndentCtx(r))
}

func getTimeCompare(timeStr string) int64 {
	timeArr := strings.Split(timeStr, "-")
	if len(timeArr) < 2 {
		return 0
	}
	year, yearErr := strconv.ParseInt(timeArr[0], 0, 32)
	if yearErr != nil {
		return 0
	}
	monthStr := ""
	if timeArr[1][0] == '0' {
		monthStr = timeArr[1][1:]
	} else {
		monthStr = timeArr[1]
	}
	month, monthErr := strconv.ParseInt(monthStr, 0, 32)
	if monthErr != nil {
		return 0
	}
	return year*12 + month
}

func (c *appContext) getReportTimeRange(w http.ResponseWriter, r *http.Request) {
	// get proposal time range
	pMinTimeInt, pMaxTimeInt, pErr := c.getProposalTimeMinMax()
	// get treasury time range
	tMinTimeInt, tMaxTimeInt, tErr := c.DataSource.GetTreasuryTimeRange()
	// get legacy time range
	lMinTimeInt, lMaxTimeInt, lErr := c.DataSource.GetLegacyTimeRange()
	if pErr != nil || tErr != nil || lErr != nil {
		writeJSON(w, nil, m.GetIndentCtx(r))
		return
	}
	minTime := int64(-1)
	maxTime := int64(-1)
	if pMinTimeInt > 0 {
		minTime = pMinTimeInt
	}
	if pMaxTimeInt > 0 {
		maxTime = pMaxTimeInt
	}

	if tMinTimeInt > 0 && tMinTimeInt < minTime {
		minTime = tMinTimeInt
	}

	if lMinTimeInt > 0 && lMinTimeInt < minTime {
		minTime = lMinTimeInt
	}

	if tMaxTimeInt > 0 && tMaxTimeInt > maxTime {
		maxTime = tMaxTimeInt
	}

	if lMaxTimeInt > 0 && lMaxTimeInt > maxTime {
		maxTime = lMaxTimeInt
	}

	minDate := time.Unix(minTime, 0)
	maxDate := time.Unix(maxTime, 0)

	//Get coin supply value
	writeJSON(w, struct {
		MinYear  int `json:"minYear"`
		MinMonth int `json:"minMonth"`
		MaxYear  int `json:"maxYear"`
		MaxMonth int `json:"maxMonth"`
	}{
		MinYear:  minDate.Year(),
		MinMonth: int(minDate.Month()),
		MaxYear:  maxDate.Year(),
		MaxMonth: int(maxDate.Month()),
	}, m.GetIndentCtx(r))
}

func (c *appContext) getReportDetail(w http.ResponseWriter, r *http.Request) {
	detailType := r.URL.Query().Get("type")
	var timeStr string
	var token string
	var name string
	if detailType == "month" || detailType == "year" {
		timeStr = r.URL.Query().Get("time")
		c.HandlerDetailReportByMonthYear(w, r, detailType, timeStr)
	} else if detailType == "proposal" {
		token = r.URL.Query().Get("token")
		c.HandlerDetailReportByProposal(w, r, token)
	} else if detailType == "domain" {
		name = r.URL.Query().Get("name")
		c.HandlerDetailReportByDomain(w, r, name)
	} else if detailType == "owner" {
		name = r.URL.Query().Get("name")
		c.HandlerDetailReportByOwner(w, r, name)
	}
}

func (c *appContext) HandlerDetailReportByProposal(w http.ResponseWriter, r *http.Request, token string) {
	//get proposal meta data by token
	proposalMeta, err := c.DataSource.GetProposalByToken(token)
	if err != nil {
		log.Errorf("Get proposals by token failed: %v", err)
		writeJSON(w, nil, m.GetIndentCtx(r))
		return
	}
	proposalTokens := c.DataSource.GetAllProposalTokens()
	now := time.Now()
	proposalInfo := apitypes.ProposalReportData{}
	var amount = proposalMeta["Amount"]
	var tokenRst = proposalMeta["Token"]
	var name = proposalMeta["Name"]
	var author = proposalMeta["Username"]
	var startDate = proposalMeta["StartDate"]
	var endDate = proposalMeta["EndDate"]
	var domain = proposalMeta["Domain"]
	startInt, err := strconv.ParseInt(startDate, 0, 32)
	endInt, err2 := strconv.ParseInt(endDate, 0, 32)
	amountFloat, err3 := strconv.ParseFloat(amount, 64)
	if amountFloat == 0.0 || err != nil || err2 != nil || err3 != nil {
		writeJSON(w, nil, m.GetIndentCtx(r))
		return
	}
	amountFloat = amountFloat / 100
	startTime := time.Unix(startInt, 0)
	endTime := time.Unix(endInt, 0)
	if startTime.After(now) || (startTime.Month() == now.Month() && startTime.Year() == now.Year()) {
		log.Errorf("There are no payments because the first payment for the project has not yet arrived")
		writeJSON(w, nil, m.GetIndentCtx(r))
		return
	}
	proposalInfo.Name = name
	proposalInfo.Author = author
	proposalInfo.Token = tokenRst
	proposalInfo.Budget = amountFloat
	proposalInfo.Start = startTime.Format("2006-01-02")
	proposalInfo.End = endTime.Format("2006-01-02")
	proposalInfo.Domain = domain
	totalSpent := 0.0
	//count month from startTime to endTime
	difference := endTime.Sub(startTime)
	var countMonths = 12*(endTime.Year()-startTime.Year()) + (int(endTime.Month()) - int(startTime.Month())) + 1
	//count day from startTime to endTime
	countDays := int16(difference.Hours()/24) + 1
	costPerDay := amountFloat / float64(countDays)
	monthDatas := make([]apitypes.MonthDataObject, 0)
	tempTime := time.Unix(startInt, 0)
	monthlyPriceMap := c.DataSource.GetCurrencyPriceMapByPeriod(startTime, endTime, false)
	//if start month and end month are the same, month data is proposal data
	if startTime.Month() == endTime.Month() && startTime.Year() == endTime.Year() {
		key := fmt.Sprintf("%d-%s", startTime.Year(), apitypes.GetFullMonthDisplay(int(startTime.Month())))
		usdPrice, exist := monthlyPriceMap[key]
		expenseDcr := int64(0)
		if exist {
			expenseDcr = int64(1e8 * amountFloat / usdPrice)
		}
		itemData := apitypes.MonthDataObject{
			Month:      key,
			Expense:    amountFloat,
			ExpenseDcr: expenseDcr,
		}
		totalSpent += amountFloat
		monthDatas = append(monthDatas, itemData)
	} else {
		//calculate cost every month
		for i := 0; i < int(countMonths); i++ {
			handlerTime := tempTime.AddDate(0, i, 0)
			key := fmt.Sprintf("%d-%s", handlerTime.Year(), apitypes.GetFullMonthDisplay(int(handlerTime.Month())))
			var costOfMonth float64
			//if start month
			if i == 0 {
				//get end day of month
				endDay := startTime.AddDate(0, 1, -startTime.Day())
				countToEndMonth := endDay.Day() - startTime.Day() + 1
				costOfMonth = float64(countToEndMonth) * costPerDay
			} else if i == int(countMonths)-1 {
				//get start day of month
				startDay := endTime.AddDate(0, 0, -endTime.Day()+1)
				countFromStartMonth := endTime.Day() - startDay.Day() + 1
				costOfMonth = float64(countFromStartMonth) * costPerDay
				//if other
			} else {
				startDay := handlerTime.AddDate(0, 0, -handlerTime.Day()+1)
				endDay := handlerTime.AddDate(0, 1, -handlerTime.Day())
				countDaysOfMonth := endDay.Day() - startDay.Day() + 1
				costOfMonth = float64(countDaysOfMonth) * costPerDay
			}
			costOfMonth = math.Ceil(costOfMonth*100) / 100
			usdPrice, exist := monthlyPriceMap[key]
			expenseDcr := int64(0)
			if exist {
				expenseDcr = int64(1e8 * costOfMonth / usdPrice)
			}
			itemData := apitypes.MonthDataObject{
				Month:      key,
				Expense:    costOfMonth,
				ExpenseDcr: expenseDcr,
			}
			isAfter := handlerTime.After(now) || (handlerTime.Month() == now.Month() && handlerTime.Year() == now.Year())
			if !isAfter {
				totalSpent += costOfMonth
			}
			monthDatas = append(monthDatas, itemData)
		}
	}

	if now.After(endTime) {
		totalSpent = amountFloat
	}

	proposalInfo.TotalSpent = math.Ceil(totalSpent*100) / 100
	proposalInfo.TotalRemaining = math.Ceil((amountFloat-totalSpent)*100) / 100

	//Get other proposal with the same owner
	proposalMetaList, err := c.DataSource.GetProposalByOwner(author)
	proposalSummaryList := make([]apitypes.ProposalReportData, 0)
	if err == nil {
		handlerProposals := make([]map[string]string, 0)
		for _, proposalMetaData := range proposalMetaList {
			if proposalMetaData["Token"] != token {
				handlerProposals = append(handlerProposals, proposalMetaData)
			}
		}
		//remove this owner from proposalMetaList
		proposalSummaryList, _ = c.GetReportDataFromProposalList(handlerProposals, false)
	}

	writeJSON(w, struct {
		ProposalInfo       apitypes.ProposalReportData   `json:"proposalInfo"`
		MonthData          []apitypes.MonthDataObject    `json:"monthData"`
		TokenList          []string                      `json:"tokenList"`
		OtherProposalInfos []apitypes.ProposalReportData `json:"otherProposalInfos"`
	}{
		ProposalInfo:       proposalInfo,
		MonthData:          monthDatas,
		TokenList:          proposalTokens,
		OtherProposalInfos: proposalSummaryList,
	}, m.GetIndentCtx(r))
}

func (c *appContext) GetReportDataFromProposalList(proposals []map[string]string, containAllTime bool) ([]apitypes.ProposalReportData, []apitypes.MonthDataObject) {
	now := time.Now()
	proposalSummaryList := make([]apitypes.ProposalReportData, 0)
	monthDatas := make([]apitypes.MonthDataObject, 0)
	monthExpenseMap := make(map[string]float64)
	monthTotalBudgetMap := make(map[string]float64)
	monthWeightMap := make(map[string]int)
	var minTime, maxTime time.Time
	var settedMinTime, settedMaxTime bool
	for _, proposalMeta := range proposals {
		proposalInfo := apitypes.ProposalReportData{}
		var amount = proposalMeta["Amount"]
		var tokenRst = proposalMeta["Token"]
		var name = proposalMeta["Name"]
		var author = proposalMeta["Username"]
		var startDate = proposalMeta["StartDate"]
		var endDate = proposalMeta["EndDate"]
		var domain = proposalMeta["Domain"]
		startInt, err := strconv.ParseInt(startDate, 0, 32)
		endInt, err2 := strconv.ParseInt(endDate, 0, 32)
		amountFloat, err3 := strconv.ParseFloat(amount, 64)
		if amountFloat == 0.0 || err != nil || err2 != nil || err3 != nil {
			continue
		}
		amountFloat = amountFloat / 100
		startTime := time.Unix(startInt, 0)
		endTime := time.Unix(endInt, 0)
		if !settedMinTime || startTime.Before(minTime) {
			minTime = startTime
			settedMinTime = true
		}
		if !settedMaxTime || endTime.After(maxTime) {
			maxTime = endTime
			settedMaxTime = true
		}
		if !containAllTime && (startTime.After(now) || (startTime.Month() == now.Month() && startTime.Year() == now.Year())) {
			continue
		}
		proposalInfo.Name = name
		proposalInfo.Author = author
		proposalInfo.Token = tokenRst
		proposalInfo.Budget = amountFloat
		proposalInfo.Start = startTime.Format("2006-01-02")
		proposalInfo.End = endTime.Format("2006-01-02")
		proposalInfo.Domain = domain
		totalSpent := 0.0
		//count month from startTime to endTime
		difference := endTime.Sub(startTime)
		var countMonths = 12*(endTime.Year()-startTime.Year()) + (int(endTime.Month()) - int(startTime.Month())) + 1
		//count day from startTime to endTime
		countDays := int16(difference.Hours()/24) + 1
		costPerDay := amountFloat / float64(countDays)
		tempTime := time.Unix(startInt, 0)
		//if start month and end month are the same, month data is proposal data
		if startTime.Month() == endTime.Month() && startTime.Year() == endTime.Year() {
			key := fmt.Sprintf("%d-%s", startTime.Year(), apitypes.GetFullMonthDisplay(int(startTime.Month())))
			totalSpent += amountFloat
			if _, ok := monthWeightMap[key]; !ok {
				monthWeightMap[key] = startTime.Year()*12 + int(startTime.Month())
			}
			value, ok := monthExpenseMap[key]
			if ok {
				monthExpenseMap[key] = value + amountFloat
			} else {
				monthExpenseMap[key] = amountFloat
			}
			totalBudget, monthTotalExist := monthTotalBudgetMap[key]
			if monthTotalExist {
				monthTotalBudgetMap[key] = totalBudget + proposalInfo.Budget
			} else {
				monthTotalBudgetMap[key] = proposalInfo.Budget
			}
		} else {
			//calculate cost every month
			for i := 0; i < int(countMonths); i++ {
				handlerTime := tempTime.AddDate(0, i, 0)
				//if month is this month or future months, break loop
				if !containAllTime && (handlerTime.After(now) || (handlerTime.Month() == now.Month() && handlerTime.Year() == now.Year())) {
					break
				}
				key := fmt.Sprintf("%d-%s", handlerTime.Year(), apitypes.GetFullMonthDisplay(int(handlerTime.Month())))
				var costOfMonth float64
				//if start month
				if i == 0 {
					//get end day of month
					endDay := startTime.AddDate(0, 1, -startTime.Day())
					countToEndMonth := endDay.Day() - startTime.Day() + 1
					costOfMonth = float64(countToEndMonth) * costPerDay
				} else if i == int(countMonths)-1 {
					//get start day of month
					startDay := endTime.AddDate(0, 0, -endTime.Day()+1)
					countFromStartMonth := endTime.Day() - startDay.Day() + 1
					costOfMonth = float64(countFromStartMonth) * costPerDay
					//if other
				} else {
					startDay := handlerTime.AddDate(0, 0, -handlerTime.Day()+1)
					endDay := handlerTime.AddDate(0, 1, -handlerTime.Day())
					countDaysOfMonth := endDay.Day() - startDay.Day() + 1
					costOfMonth = float64(countDaysOfMonth) * costPerDay
				}
				costOfMonth = math.Ceil(costOfMonth*100) / 100
				isAfter := handlerTime.After(now) || (handlerTime.Month() == now.Month() && handlerTime.Year() == now.Year())
				if !isAfter {
					totalSpent += costOfMonth
				}
				if _, ok := monthWeightMap[key]; !ok {
					monthWeightMap[key] = handlerTime.Year()*12 + int(handlerTime.Month())
				}
				value, ok := monthExpenseMap[key]
				if ok {
					monthExpenseMap[key] = value + costOfMonth
				} else {
					monthExpenseMap[key] = costOfMonth
				}
				totalBudget, monthTotalExist := monthTotalBudgetMap[key]
				if monthTotalExist {
					monthTotalBudgetMap[key] = totalBudget + proposalInfo.Budget
				} else {
					monthTotalBudgetMap[key] = proposalInfo.Budget
				}
			}
		}
		if now.After(endTime) {
			totalSpent = amountFloat
		}
		proposalInfo.TotalSpent = math.Ceil(totalSpent*100) / 100
		proposalInfo.TotalRemaining = math.Ceil((amountFloat-totalSpent)*100) / 100
		proposalSummaryList = append(proposalSummaryList, proposalInfo)
	}

	monthStringArr := make([]string, 0)
	monthValueArr := make([]int, 0)
	for key, value := range monthWeightMap {
		monthStringArr = append(monthStringArr, key)
		monthValueArr = append(monthValueArr, value)
	}

	//sort by month value
	for i := 0; i < len(monthValueArr); i++ {
		for j := i + 1; j < len(monthValueArr); j++ {
			if monthValueArr[j] < monthValueArr[i] {
				var tmpKey = monthStringArr[i]
				var tmpValue = monthValueArr[i]
				monthStringArr[i] = monthStringArr[j]
				monthValueArr[i] = monthValueArr[j]
				monthStringArr[j] = tmpKey
				monthValueArr[j] = tmpValue
			}
		}
	}
	monthlyPriceMap := c.DataSource.GetCurrencyPriceMapByPeriod(minTime, maxTime, false)
	for _, key := range monthStringArr {
		v, ok := monthExpenseMap[key]
		if !ok {
			continue
		}
		usdPrice, exist := monthlyPriceMap[key]
		expenseDcr := int64(0)
		if exist {
			expenseDcr = int64(1e8 * v / usdPrice)
		}
		itemData := apitypes.MonthDataObject{
			Month:       key,
			Expense:     v,
			ExpenseDcr:  expenseDcr,
			TotalBudget: monthTotalBudgetMap[key],
		}
		monthDatas = append(monthDatas, itemData)
	}
	return proposalSummaryList, monthDatas
}

func (c *appContext) MainHandlerForReportByParam(w http.ResponseWriter, r *http.Request, proposals []map[string]string, paramType string) {
	var allParamList []string
	if paramType == "domain" {
		allParamList = c.DataSource.GetAllProposalDomains()
	} else if paramType == "owner" {
		allParamList = c.DataSource.GetAllProposalOwners()
	}

	proposalSummaryList, monthDatas := c.GetReportDataFromProposalList(proposals, true)

	if paramType == "domain" {
		writeJSON(w, struct {
			ProposalInfos []apitypes.ProposalReportData `json:"proposalInfos"`
			MonthData     []apitypes.MonthDataObject    `json:"monthData"`
			DomainList    []string                      `json:"domainList"`
		}{
			ProposalInfos: proposalSummaryList,
			MonthData:     monthDatas,
			DomainList:    allParamList,
		}, m.GetIndentCtx(r))
	} else if paramType == "owner" {
		writeJSON(w, struct {
			ProposalInfos []apitypes.ProposalReportData `json:"proposalInfos"`
			MonthData     []apitypes.MonthDataObject    `json:"monthData"`
			OwnerList     []string                      `json:"ownerList"`
		}{
			ProposalInfos: proposalSummaryList,
			MonthData:     monthDatas,
			OwnerList:     allParamList,
		}, m.GetIndentCtx(r))
	}
}

func (c *appContext) HandlerDetailReportByOwner(w http.ResponseWriter, r *http.Request, name string) {
	//get proposal meta data by owner name
	proposalMetaList, err := c.DataSource.GetProposalByOwner(name)
	if err != nil {
		log.Errorf("Get proposals by owner failed: %v", err)
		writeJSON(w, nil, m.GetIndentCtx(r))
		return
	}
	c.MainHandlerForReportByParam(w, r, proposalMetaList, "owner")
}

func (c *appContext) HandlerDetailReportByDomain(w http.ResponseWriter, r *http.Request, domain string) {
	//get proposal meta data by domain
	proposalMetaList, err := c.DataSource.GetProposalByDomain(domain)
	if err != nil {
		log.Errorf("Get proposals by domain failed: %v", err)
		writeJSON(w, nil, m.GetIndentCtx(r))
		return
	}
	c.MainHandlerForReportByParam(w, r, proposalMetaList, "domain")
}

func (c *appContext) HandlerDetailReportByMonthYear(w http.ResponseWriter, r *http.Request, timeType string, timeStr string) {
	report := make([]apitypes.ProposalReportData, 0)
	domainList := make([]string, 0)
	proposalList := make([]string, 0)
	total := float64(0)

	var treasurySummary dbtypes.TreasurySummary
	var legacySummary dbtypes.TreasurySummary
	monthResultData := make([]apitypes.MonthDataObject, 0)
	monthPrice := float64(0)
	now := time.Now()
	if timeType == "month" {
		timeArr := strings.Split(timeStr, "_")
		year, yearErr := strconv.ParseInt(timeArr[0], 0, 32)
		month, monthErr := strconv.ParseInt(timeArr[1], 0, 32)
		if len(timeArr) != 2 || yearErr != nil || monthErr != nil {
			writeJSON(w, nil, m.GetIndentCtx(r))
			return
		}
		proposalMetaList, proposalErr := c.DataSource.GetProposalMetaByMonth(int(year), int(month))
		treasuryData, treasuryErr := c.DataSource.GetTreasurySummaryByMonth(int(year), int(month))
		legacyData, legacyErr := c.DataSource.GetLegacySummaryByMonth(int(year), int(month))
		if proposalErr != nil || treasuryErr != nil || legacyErr != nil {
			writeJSON(w, nil, m.GetIndentCtx(r))
			return
		}
		treasurySummary = *treasuryData
		legacySummary = *legacyData
		monthPrice, _ = c.DataSource.GetMonthlyPrice(int(year), int(month))
		for _, proposalMeta := range proposalMetaList {
			var amount = proposalMeta["Amount"]
			var token = proposalMeta["Token"]
			var name = proposalMeta["Name"]
			var author = proposalMeta["Username"]
			var startDate = proposalMeta["StartDate"]
			var endDate = proposalMeta["EndDate"]
			var domain = proposalMeta["Domain"]
			//parse date time of startDate and endDate
			startInt, err := strconv.ParseInt(startDate, 0, 32)
			endInt, err2 := strconv.ParseInt(endDate, 0, 32)
			amountFloat, err3 := strconv.ParseFloat(amount, 64)
			if amountFloat == 0.0 || err != nil || err2 != nil || err3 != nil {
				continue
			}
			amountFloat = amountFloat / 100
			startTime := time.Unix(startInt, 0)
			//If the proposal's starting month is after this month (including this month), then ignore it and not include it in the report
			// if startTime.After(now) || (startTime.Month() == now.Month() && startTime.Year() == now.Year()) {
			// 	continue
			// }

			if !slices.Contains(proposalList, name) {
				proposalList = append(proposalList, name)
			}
			if !slices.Contains(domainList, domain) {
				domainList = append(domainList, domain)
			}
			endTime := time.Unix(endInt, 0)
			//count month from startTime to endTime
			difference := endTime.Sub(startTime)
			//count day from startTime to endTime
			countDays := int16(difference.Hours()/24) + 1
			costPerDay := amountFloat / float64(countDays)

			//if start month and end month are the same, month data is proposal data
			varMonthData := apitypes.ProposalReportData{}
			varMonthData.Budget = amountFloat
			varMonthData.Token = token
			varMonthData.Author = author
			varMonthData.Name = name
			varMonthData.Domain = domain
			varMonthData.Start = startTime.Format("2006-01-02")
			varMonthData.End = endTime.Format("2006-01-02")
			varMonthData.TotalSpent = 0.0
			if startTime.Month() == endTime.Month() && startTime.Year() == endTime.Year() {
				varMonthData.TotalSpent = amountFloat
			} else {
				var costOfMonth float64
				if startTime.Year() == int(year) && int(startTime.Month()) == int(month) {
					endDay := startTime.AddDate(0, 1, -startTime.Day())
					countToEndMonth := endDay.Day() - startTime.Day() + 1
					costOfMonth = float64(countToEndMonth) * costPerDay
				} else if endTime.Year() == int(year) && int(endTime.Month()) == int(month) {
					startDay := endTime.AddDate(0, 0, -endTime.Day()+1)
					countFromStartMonth := endTime.Day() - startDay.Day() + 1
					costOfMonth = float64(countFromStartMonth) * costPerDay
				} else {
					startDay := time.Date(int(year), time.Month(month), 1, 0, 0, 0, 0, time.Local)
					endDay := startDay.AddDate(0, 1, -startDay.Day())
					countDaysOfMonth := endDay.Day() - startDay.Day() + 1
					costOfMonth = float64(countDaysOfMonth) * costPerDay
				}
				varMonthData.TotalSpent = costOfMonth
			}
			if now.Year()*12+int(now.Month()) >= int(year*12+month) {
				varMonthData.SpentEst = varMonthData.TotalSpent
				if monthPrice > 0 {
					varMonthData.TotalSpentDcr = varMonthData.TotalSpent / monthPrice
				}
			}
			total += varMonthData.TotalSpent
			report = append(report, varMonthData)
		}
	}
	yearList := make([]int, 0)
	if timeType == "year" {
		year, yearErr := strconv.ParseInt(timeStr, 0, 32)
		if yearErr != nil {
			writeJSON(w, nil, m.GetIndentCtx(r))
			return
		}
		var proposalMetaList []map[string]string
		var treasuryGroupByMonth []dbtypes.TreasurySummary
		var legacyGroupByMonth []dbtypes.TreasurySummary
		var proposalErr error
		// if year = 0, get summary all data
		if year == 0 {
			proposalMetaList, proposalErr = c.DataSource.GetAllProposalMeta("")
			treasuryGroupByMonth, _ = c.DataSource.GetTreasurySummaryAllYear()
			legacyGroupByMonth, _ = c.DataSource.GetLegacySummaryAllYear()
		} else {
			proposalMetaList, proposalErr = c.DataSource.GetProposalMetaByYear(int(year))
			treasuryGroupByMonth, _ = c.DataSource.GetTreasurySummaryGroupByMonth(int(year))
			legacyGroupByMonth, _ = c.DataSource.GetLegacySummaryGroupByMonth(int(year))
		}
		if proposalErr != nil {
			writeJSON(w, nil, m.GetIndentCtx(r))
			return
		}
		combinedMap := make(map[string]dbtypes.TreasurySummary)
		treasurySummary = dbtypes.TreasurySummary{}
		legacySummary = dbtypes.TreasurySummary{}
		for _, treasuryItem := range treasuryGroupByMonth {
			treasuryItem.OriginalInvalue = treasuryItem.Invalue
			treasuryItem.OriginalInvalueUSD = treasuryItem.InvalueUSD
			treasuryItem.Invalue -= treasuryItem.TaddValue
			treasuryItem.InvalueUSD -= treasuryItem.TaddValueUSD
			treasurySummary.Invalue += treasuryItem.OriginalInvalue
			treasurySummary.InvalueUSD += treasuryItem.OriginalInvalueUSD
			treasurySummary.Outvalue += treasuryItem.Outvalue
			treasurySummary.OutvalueUSD += treasuryItem.OutvalueUSD
			treasurySummary.Difference += treasuryItem.Difference
			treasurySummary.DifferenceUSD += treasuryItem.DifferenceUSD
			treasurySummary.Total += treasuryItem.Total
			treasurySummary.TotalUSD += treasuryItem.TotalUSD
			treasurySummary.TaddValue += treasuryItem.TaddValue
			treasurySummary.TaddValueUSD += treasuryItem.TaddValueUSD
			combinedMap[treasuryItem.Month] = treasuryItem
		}
		for _, legacyItem := range legacyGroupByMonth {
			tSummary, exist := combinedMap[legacyItem.Month]
			legacyItem.OriginalOutvalue = legacyItem.Outvalue
			legacyItem.OriginalOutvalueUSD = legacyItem.OutvalueUSD
			if exist {
				legacyItem.Outvalue -= tSummary.TaddValue
				legacyItem.OutvalueUSD -= tSummary.TaddValueUSD
				tSummary.Invalue += legacyItem.Invalue
				tSummary.InvalueUSD += legacyItem.InvalueUSD
				tSummary.Outvalue += legacyItem.Outvalue
				tSummary.OutvalueUSD += legacyItem.OutvalueUSD
				tSummary.Difference = int64(math.Abs(float64(tSummary.Invalue - tSummary.Outvalue)))
				tSummary.DifferenceUSD = math.Abs(tSummary.InvalueUSD - tSummary.OutvalueUSD)
				tSummary.Total = int64(math.Abs(float64(tSummary.Invalue + tSummary.Outvalue)))
				tSummary.TotalUSD = math.Abs(tSummary.InvalueUSD + tSummary.OutvalueUSD)
				combinedMap[legacyItem.Month] = tSummary
			} else {
				combinedMap[legacyItem.Month] = legacyItem
			}
			legacySummary.Invalue += legacyItem.Invalue
			legacySummary.InvalueUSD += legacyItem.InvalueUSD
			legacySummary.Outvalue += legacyItem.OriginalOutvalue
			legacySummary.OutvalueUSD += legacyItem.OriginalOutvalueUSD
			legacySummary.Difference += legacyItem.Difference
			legacySummary.DifferenceUSD += legacyItem.DifferenceUSD
			legacySummary.Total += legacyItem.Total
			legacySummary.TotalUSD += legacyItem.TotalUSD
		}
		//get month rate map of year
		var monthlyPriceMap map[string]float64
		var proposalMinTime time.Time
		var proposalMaxTime time.Time
		if year == 0 {
			proposalMinTime, proposalMaxTime, _ = c.GetTimeRangeFromProposalMetaList(proposalMetaList)
			monthlyPriceMap = c.DataSource.GetCurrencyPriceMapByPeriod(proposalMinTime, proposalMaxTime, false)
		} else {
			startDayOfYear := time.Date(int(year), time.January, 1, 0, 0, 0, 0, time.Local)
			lastDateOfYear := time.Date(int(year)+1, 1, 0, 0, 0, 0, 0, time.Local)
			monthlyPriceMap = c.DataSource.GetCurrencyPriceMapByPeriod(startDayOfYear, lastDateOfYear, false)
		}
		monthWeightMap := make(map[string]int)
		monthExpenseMap := make(map[string]float64)
		for _, proposalMeta := range proposalMetaList {
			var amount = proposalMeta["Amount"]
			var token = proposalMeta["Token"]
			var author = proposalMeta["Username"]
			var name = proposalMeta["Name"]
			var startDate = proposalMeta["StartDate"]
			var endDate = proposalMeta["EndDate"]
			var domain = proposalMeta["Domain"]
			//parse date time of startDate and endDate
			startInt, err := strconv.ParseInt(startDate, 0, 32)
			endInt, err2 := strconv.ParseInt(endDate, 0, 32)
			amountFloat, err3 := strconv.ParseFloat(amount, 64)
			if amountFloat == 0.0 || err != nil || err2 != nil || err3 != nil {
				continue
			}
			amountFloat = amountFloat / 100
			startTime := time.Unix(startInt, 0)

			if !slices.Contains(proposalList, name) {
				proposalList = append(proposalList, name)
			}
			if !slices.Contains(domainList, domain) {
				domainList = append(domainList, domain)
			}
			endTime := time.Unix(endInt, 0)
			//count month from startTime to endTime
			difference := endTime.Sub(startTime)
			//count day from startTime to endTime
			countDays := int16(difference.Hours()/24) + 1
			costPerDay := amountFloat / float64(countDays)

			//if start month and end month are the same, month data is proposal data
			varYearData := apitypes.ProposalReportData{}
			varYearData.Token = token
			varYearData.Author = author
			varYearData.Name = name
			varYearData.Domain = domain
			varYearData.Start = startTime.Format("2006-01-02")
			varYearData.End = endTime.Format("2006-01-02")
			varYearData.Budget = amountFloat
			//if current year
			costOfYear := float64(0)
			costSpentOfYear := float64(0)
			costOfYearDcr := float64(0)
			//count month from startTime to endTime
			countMonths := 12*(endTime.Year()-startTime.Year()) + (int(endTime.Month()) - int(startTime.Month())) + 1
			tempTime := time.Unix(startInt, 0)
			yearGroupMap := make(map[string]float64)
			yearGroupData := make([]apitypes.YearSpend, 0)
			if ((year != 0 && startTime.Year() == int(year)) || year == 0) && startTime.Month() == endTime.Month() && startTime.Year() == endTime.Year() {
				key := fmt.Sprintf("%d-%s", startTime.Year(), apitypes.GetFullMonthDisplay(int(startTime.Month())))
				costOfYear += amountFloat
				if now.Year()*12+int(now.Month()) >= startTime.Year()*12+int(startTime.Month()) {
					costSpentOfYear += amountFloat
					monthPrice, ok := monthlyPriceMap[key]
					if ok {
						costOfYearDcr += amountFloat / monthPrice
					}
				}
				if _, ok := monthWeightMap[key]; !ok {
					monthWeightMap[key] = startTime.Year()*12 + int(startTime.Month())
				}
				value, ok := monthExpenseMap[key]
				if ok {
					monthExpenseMap[key] = value + amountFloat
				} else {
					monthExpenseMap[key] = amountFloat
				}
				if year == 0 {
					yearStr := fmt.Sprintf("%d", startTime.Year())
					yearData, exist := yearGroupMap[yearStr]
					if exist {
						yearGroupMap[yearStr] = yearData + amountFloat
					} else {
						yearGroupMap[yearStr] = amountFloat
					}
				}
			} else {
				//calculate cost every month
				for i := 0; i < int(countMonths); i++ {
					handlerTime := tempTime.AddDate(0, i, 0)
					if year > 0 && handlerTime.Year() != int(year) {
						continue
					}
					key := fmt.Sprintf("%d-%s", handlerTime.Year(), apitypes.GetFullMonthDisplay(int(handlerTime.Month())))
					var costOfMonth float64
					//if start month
					if i == 0 {
						//get end day of month
						endDay := startTime.AddDate(0, 1, -startTime.Day())
						countToEndMonth := endDay.Day() - startTime.Day() + 1
						costOfMonth = float64(countToEndMonth) * costPerDay
					} else if i == int(countMonths)-1 {
						//get start day of month
						startDay := endTime.AddDate(0, 0, -endTime.Day()+1)
						countFromStartMonth := endTime.Day() - startDay.Day() + 1
						costOfMonth = float64(countFromStartMonth) * costPerDay
						//if other
					} else {
						startDay := handlerTime.AddDate(0, 0, -handlerTime.Day()+1)
						endDay := handlerTime.AddDate(0, 1, -handlerTime.Day())
						countDaysOfMonth := endDay.Day() - startDay.Day() + 1
						costOfMonth = float64(countDaysOfMonth) * costPerDay
					}
					costOfMonth = math.Ceil(costOfMonth*100) / 100
					costOfYear += costOfMonth
					if now.Year()*12+int(now.Month()) >= handlerTime.Year()*12+int(handlerTime.Month()) {
						costSpentOfYear += costOfMonth
						monthPrice, priceExist := monthlyPriceMap[key]
						if priceExist {
							costOfYearDcr += costOfMonth / monthPrice
						}
					}
					if _, ok := monthWeightMap[key]; !ok {
						monthWeightMap[key] = handlerTime.Year()*12 + int(handlerTime.Month())
					}
					value, ok := monthExpenseMap[key]
					if ok {
						monthExpenseMap[key] = value + costOfMonth
					} else {
						monthExpenseMap[key] = costOfMonth
					}
					if year == 0 {
						yearStr := fmt.Sprintf("%d", handlerTime.Year())
						yearData, exist := yearGroupMap[yearStr]
						if exist {
							yearGroupMap[yearStr] = yearData + costOfMonth
						} else {
							yearGroupMap[yearStr] = costOfMonth
						}
					}
				}
			}

			if year == 0 || (startTime.Year() == int(year) && endTime.Year() == int(year)) {
				costOfYear = amountFloat
			}

			varYearData.TotalSpent = math.Ceil(costOfYear*100) / 100
			varYearData.SpentEst = math.Ceil(costSpentOfYear*100) / 100
			varYearData.TotalSpentDcr = math.Ceil(costOfYearDcr*100) / 100
			if year == 0 {
				for tempYear := proposalMinTime.Year(); tempYear <= proposalMaxTime.Year(); tempYear++ {
					yearSpend, exist := yearGroupMap[fmt.Sprintf("%d", tempYear)]
					if !exist {
						yearSpend = 0
					}
					yearGroupData = append(yearGroupData, apitypes.YearSpend{
						Year:  fmt.Sprintf("%d", tempYear),
						Spend: yearSpend,
					})
				}
				varYearData.SpentYears = yearGroupData
			}
			total += costOfYear
			report = append(report, varYearData)
		}
		monthStringArr := make([]string, 0)
		monthValueArr := make([]int, 0)
		for key, value := range monthWeightMap {
			monthStringArr = append(monthStringArr, key)
			monthValueArr = append(monthValueArr, value)
		}
		if year == 0 {
			for tempYear := proposalMinTime.Year(); tempYear <= proposalMaxTime.Year(); tempYear++ {
				yearList = append(yearList, tempYear)
			}
		}

		//sort by month value
		for i := 0; i < len(monthValueArr); i++ {
			for j := i + 1; j < len(monthValueArr); j++ {
				if monthValueArr[j] < monthValueArr[i] {
					var tmpKey = monthStringArr[i]
					var tmpValue = monthValueArr[i]
					monthStringArr[i] = monthStringArr[j]
					monthValueArr[i] = monthValueArr[j]
					monthStringArr[j] = tmpKey
					monthValueArr[j] = tmpValue
				}
			}
		}
		monthDatas := make([]apitypes.MonthDataObject, 0)
		for _, key := range monthStringArr {
			v, ok := monthExpenseMap[key]
			if !ok {
				continue
			}
			itemData := apitypes.MonthDataObject{
				Month:   key,
				Expense: v,
			}
			monthDatas = append(monthDatas, itemData)
		}

		summaryDataObjList := make([]apitypes.MonthDataObject, 0)
		var timeTemp time.Time
		var maxTimeCompare int64
		var timeCompare int64
		if year == 0 {
			timeTemp = proposalMinTime
			maxTimeCompare = int64(proposalMaxTime.Year())*12 + int64(proposalMaxTime.Month())
			timeCompare = int64(timeTemp.Year())*12 + int64(timeTemp.Month())
		} else {
			timeTemp = time.Date(int(year), time.January, 1, 0, 0, 0, 0, time.Local)
		}
		for (year == 0 && timeCompare <= maxTimeCompare) || (year != 0 && timeTemp.Year() == int(year)) {
			monthStr := timeTemp.Format("2006-01")
			expense := c.getExpenseFromList(monthDatas, monthStr)
			treasuryCombined, exist := combinedMap[monthStr]
			actualExpense := float64(0)
			if exist {
				actualExpense = treasuryCombined.OutvalueUSD
			}
			if expense == 0 && actualExpense == 0 {
				timeTemp = timeTemp.AddDate(0, 1, 0)
				if year == 0 {
					timeCompare = int64(timeTemp.Year())*12 + int64(timeTemp.Month())
				}
				continue
			}
			monthPrice, existPrice := monthlyPriceMap[monthStr]
			expenseDcr := int64(0)
			actualDcr := int64(0)
			if existPrice {
				expenseDcr = int64(1e8 * expense / monthPrice)
				actualDcr = int64(1e8 * actualExpense / monthPrice)
			}
			dataObj := apitypes.MonthDataObject{
				Month:            monthStr,
				Expense:          expense,
				ExpenseDcr:       expenseDcr,
				ActualExpense:    actualExpense,
				ActualExpenseDcr: actualDcr,
			}
			summaryDataObjList = append(summaryDataObjList, dataObj)
			timeTemp = timeTemp.AddDate(0, 1, 0)
			if year == 0 {
				timeCompare = int64(timeTemp.Year())*12 + int64(timeTemp.Month())
			}
		}
		monthResultData = summaryDataObjList
	}

	writeJSON(w, struct {
		ReportDetail      []apitypes.ProposalReportData `json:"reportDetail"`
		ProposalList      []string                      `json:"proposalList"`
		DomainList        []string                      `json:"domainList"`
		ProposalTotal     float64                       `json:"proposalTotal"`
		TreasurySummary   dbtypes.TreasurySummary       `json:"treasurySummary"`
		LegacySummary     dbtypes.TreasurySummary       `json:"legacySummary"`
		MonthlyResultData []apitypes.MonthDataObject    `json:"monthlyResultData"`
		YearList          []int                         `json:"yearList"`
	}{
		ReportDetail:      report,
		ProposalList:      proposalList,
		DomainList:        domainList,
		ProposalTotal:     total,
		TreasurySummary:   treasurySummary,
		LegacySummary:     legacySummary,
		MonthlyResultData: monthResultData,
		YearList:          yearList,
	}, m.GetIndentCtx(r))
}

func (c *appContext) GetTimeRangeFromProposalMetaList(proposalMetaList []map[string]string) (time.Time, time.Time, error) {
	var minTime time.Time
	var maxTime time.Time
	for _, proposalMeta := range proposalMetaList {
		var amount = proposalMeta["Amount"]
		amountFloat, err3 := strconv.ParseFloat(amount, 64)
		if err3 != nil || amountFloat == 0.0 {
			continue
		}
		var startDate = proposalMeta["StartDate"]
		var endDate = proposalMeta["EndDate"]
		startInt, err := strconv.ParseInt(startDate, 0, 32)
		endInt, err2 := strconv.ParseInt(endDate, 0, 32)
		if err != nil || err2 != nil {
			continue
		}
		startTime := time.Unix(startInt, 0)
		endTime := time.Unix(endInt, 0)
		if minTime.IsZero() || startTime.Before(minTime) {
			minTime = startTime
		}
		if maxTime.IsZero() || endTime.After(maxTime) {
			maxTime = endTime
		}
	}
	return minTime, maxTime, nil
}

func (c *appContext) getActualExpenseFromList(list []dbtypes.TreasurySummary, month string) float64 {
	for _, item := range list {
		if item.Month == month {
			return item.OutvalueUSD
		}
	}
	return 0
}

func (c *appContext) getExpenseFromList(list []apitypes.MonthDataObject, month string) float64 {
	for _, item := range list {
		if item.Month == month {
			return item.Expense
		}
	}
	return 0
}

func (c *appContext) getTreasuryReport(w http.ResponseWriter, r *http.Request) {
	treasurySummary, err := c.DataSource.GetTreasurySummary()
	legacySummary, legacyErr := c.DataSource.GetLegacySummary()

	if err != nil || legacyErr != nil {
		log.Errorf("Get Treasury/Legacy Summary data failed")
	}

	//calculate summary by proposal to display estimate outgoing
	//Get All Proposal Metadata for Report
	proposalMetaList, err := c.DataSource.GetAllProposalMeta("")
	if err == nil {
		monthDataMap := make(map[string]float64)
		totalBudgetMap := make(map[string]float64)
		_, monthDatas := c.GetReportDataFromProposalList(proposalMetaList, true)
		for _, monthData := range monthDatas {
			monthDataMap[monthData.Month] = monthData.Expense
			totalBudgetMap[monthData.Month] = monthData.TotalBudget
		}
		//merge with summary report data
		for _, treasuryItem := range treasurySummary {
			monthProposalData, exist := monthDataMap[treasuryItem.Month]
			monthTotalBudget, tbExist := totalBudgetMap[treasuryItem.Month]
			if exist {
				if tbExist {
					treasuryItem.DevSpentPercent = 100 * (monthProposalData / monthTotalBudget)
				}
				treasuryItem.OutEstimateUsd = monthProposalData
				if treasuryItem.MonthPrice <= 0 {
					treasuryItem.OutEstimate = 0.0
				} else {
					treasuryItem.OutEstimate = monthProposalData / treasuryItem.MonthPrice
				}
			} else {
				treasuryItem.OutEstimate = 0.0
				treasuryItem.OutEstimateUsd = 0.0
			}
		}
	}

	//Get coin supply value
	writeJSON(w, struct {
		TreasurySummary []*dbtypes.TreasurySummary `json:"treasurySummary"`
		LegacySummary   []*dbtypes.TreasurySummary `json:"legacySummary"`
	}{
		TreasurySummary: treasurySummary,
		LegacySummary:   legacySummary,
	}, m.GetIndentCtx(r))
}

func (c *appContext) getBlocksReward(w http.ResponseWriter, r *http.Request) {
	blockList := r.URL.Query().Get("list")
	if blockList == "" {
		http.Error(w, http.StatusText(http.StatusInternalServerError),
			http.StatusInternalServerError)
		return
	}
	blocksArr := strings.Split(blockList, ",")
	resMap := make(map[int64]float64)
	for _, blockStr := range blocksArr {
		if blockStr == "" {
			continue
		}
		//parse int block
		block, parseErr := strconv.ParseInt(blockStr, 0, 32)
		if parseErr != nil {
			continue
		}
		subsidy, _ := c.nodeClient.GetBlockSubsidy(context.TODO(), block, 1)
		resMap[block] = dcrutil.Amount(subsidy.PoS).ToCoin()
	}
	writeJSON(w, struct {
		RewardMap map[int64]float64 `json:"rewardMap"`
	}{
		RewardMap: resMap,
	}, m.GetIndentCtx(r))
}

func (c *appContext) getDecodedTx(w http.ResponseWriter, r *http.Request) {
	// Look up any spending transactions for each output of this transaction
	// when the client requests spends with the URL query ?spends=true.
	var withSpends bool
	if spendParam := r.URL.Query().Get("spends"); spendParam != "" {
		b, err := strconv.ParseBool(spendParam)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}

		withSpends = b
	}

	txid, err := m.GetTxIDCtx(r)
	if err != nil {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	tx := c.DataSource.GetTrimmedTransaction(txid)
	if tx == nil {
		apiLog.Errorf("Unable to get transaction %s", txid)
		http.Error(w, http.StatusText(422), 422)
		return
	}

	if withSpends {
		if err := c.setTrimmedTxSpends(tx); err != nil {
			apiLog.Errorf("Unable to get spending transaction info for outputs of %s: %v", txid, err)
			http.Error(w, http.StatusText(http.StatusInternalServerError),
				http.StatusInternalServerError)
			return
		}
	}

	writeJSON(w, tx, m.GetIndentCtx(r))
}

// getTxSwapsInfo checks the inputs and outputs of the specified transaction for
// information about completed atomic swaps that were created and/or redeemed in
// the transaction.
func (c *appContext) getTxSwapsInfo(w http.ResponseWriter, r *http.Request) {
	txHash, err := m.GetTxIDCtx(r)
	if err != nil {
		http.Error(w, http.StatusText(422), 422)
		return
	}
	txid := txHash.String()

	tx, err := c.nodeClient.GetRawTransaction(r.Context(), txHash)
	if err != nil {
		apiLog.Errorf("Unable to get transaction %s: %v", txHash, err)
		http.Error(w, http.StatusText(422), 422)
		return
	}
	msgTx := tx.MsgTx()

	// Check if tx is a stake tree tx or coinbase tx and return empty swap info.
	if txhelpers.IsStakeTx(msgTx) || txhelpers.IsCoinBaseTx(msgTx) {
		noSwaps := &txhelpers.TxAtomicSwaps{
			TxID:  txHash.String(),
			Found: "No created or redeemed swaps in tx",
		}
		writeJSON(w, noSwaps, m.GetIndentCtx(r))
		return
	}

	// Fetch spending info for this tx if there is at least 1 p2sh output.
	// P2SH outputs may be contracts and the spending input sig is required
	// to know for sure.
	var maybeHasContracts bool
	for _, vout := range msgTx.TxOut {
		if stdscript.IsScriptHashScript(vout.Version, vout.PkScript) {
			maybeHasContracts = true
			break
		}
	}

	outputSpenders := make(map[uint32]*txhelpers.OutputSpenderTxOut)
	if maybeHasContracts {
		spendingTxHashes, spendingTxVinInds, voutInds, err := c.DataSource.SpendingTransactions(txid)
		if err != nil {
			apiLog.Errorf("Unable to retrieve spending transactions for %s: %v", txid, err)
			http.Error(w, http.StatusText(422), 422)
			return
		}
		for i, voutIndex := range voutInds {
			if int(voutIndex) >= len(msgTx.TxOut) {
				apiLog.Errorf("Invalid spending transactions data for %s: %v", txid)
				http.Error(w, http.StatusText(422), 422)
				return
			}
			if !stdscript.IsScriptHashScript(msgTx.TxOut[voutIndex].Version, msgTx.TxOut[voutIndex].PkScript) {
				// only retrieve spending tx for p2sh outputs
				continue
			}
			spendingTxHash, spendingInputIndex := spendingTxHashes[i], spendingTxVinInds[i]
			txhash, err := chainhash.NewHashFromStr(spendingTxHash)
			if err != nil {
				return
			}
			spendingTx, err := c.nodeClient.GetRawTransaction(r.Context(), txhash)
			if err != nil {
				apiLog.Errorf("Unable to get transaction %s: %v", spendingTxHash, err)
				http.Error(w, http.StatusText(422), 422)
				return
			}
			outputSpenders[voutIndex] = &txhelpers.OutputSpenderTxOut{
				Tx:  spendingTx.MsgTx(),
				Vin: spendingInputIndex,
			}
		}
	}

	swapsData, err := txhelpers.MsgTxAtomicSwapsInfo(msgTx, outputSpenders, c.Params)
	if err != nil {
		apiLog.Errorf("Unable to get atomic swap info for transaction %v: %v", txid, err)
		http.Error(w, http.StatusText(422), 422)
		return
	}
	var swapsInfo *txhelpers.TxAtomicSwaps
	if swapsData == nil {
		swapsInfo = &txhelpers.TxAtomicSwaps{
			TxID: txid,
		}
	} else {
		swapsInfo = swapsData.ToAPI()
	}
	if swapsInfo.Found == "" {
		swapsInfo.Found = "No created or redeemed swaps in tx"
	}
	writeJSON(w, swapsInfo, m.GetIndentCtx(r))
}

// getTransactions handles the /txns POST API endpoint.
func (c *appContext) getTransactions(w http.ResponseWriter, r *http.Request) {
	// Look up any spending transactions for each output of this transaction
	// when the client requests spends with the URL query ?spends=true.
	var withSpends bool
	if spendParam := r.URL.Query().Get("spends"); spendParam != "" {
		b, err := strconv.ParseBool(spendParam)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}

		withSpends = b
	}

	txids, err := m.GetTxnsCtx(r)
	if err != nil {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	txns := make([]*apitypes.Tx, 0, len(txids))
	for i := range txids {
		tx := c.DataSource.GetAPITransaction(txids[i])
		if tx == nil {
			apiLog.Errorf("Unable to get transaction %s", txids[i])
			http.Error(w, http.StatusText(422), 422)
			return
		}

		if withSpends {
			if err := c.setTxSpends(tx); err != nil {
				apiLog.Errorf("Unable to get spending transaction info for outputs of %s: %v",
					txids[i], err)
				http.Error(w, http.StatusText(http.StatusInternalServerError),
					http.StatusInternalServerError)
				return
			}
		}

		txns = append(txns, tx)
	}

	writeJSON(w, txns, m.GetIndentCtx(r))
}

func (c *appContext) getDecodedTransactions(w http.ResponseWriter, r *http.Request) {
	txids, err := m.GetTxnsCtx(r)
	if err != nil {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	txns := make([]*apitypes.TrimmedTx, 0, len(txids))
	for i := range txids {
		tx := c.DataSource.GetTrimmedTransaction(txids[i])
		if tx == nil {
			apiLog.Errorf("Unable to get transaction %v", tx)
			http.Error(w, http.StatusText(422), 422)
			return
		}
		txns = append(txns, tx)
	}

	writeJSON(w, txns, m.GetIndentCtx(r))
}

func (c *appContext) getTxVoteInfo(w http.ResponseWriter, r *http.Request) {
	txid, err := m.GetTxIDCtx(r)
	if err != nil {
		http.Error(w, http.StatusText(422), 422)
		return
	}
	vinfo, err := c.DataSource.GetVoteInfo(txid)
	if err != nil {
		err = fmt.Errorf("unable to get vote info for tx %v: %v",
			txid, err)
		apiLog.Error(err)
		errStr := html.EscapeString(err.Error())
		http.Error(w, errStr, 422)
		return
	}
	writeJSON(w, vinfo, m.GetIndentCtx(r))
}

// For /tx/{txid}/tinfo
func (c *appContext) getTxTicketInfo(w http.ResponseWriter, r *http.Request) {
	txid, err := m.GetTxIDCtx(r)
	if err != nil {
		http.Error(w, http.StatusText(422), 422)
		return
	}
	tinfo, err := c.DataSource.GetTicketInfo(txid.String())
	if err != nil {
		if errors.Is(err, dbtypes.ErrNoResult) {
			http.Error(w, "ticket not found", http.StatusNotFound)
			return
		}
		err = fmt.Errorf("unable to get ticket info for tx %v: %w", txid, err)
		apiLog.Error(err)
		errStr := html.EscapeString(err.Error())
		http.Error(w, errStr, 422)
		return
	}
	writeJSON(w, tinfo, m.GetIndentCtx(r))
}

// getTransactionInputs serves []TxIn
func (c *appContext) getTransactionInputs(w http.ResponseWriter, r *http.Request) {
	txid, err := m.GetTxIDCtx(r)
	if err != nil {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	allTxIn := c.DataSource.GetAllTxIn(txid)
	// allTxIn may be empty, but not a nil slice
	if allTxIn == nil {
		apiLog.Errorf("Unable to get all TxIn for transaction %s", txid)
		http.Error(w, http.StatusText(422), 422)
		return
	}

	writeJSON(w, allTxIn, m.GetIndentCtx(r))
}

// getTransactionInput serves TxIn[i]
func (c *appContext) getTransactionInput(w http.ResponseWriter, r *http.Request) {
	txid, err := m.GetTxIDCtx(r)
	if err != nil {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	index := m.GetTxIOIndexCtx(r)
	if index < 0 {
		http.NotFound(w, r)
		//http.Error(w, http.StatusText(422), 422)
		return
	}

	allTxIn := c.DataSource.GetAllTxIn(txid)
	// allTxIn may be empty, but not a nil slice
	if allTxIn == nil {
		apiLog.Warnf("Unable to get all TxIn for transaction %s", txid)
		http.NotFound(w, r)
		return
	}

	if len(allTxIn) <= index {
		apiLog.Debugf("Index %d larger than []TxIn length %d", index, len(allTxIn))
		http.NotFound(w, r)
		return
	}

	writeJSON(w, *allTxIn[index], m.GetIndentCtx(r))
}

// getTransactionOutputs serves []TxOut
func (c *appContext) getTransactionOutputs(w http.ResponseWriter, r *http.Request) {
	txid, err := m.GetTxIDCtx(r)
	if err != nil {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	allTxOut := c.DataSource.GetAllTxOut(txid)
	// allTxOut may be empty, but not a nil slice
	if allTxOut == nil {
		apiLog.Errorf("Unable to get all TxOut for transaction %s", txid)
		http.Error(w, http.StatusText(422), 422)
		return
	}

	writeJSON(w, allTxOut, m.GetIndentCtx(r))
}

// getMultichainTransactionInputs serves []MultichainTxOut
func (c *appContext) getMultichainTransactionInputs(w http.ResponseWriter, r *http.Request) {
	chainType, txid := m.GetMultichainTxID(r)
	if chainType == "" || txid == "" {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	// this function only support for utxo based blockchain
	if chainType == mutilchain.TYPEXMR {
		apiLog.Errorf("This API not support for %s", chainType)
		http.Error(w, http.StatusText(422), 422)
		return
	}

	allTxOut, err := c.DataSource.GetMultichainAllTxIn(chainType, txid)
	// allTxOut may be empty, but not a nil slice
	if err != nil {
		apiLog.Errorf("Unable to get all TxOut for %s transaction %s. Error: %v", chainType, txid, err)
		http.Error(w, http.StatusText(422), 422)
		return
	}

	writeJSON(w, allTxOut, m.GetIndentCtx(r))
}

// getMultichainTransactionInput serves []MultichainTxIn
func (c *appContext) getMultichainTransactionInput(w http.ResponseWriter, r *http.Request) {
	chainType, txid := m.GetMultichainTxID(r)
	if chainType == "" || txid == "" {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	// this function only support for utxo based blockchain
	if chainType == mutilchain.TYPEXMR {
		apiLog.Errorf("This API not support for %s", chainType)
		http.Error(w, http.StatusText(422), 422)
		return
	}

	index := m.GetTxIOIndexCtx(r)
	if index < 0 {
		http.NotFound(w, r)
		return
	}

	allTxIn, err := c.DataSource.GetMultichainAllTxIn(chainType, txid)
	// allTxIn may be empty, but not a nil slice
	if err != nil {
		apiLog.Warnf("Unable to get all TxIn for %s transaction %s", chainType, txid)
		http.NotFound(w, r)
		return
	}

	if len(allTxIn) <= index {
		apiLog.Debugf("[%s] Index %d larger than []TxIn length %d", chainType, index, len(allTxIn))
		http.NotFound(w, r)
		return
	}

	writeJSON(w, *allTxIn[index], m.GetIndentCtx(r))
}

// getMultichainTransactionOutputs serves []MultichainTxOut
func (c *appContext) getMultichainTransactionOutputs(w http.ResponseWriter, r *http.Request) {
	chainType, txid := m.GetMultichainTxID(r)
	if chainType == "" || txid == "" {
		http.Error(w, http.StatusText(422), 422)
		return
	}
	// this function only support for utxo based blockchain
	if chainType == mutilchain.TYPEXMR {
		apiLog.Errorf("This API not support for %s", chainType)
		http.Error(w, http.StatusText(422), 422)
		return
	}
	allTxOut, err := c.DataSource.GetMultichainAllTxOut(chainType, txid)
	// allTxOut may be empty, but not a nil slice
	if err != nil {
		apiLog.Errorf("Unable to get all TxOut for %s transaction %s. Error: %v", chainType, txid, err)
		http.Error(w, http.StatusText(422), 422)
		return
	}

	writeJSON(w, allTxOut, m.GetIndentCtx(r))
}

// getMultichainTransactionOutput serves MultichainTxOut
func (c *appContext) getMultichainTransactionOutput(w http.ResponseWriter, r *http.Request) {
	chainType, txid := m.GetMultichainTxID(r)
	if chainType == "" || txid == "" {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	// this function only support for utxo based blockchain
	if chainType == mutilchain.TYPEXMR {
		apiLog.Errorf("This API not support for %s", chainType)
		http.Error(w, http.StatusText(422), 422)
		return
	}

	index := m.GetTxIOIndexCtx(r)
	if index < 0 {
		http.NotFound(w, r)
		return
	}

	allTxOut, err := c.DataSource.GetMultichainAllTxOut(chainType, txid)
	// allTxOut may be empty, but not a nil slice
	if err != nil {
		apiLog.Errorf("Unable to get all TxOut for %s transaction %s. Error: %v", chainType, txid, err)
		http.Error(w, http.StatusText(422), 422)
		return
	}

	if len(allTxOut) <= index {
		apiLog.Debugf("[%s] Index %d larger than []TxOut length %d", chainType, index, len(allTxOut))
		http.NotFound(w, r)
		return
	}

	writeJSON(w, *allTxOut[index], m.GetIndentCtx(r))
}

// getTransactionOutput serves TxOut[i]
func (c *appContext) getTransactionOutput(w http.ResponseWriter, r *http.Request) {
	txid, err := m.GetTxIDCtx(r)
	if err != nil {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	index := m.GetTxIOIndexCtx(r)
	if index < 0 {
		http.NotFound(w, r)
		return
	}

	allTxOut := c.DataSource.GetAllTxOut(txid)
	// allTxOut may be empty, but not a nil slice
	if allTxOut == nil {
		apiLog.Errorf("Unable to get all TxOut for transaction %s", txid)
		http.Error(w, http.StatusText(422), 422)
		return
	}

	if len(allTxOut) <= index {
		apiLog.Debugf("Index %d larger than []TxOut length %d", index, len(allTxOut))
		http.NotFound(w, r)
		return
	}

	writeJSON(w, *allTxOut[index], m.GetIndentCtx(r))
}

// getBlockStakeInfoExtendedByHash retrieves the apitype.StakeInfoExtended
// for the given blockhash
func (c *appContext) getBlockStakeInfoExtendedByHash(w http.ResponseWriter, r *http.Request) {
	hash, err := c.getBlockHashCtx(r)
	if err != nil {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	stakeinfo := c.DataSource.GetStakeInfoExtendedByHash(hash)
	if stakeinfo == nil {
		apiLog.Errorf("Unable to get block fee info for %s", hash)
		http.Error(w, http.StatusText(422), 422)
		return
	}

	writeJSON(w, stakeinfo, m.GetIndentCtx(r))
}

// getBlockStakeInfoExtendedByHeight retrieves the apitype.StakeInfoExtended
// for the given blockheight on mainchain
func (c *appContext) getBlockStakeInfoExtendedByHeight(w http.ResponseWriter, r *http.Request) {
	idx, err := c.getBlockHeightCtx(r)
	if err != nil {
		http.Error(w, http.StatusText(422), 422)
		return
	}
	stakeinfo := c.DataSource.GetStakeInfoExtendedByHeight(int(idx))
	if stakeinfo == nil {
		apiLog.Errorf("Unable to get stake info for height %d", idx)
		http.Error(w, http.StatusText(422), 422)
		return
	}

	writeJSON(w, stakeinfo, m.GetIndentCtx(r))
}

func (c *appContext) getStakeDiffSummary(w http.ResponseWriter, r *http.Request) {
	stakeDiff := c.DataSource.GetStakeDiffEstimates()
	if stakeDiff == nil {
		apiLog.Errorf("Unable to get stake diff info")
		http.Error(w, http.StatusText(422), 422)
		return
	}

	writeJSON(w, stakeDiff, m.GetIndentCtx(r))
}

// Encodes apitypes.PowerlessTickets, which is missed or expired tickets sorted
// by revocation status.
func (c *appContext) getPowerlessTickets(w http.ResponseWriter, r *http.Request) {
	tickets, err := c.DataSource.PowerlessTickets()
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	writeJSON(w, tickets, m.GetIndentCtx(r))
}

func (c *appContext) getStakeDiffCurrent(w http.ResponseWriter, r *http.Request) {
	stakeDiff := c.DataSource.GetStakeDiffEstimates()
	if stakeDiff == nil {
		apiLog.Errorf("Unable to get stake diff info")
		http.Error(w, http.StatusText(422), 422)
		return
	}

	stakeDiffCurrent := chainjson.GetStakeDifficultyResult{
		CurrentStakeDifficulty: stakeDiff.CurrentStakeDifficulty,
		NextStakeDifficulty:    stakeDiff.NextStakeDifficulty,
	}

	writeJSON(w, stakeDiffCurrent, m.GetIndentCtx(r))
}

func (c *appContext) getStakeDiffEstimates(w http.ResponseWriter, r *http.Request) {
	stakeDiff := c.DataSource.GetStakeDiffEstimates()
	if stakeDiff == nil {
		apiLog.Errorf("Unable to get stake diff info")
		http.Error(w, http.StatusText(422), 422)
		return
	}

	writeJSON(w, stakeDiff.Estimates, m.GetIndentCtx(r))
}

func (c *appContext) getSSTxSummary(w http.ResponseWriter, r *http.Request) {
	sstxSummary := c.DataSource.GetMempoolSSTxSummary()
	if sstxSummary == nil {
		apiLog.Errorf("Unable to get SSTx info from mempool")
		http.Error(w, http.StatusText(422), 422)
		return
	}

	writeJSON(w, sstxSummary, m.GetIndentCtx(r))
}

func (c *appContext) getSSTxFees(w http.ResponseWriter, r *http.Request) {
	N := m.GetNCtx(r)
	sstxFees := c.DataSource.GetMempoolSSTxFeeRates(N)
	if sstxFees == nil {
		apiLog.Errorf("Unable to get SSTx fees from mempool")
		http.Error(w, http.StatusText(422), 422)
		return
	}

	writeJSON(w, sstxFees, m.GetIndentCtx(r))
}

func (c *appContext) getSSTxDetails(w http.ResponseWriter, r *http.Request) {
	N := m.GetNCtx(r)
	sstxDetails := c.DataSource.GetMempoolSSTxDetails(N)
	if sstxDetails == nil {
		apiLog.Errorf("Unable to get SSTx details from mempool")
		http.Error(w, http.StatusText(422), 422)
		return
	}

	writeJSON(w, sstxDetails, m.GetIndentCtx(r))
}

// getTicketPoolCharts pulls the initial data to populate the /ticketpool page
// charts.
func (c *appContext) getTicketPoolCharts(w http.ResponseWriter, r *http.Request) {
	timeChart, priceChart, outputsChart, height, err := c.DataSource.TicketPoolVisualization(dbtypes.AllGrouping)
	if dbtypes.IsTimeoutErr(err) {
		apiLog.Errorf("TicketPoolVisualization: %v", err)
		http.Error(w, "Database timeout.", http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		apiLog.Errorf("Unable to get ticket pool charts: %v", err)
		http.Error(w, http.StatusText(http.StatusUnprocessableEntity), http.StatusUnprocessableEntity)
		return
	}

	mp := c.DataSource.GetMempoolPriceCountTime()

	response := &apitypes.TicketPoolChartsData{
		ChartHeight:  uint64(height),
		TimeChart:    timeChart,
		PriceChart:   priceChart,
		OutputsChart: outputsChart,
		Mempool:      mp,
	}

	writeJSON(w, response, m.GetIndentCtx(r))
}

func (c *appContext) getTicketPoolByDate(w http.ResponseWriter, r *http.Request) {
	tp := m.GetTpCtx(r)
	// default to day if no grouping was sent
	if tp == "" {
		tp = "day"
	}

	// The db queries are fast enough that it makes sense to call
	// TicketPoolVisualization here even though it returns a lot of data not
	// needed by this request.
	interval := dbtypes.TimeGroupingFromStr(tp)
	if interval == dbtypes.UnknownGrouping {
		http.Error(w, http.StatusText(http.StatusUnprocessableEntity), http.StatusUnprocessableEntity)
		return
	}
	timeChart, _, _, height, err := c.DataSource.TicketPoolVisualization(interval)
	if dbtypes.IsTimeoutErr(err) {
		apiLog.Errorf("TicketPoolVisualization: %v", err)
		http.Error(w, "Database timeout.", http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		apiLog.Errorf("Unable to get ticket pool by date: %v", err)
		http.Error(w, http.StatusText(http.StatusUnprocessableEntity), http.StatusUnprocessableEntity)
		return
	}

	tpResponse := struct {
		Height    int64                    `json:"height"`
		TimeChart *dbtypes.PoolTicketsData `json:"time_chart"`
	}{
		height,
		timeChart, // purchase time distribution
	}

	writeJSON(w, tpResponse, m.GetIndentCtx(r))
}

func (c *appContext) getProposalChartData(w http.ResponseWriter, r *http.Request) {
	token := m.GetProposalTokenCtx(r)

	proposal, err := c.ProposalsDB.ProposalByToken(token)
	if dbtypes.IsTimeoutErr(err) {
		apiLog.Errorf("ProposalByToken: %v", err)
		http.Error(w, "Database timeout.", http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		apiLog.Errorf("Unable to get proposal chart data for token %s : %v", token, err)
		http.Error(w, http.StatusText(http.StatusUnprocessableEntity),
			http.StatusUnprocessableEntity)
		return
	}

	writeJSON(w, proposal.ChartData, m.GetIndentCtx(r))
}

func (c *appContext) getBlockSize(w http.ResponseWriter, r *http.Request) {
	idx, err := c.getBlockHeightCtx(r)
	if err != nil {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	blockSize, err := c.DataSource.GetBlockSize(int(idx))
	if err != nil {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	writeJSON(w, blockSize, "")
}

func (c *appContext) blockSubsidies(w http.ResponseWriter, r *http.Request) {
	idx, err := c.getBlockHeightCtx(r)
	if err != nil {
		http.Error(w, http.StatusText(422), 422)
		return
	}
	hash, err := c.getBlockHashCtx(r)
	if err != nil {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	// Unless this is a mined block, assume all votes.
	numVotes := int16(c.Params.TicketsPerBlock)
	if hash != "" {
		var err error
		numVotes, err = c.DataSource.VotesInBlock(hash)
		if dbtypes.IsTimeoutErr(err) {
			apiLog.Errorf("VotesInBlock: %v", err)
			http.Error(w, "Database timeout.", http.StatusServiceUnavailable)
			return
		}
		if err != nil {
			http.NotFound(w, r)
			return
		}
	}

	ssv := standalone.SSVOriginal
	if c.DataSource.IsDCP0012Active(idx) {
		ssv = standalone.SSVDCP0012
	} else if c.DataSource.IsDCP0010Active(idx) {
		ssv = standalone.SSVDCP0010
	}
	work, stake, tax := txhelpers.RewardsAtBlock(idx, uint16(numVotes), c.Params, ssv)
	rewards := apitypes.BlockSubsidies{
		BlockNum:   idx,
		BlockHash:  hash,
		Work:       work,
		Stake:      stake,
		NumVotes:   numVotes,
		TotalStake: stake * int64(numVotes),
		Tax:        tax,
		Total:      work + stake*int64(numVotes) + tax,
	}

	writeJSON(w, rewards, m.GetIndentCtx(r))
}

func (c *appContext) getBlockRangeSize(w http.ResponseWriter, r *http.Request) {
	idx0 := m.GetBlockIndex0Ctx(r)
	if idx0 < 0 {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	idx := m.GetBlockIndexCtx(r)
	if idx < 0 || idx < idx0 {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	blockSizes, err := c.DataSource.GetBlockSizeRange(idx0, idx)
	if err != nil {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	writeJSON(w, blockSizes, "")
}

func (c *appContext) getBlockRangeSteppedSize(w http.ResponseWriter, r *http.Request) {
	idx0 := m.GetBlockIndex0Ctx(r)
	if idx0 < 0 {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	idx := m.GetBlockIndexCtx(r)
	if idx < 0 || idx < idx0 {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	step := m.GetBlockStepCtx(r)
	if step <= 0 {
		http.Error(w, "Yeaaah, that step's not gonna work with me.", 422)
		return
	}

	blockSizesFull, err := c.DataSource.GetBlockSizeRange(idx0, idx)
	if err != nil {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	var blockSizes []int32
	if step == 1 {
		blockSizes = blockSizesFull
	} else {
		numValues := (idx - idx0 + 1) / step
		blockSizes = make([]int32, 0, numValues)
		for i := idx0; i <= idx; i += step {
			blockSizes = append(blockSizes, blockSizesFull[i-idx0])
		}
		// it's the client's problem if i doesn't go all the way to idx
	}

	writeJSON(w, blockSizes, "")
}

func (c *appContext) getBlockRangeSummary(w http.ResponseWriter, r *http.Request) {
	idx0 := m.GetBlockIndex0Ctx(r)
	idx1 := m.GetBlockIndexCtx(r)

	low, high := idx0, idx1
	if idx0 > idx1 {
		low, high = idx1, idx0
	}
	if low < 0 || uint32(high) > c.Status.Height() {
		http.Error(w, "invalid block range", http.StatusBadRequest)
		return
	}

	if high-low+1 > maxBlockRangeCount {
		http.Error(w, fmt.Sprintf("requested more than %d-block maximum", maxBlockRangeCount), http.StatusBadRequest)
		return
	}

	blocks := c.DataSource.GetSummaryRange(idx0, idx1)
	if blocks == nil {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	writeJSON(w, blocks, m.GetIndentCtx(r))
}

func (c *appContext) getBlockRangeSteppedSummary(w http.ResponseWriter, r *http.Request) {
	idx0 := m.GetBlockIndex0Ctx(r)
	idx1 := m.GetBlockIndexCtx(r)
	step := m.GetBlockStepCtx(r)
	if step <= 0 {
		http.Error(w, "Yeaaah, that step's not gonna work with me.", 422)
		return
	}

	low, high := idx0, idx1
	if idx0 > idx1 {
		low, high = idx1, idx0
	}
	if low < 0 || uint32(high) > c.Status.Height() {
		http.Error(w, "invalid block range", http.StatusBadRequest)
		return
	}

	if (high-low)/step+1 > maxBlockRangeCount {
		http.Error(w, fmt.Sprintf("requested more than %d-block maximum", maxBlockRangeCount), http.StatusBadRequest)
		return
	}

	blocks := c.DataSource.GetSummaryRangeStepped(idx0, idx1, step)
	if blocks == nil {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	writeJSON(w, blocks, m.GetIndentCtx(r))
}

func (c *appContext) getTicketPool(w http.ResponseWriter, r *http.Request) {
	var sortPool bool
	if sortParam := r.URL.Query().Get("sort"); sortParam != "" {
		val, err := strconv.ParseBool(sortParam)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		sortPool = val
	}

	// getBlockHeightCtx falls back to try hash if height fails
	idx, err := c.getBlockHeightCtx(r)
	if err != nil {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	tp, err := c.DataSource.GetPool(idx)
	if err != nil {
		apiLog.Errorf("Unable to fetch ticket pool: %v", err)
		http.Error(w, http.StatusText(422), 422)
		return
	}

	if sortPool {
		sort.Strings(tp)
	}
	writeJSON(w, tp, m.GetIndentCtx(r))
}

func (c *appContext) getTicketPoolInfo(w http.ResponseWriter, r *http.Request) {
	idx, err := c.getBlockHeightCtx(r)
	if err != nil {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	tpi := c.DataSource.GetPoolInfo(int(idx))
	writeJSON(w, tpi, m.GetIndentCtx(r))
}

func (c *appContext) getTicketPoolInfoRange(w http.ResponseWriter, r *http.Request) {
	if useArray := r.URL.Query().Get("arrays"); useArray != "" {
		_, err := strconv.ParseBool(useArray)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		c.getTicketPoolValAndSizeRange(w, r)
		return
	}

	idx0 := m.GetBlockIndex0Ctx(r)
	if idx0 < 0 {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	idx := m.GetBlockIndexCtx(r)
	if idx < 0 {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	tpis := c.DataSource.GetPoolInfoRange(idx0, idx)
	if tpis == nil {
		http.Error(w, "invalid range", http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, tpis, m.GetIndentCtx(r))
}

func (c *appContext) getTicketPoolValAndSizeRange(w http.ResponseWriter, r *http.Request) {
	idx0 := m.GetBlockIndex0Ctx(r)
	if idx0 < 0 {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	idx := m.GetBlockIndexCtx(r)
	if idx < 0 {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	pvs, pss := c.DataSource.GetPoolValAndSizeRange(idx0, idx)
	if pvs == nil || pss == nil {
		http.Error(w, "invalid range", http.StatusUnprocessableEntity)
		return
	}

	tPVS := apitypes.TicketPoolValsAndSizes{
		StartHeight: uint32(idx0),
		EndHeight:   uint32(idx),
		Value:       pvs,
		Size:        pss,
	}
	writeJSON(w, tPVS, m.GetIndentCtx(r))
}

func (c *appContext) getStakeDiff(w http.ResponseWriter, r *http.Request) {
	idx, err := c.getBlockHeightCtx(r)
	if err != nil {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	sdiff := c.DataSource.GetSDiff(int(idx))
	writeJSON(w, []float64{sdiff}, m.GetIndentCtx(r))
}

func (c *appContext) getStakeDiffRange(w http.ResponseWriter, r *http.Request) {
	idx0 := m.GetBlockIndex0Ctx(r)
	if idx0 < 0 {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	idx := m.GetBlockIndexCtx(r)
	if idx < 0 {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	sdiffs := c.DataSource.GetSDiffRange(idx0, idx)
	writeJSON(w, sdiffs, m.GetIndentCtx(r))
}

func (c *appContext) addressTotals(w http.ResponseWriter, r *http.Request) {
	addresses, err := m.GetAddressCtx(r, c.Params)
	if err != nil || len(addresses) > 1 {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	address := addresses[0]
	totals, err := c.DataSource.AddressTotals(address)
	if dbtypes.IsTimeoutErr(err) {
		apiLog.Errorf("AddressTotals: %v", err)
		http.Error(w, "Database timeout.", http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		log.Warnf("failed to get address totals (%s): %v", address, err)
		http.Error(w, http.StatusText(422), 422)
		return
	}

	writeJSON(w, totals, m.GetIndentCtx(r))
}

func (c *appContext) multichainAddressTotals(w http.ResponseWriter, r *http.Request) {
	chainType, address := m.GetMultichainAddressCtx(r)
	if chainType == "" || address == "" {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	// this function only support for utxo based blockchain
	if chainType == mutilchain.TYPEXMR {
		apiLog.Errorf("This API not support for %s", chainType)
		http.Error(w, http.StatusText(422), 422)
		return
	}

	totals, err := c.DataSource.MultichainAddressTotals(chainType, address)
	if dbtypes.IsTimeoutErr(err) {
		apiLog.Errorf("MultichainAddressTotals: %v", err)
		http.Error(w, "Database timeout.", http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		log.Warnf("failed to get %s address totals (%s): %v", chainType, address, err)
		http.Error(w, http.StatusText(422), 422)
		return
	}

	writeJSON(w, totals, m.GetIndentCtx(r))
}

// addressExists provides access to the existsaddresses RPC call and parses the
// hexadecimal string into a list of bools. A maximum of 64 addresses can be
// provided. Duplicates are not filtered.
func (c *appContext) addressExists(w http.ResponseWriter, r *http.Request) {
	addresses, err := m.GetAddressRawCtx(r, c.Params)
	if err != nil {
		apiLog.Errorf("addressExists rejecting request: %v", err)
		http.Error(w, "address parsing error", http.StatusBadRequest)
		return
	}
	// GetAddressCtx throws an error if there would be no addresses.
	strMask, err := c.nodeClient.ExistsAddresses(context.TODO(), addresses)
	if err != nil {
		log.Warnf("existsaddress error: %v", err)
		http.Error(w, http.StatusText(422), 422)
	}
	b, err := hex.DecodeString(strMask)
	if err != nil {
		log.Warnf("existsaddress error: %v", err)
		http.Error(w, http.StatusText(422), 422)
	}
	mask := binary.LittleEndian.Uint64(append(b, make([]byte, 8-len(b))...))
	exists := make([]bool, 0, len(addresses))
	for n := range addresses {
		exists = append(exists, (mask&(1<<uint8(n))) != 0)
	}
	writeJSON(w, exists, m.GetIndentCtx(r))
}

func (c *appContext) addressIoCsvNoCR(w http.ResponseWriter, r *http.Request) {
	c.addressIoCsv(false, w, r)
}
func (c *appContext) addressIoCsvCR(w http.ResponseWriter, r *http.Request) {
	c.addressIoCsv(true, w, r)
}

// Handler for address activity CSV file download.
// /download/address/io/{address}[/win]
func (c *appContext) addressIoCsv(crlf bool, w http.ResponseWriter, r *http.Request) {
	wf, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "unable to flush streamed data", http.StatusBadRequest)
		return
	}

	addresses, err := m.GetAddressCtx(r, c.Params)
	if err != nil || len(addresses) > 1 {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	address := addresses[0]

	_, err = stdaddr.DecodeAddress(address, c.Params)
	if err != nil {
		log.Debugf("Error validating address %s: %v", address, err)
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}

	// TODO: Improve the DB component also to avoid retrieving all row data
	// and/or put a hard limit on the number of rows that can be retrieved.
	// However it is a slice of pointers, and they are are also in the address
	// cache and thus shared across calls to the same address.
	rows, err := c.DataSource.AddressRowsCompact(address)
	if err != nil {
		log.Errorf("Failed to fetch AddressTxIoCsv: %v", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	filename := fmt.Sprintf("address-io-%s-%d-%s.csv", address,
		c.Status.Height(), strconv.FormatInt(time.Now().Unix(), 10))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment;filename=%s", filename))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	writer := csv.NewWriter(w)
	writer.UseCRLF = crlf

	err = writer.Write([]string{"tx_hash", "direction", "io_index",
		"valid_mainchain", "value", "time_stamp", "tx_type", "matching_tx_hash"})
	if err != nil {
		return // too late to write an error code
	}
	writer.Flush()
	wf.Flush()

	var strValidMainchain, strDirection string
	for _, r := range rows {
		if r.ValidMainChain {
			strValidMainchain = "1"
		} else {
			strValidMainchain = "0"
		}
		if r.IsFunding {
			strDirection = "1"
		} else {
			strDirection = "-1"
		}

		err = writer.Write([]string{
			r.TxHash.String(),
			strDirection,
			strconv.FormatUint(uint64(r.TxVinVoutIndex), 10),
			strValidMainchain,
			strconv.FormatFloat(dcrutil.Amount(r.Value).ToCoin(), 'f', -1, 64),
			strconv.FormatInt(r.TxBlockTime, 10),
			txhelpers.TxTypeToString(int(r.TxType)),
			r.MatchingTxHash.String(),
		})
		if err != nil {
			return // too late to write an error code
		}
		writer.Flush()
		wf.Flush()
	}
}

// Get contract count chart data for atomic swap
func (c *appContext) getSwapsTxcountChartData(w http.ResponseWriter, r *http.Request) {
	chartGrouping := m.GetChartGroupingCtx(r)
	if chartGrouping == "" {
		http.Error(w, http.StatusText(http.StatusUnprocessableEntity), http.StatusUnprocessableEntity)
		return
	}

	interval := dbtypes.TimeGroupingFromStr(chartGrouping)
	if interval == dbtypes.UnknownGrouping {
		http.Error(w, http.StatusText(http.StatusUnprocessableEntity), http.StatusUnprocessableEntity)
		return
	}
	data, err := c.DataSource.SwapsChartData(dbtypes.SwapTxCount, interval)
	if dbtypes.IsTimeoutErr(err) {
		apiLog.Errorf("SwapsChartData by txcount: %v", err)
		http.Error(w, "Database timeout.", http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		log.Warnf("failed to get swap chart data by txcount : %v", err)
		http.Error(w, http.StatusText(http.StatusUnprocessableEntity), http.StatusUnprocessableEntity)
		return
	}

	if len(data.Time) > 0 {
		lastTime := data.Time[len(data.Time)-1]
		now := time.Now()
		//one day before
		oneDayBefore := now.AddDate(0, -1, 0)
		addNow := lastTime.T.Before(oneDayBefore)
		if addNow {
			data.Time = append(data.Time, dbtypes.NewTimeDef(now))
			data.RedeemCount = append(data.RedeemCount, 0)
			data.RefundCount = append(data.RefundCount, 0)
		}
	}
	writeJSON(w, data, m.GetIndentCtx(r))
}

// Get trading amount chart data for atomic swaps
func (c *appContext) getSwapsAmountChartData(w http.ResponseWriter, r *http.Request) {
	chartGrouping := m.GetChartGroupingCtx(r)
	if chartGrouping == "" {
		http.Error(w, http.StatusText(http.StatusUnprocessableEntity), http.StatusUnprocessableEntity)
		return
	}

	interval := dbtypes.TimeGroupingFromStr(chartGrouping)
	if interval == dbtypes.UnknownGrouping {
		http.Error(w, http.StatusText(http.StatusUnprocessableEntity), http.StatusUnprocessableEntity)
		return
	}
	data, err := c.DataSource.SwapsChartData(dbtypes.SwapAmount, interval)
	if dbtypes.IsTimeoutErr(err) {
		apiLog.Errorf("SwapsChartData by amount: %v", err)
		http.Error(w, "Database timeout.", http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		log.Warnf("failed to get swap chart data by amount : %v", err)
		http.Error(w, http.StatusText(http.StatusUnprocessableEntity), http.StatusUnprocessableEntity)
		return
	}

	if len(data.Time) > 0 {
		lastTime := data.Time[len(data.Time)-1]
		now := time.Now()
		//one day before
		oneDayBefore := now.AddDate(0, -1, 0)
		addNow := lastTime.T.Before(oneDayBefore)
		if addNow {
			data.Time = append(data.Time, dbtypes.NewTimeDef(now))
			data.RedeemAmount = append(data.RedeemAmount, 0)
			data.RefundAmount = append(data.RefundAmount, 0)
		}
	}
	writeJSON(w, data, m.GetIndentCtx(r))
}

func (c *appContext) getAddressTxTypesData(w http.ResponseWriter, r *http.Request) {
	addresses, err := m.GetAddressCtx(r, c.Params)
	if err != nil || len(addresses) > 1 {
		http.Error(w, http.StatusText(http.StatusUnprocessableEntity), http.StatusUnprocessableEntity)
		return
	}
	address := addresses[0]

	chartGrouping := m.GetChartGroupingCtx(r)
	if chartGrouping == "" {
		http.Error(w, http.StatusText(http.StatusUnprocessableEntity), http.StatusUnprocessableEntity)
		return
	}

	interval := dbtypes.TimeGroupingFromStr(chartGrouping)
	if interval == dbtypes.UnknownGrouping {
		http.Error(w, http.StatusText(http.StatusUnprocessableEntity), http.StatusUnprocessableEntity)
		return
	}
	data, err := c.DataSource.TxHistoryData(address, dbtypes.TxsType, interval)
	if dbtypes.IsTimeoutErr(err) {
		apiLog.Errorf("TxHistoryData: %v", err)
		http.Error(w, "Database timeout.", http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		log.Warnf("failed to get address (%s) history by tx type : %v", address, err)
		http.Error(w, http.StatusText(http.StatusUnprocessableEntity), http.StatusUnprocessableEntity)
		return
	}

	if len(data.Time) > 0 {
		lastTime := data.Time[len(data.Time)-1]
		now := time.Now()
		//one day before
		oneDayBefore := now.AddDate(0, -1, 0)
		addNow := lastTime.T.Before(oneDayBefore)
		if addNow {
			data.Time = append(data.Time, dbtypes.NewTimeDef(now))
			data.SentRtx = append(data.SentRtx, 0)
			data.ReceivedRtx = append(data.ReceivedRtx, 0)
			data.Tickets = append(data.Tickets, 0)
			data.Votes = append(data.Votes, 0)
			data.RevokeTx = append(data.RevokeTx, 0)
		}
	}

	writeJSON(w, data, m.GetIndentCtx(r))
}

func (c *appContext) getAddressTxAmountFlowData(w http.ResponseWriter, r *http.Request) {
	addresses, err := m.GetAddressCtx(r, c.Params)
	if err != nil || len(addresses) > 1 {
		http.Error(w, http.StatusText(http.StatusUnprocessableEntity), http.StatusUnprocessableEntity)
		return
	}
	address := addresses[0]

	chartGrouping := m.GetChartGroupingCtx(r)
	if chartGrouping == "" {
		http.Error(w, http.StatusText(http.StatusUnprocessableEntity), http.StatusUnprocessableEntity)
		return
	}
	interval := dbtypes.TimeGroupingFromStr(chartGrouping)
	if interval == dbtypes.UnknownGrouping {
		http.Error(w, http.StatusText(http.StatusUnprocessableEntity), http.StatusUnprocessableEntity)
		return
	}
	data, err := c.DataSource.TxHistoryData(address, dbtypes.AmountFlow, interval)
	if dbtypes.IsTimeoutErr(err) {
		apiLog.Errorf("TxHistoryData: %v", err)
		http.Error(w, "Database timeout.", http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		log.Warnf("failed to get address (%s) history by amount flow: %v", address, err)
		http.Error(w, http.StatusText(http.StatusUnprocessableEntity), http.StatusUnprocessableEntity)
		return
	}
	if len(data.Time) > 0 {
		lastTime := data.Time[len(data.Time)-1]
		now := time.Now()
		//one day before
		oneDayBefore := now.AddDate(0, -1, 0)
		addNow := lastTime.T.Before(oneDayBefore)
		if addNow {
			data.Time = append(data.Time, dbtypes.NewTimeDef(now))
			data.Sent = append(data.Sent, 0)
			data.Received = append(data.Received, 0)
			data.Net = append(data.Net, 0)
		}
	}
	writeJSON(w, data, m.GetIndentCtx(r))
}

func (c *appContext) getTreasuryBalance(w http.ResponseWriter, r *http.Request) {
	treasuryBalance, err := c.DataSource.TreasuryBalance()
	if err != nil {
		log.Errorf("TreasuryBalance failed: %v", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	writeJSON(w, treasuryBalance, m.GetIndentCtx(r))
}

func (c *appContext) getTreasuryIO(w http.ResponseWriter, r *http.Request) {
	chartGrouping := m.GetChartGroupingCtx(r)
	if chartGrouping == "" {
		http.Error(w, http.StatusText(http.StatusUnprocessableEntity), http.StatusUnprocessableEntity)
		return
	}
	interval := dbtypes.TimeGroupingFromStr(chartGrouping)
	if interval == dbtypes.UnknownGrouping {
		http.Error(w, http.StatusText(http.StatusUnprocessableEntity), http.StatusUnprocessableEntity)
		return
	}
	data, err := c.DataSource.BinnedTreasuryIO(interval)
	if dbtypes.IsTimeoutErr(err) {
		apiLog.Errorf("BinnedTreasuryIO: %v", err)
		http.Error(w, "Database timeout.", http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		log.Warnf("failed to get treasury i/o: %v", err)
		http.Error(w, http.StatusText(http.StatusUnprocessableEntity), http.StatusUnprocessableEntity)
		return
	}
	if len(data.Time) > 0 {
		lastTime := data.Time[len(data.Time)-1]
		now := time.Now()
		//one day before
		oneDayBefore := now.AddDate(0, -1, 0)
		addNow := lastTime.T.Before(oneDayBefore)
		if addNow {
			data.Time = append(data.Time, dbtypes.NewTimeDef(now))
			data.Sent = append(data.Sent, 0)
			data.Received = append(data.Received, 0)
			data.Net = append(data.Net, 0)
		}
	}
	writeJSON(w, data, m.GetIndentCtx(r))
}

func (c *appContext) ChartTypeData(w http.ResponseWriter, r *http.Request) {
	chartType := m.GetChartTypeCtx(r)
	bin := r.URL.Query().Get("bin")
	rangeOption := r.URL.Query().Get("range")
	// Support the deprecated URL parameter "zoom".
	if bin == "" {
		bin = r.URL.Query().Get("zoom")
	}
	axis := r.URL.Query().Get("axis")
	chartData, err := c.charts.Chart(chartType, bin, axis, rangeOption)
	if err != nil {
		http.NotFound(w, r)
		log.Warnf(`Error fetching chart %q at bin level '%s': %v`, chartType, bin, err)
		return
	}
	writeJSONBytes(w, chartData)
}

func (c *appContext) MoneroDecodeOutputs(w http.ResponseWriter, r *http.Request) {
	txid := r.URL.Query().Get("txid")
	address := r.URL.Query().Get("address")
	viewkey := r.URL.Query().Get("viewkey")
	type decodedData struct {
		Error      bool                   `json:"err"`
		Msg        string                 `json:"msg"`
		DecodeData []externalapi.TxOutput `json:"decodeData"`
	}
	if txid == "" || address == "" || viewkey == "" {
		writeJSON(w, &decodedData{
			Error: true,
			Msg:   "There is an empty param. Please check or try again.",
		}, m.GetIndentCtx(r))
		return
	}
	// handler decode outputs for monero tx
	decodeOuput, err := c.DataSource.MoneroDecodeOutputs(txid, address, viewkey)
	if err != nil {
		writeJSON(w, &decodedData{
			Error: true,
			Msg:   "An error occurred while decoding outputs.",
		}, m.GetIndentCtx(r))
		return
	}
	writeJSON(w, &decodedData{
		Error:      false,
		DecodeData: decodeOuput,
	}, m.GetIndentCtx(r))
}

func (c *appContext) MoneroProveTx(w http.ResponseWriter, r *http.Request) {
	txid := r.URL.Query().Get("txid")
	address := r.URL.Query().Get("address")
	txkey := r.URL.Query().Get("txkey")
	type proveData struct {
		Error        bool                   `json:"err"`
		ErrorContent string                 `json:"errorContent"`
		Msg          string                 `json:"msg"`
		ProveData    []externalapi.TxOutput `json:"proveData"`
	}
	if txid == "" || address == "" || txkey == "" {
		writeJSON(w, &proveData{
			Error: true,
			Msg:   "There is an empty param. Please check or try again.",
		}, m.GetIndentCtx(r))
		return
	}
	// handler decode outputs for monero tx
	proveOutput, err := c.DataSource.MoneroProveOutputs(txid, address, txkey)
	if err != nil {
		log.Errorf("XMR: error where prove tx: %v", err)
		writeJSON(w, &proveData{
			Error:        true,
			ErrorContent: err.Error(),
			Msg:          "An error occurred while prove sending.",
		}, m.GetIndentCtx(r))
		return
	}
	writeJSON(w, &proveData{
		Error:     false,
		ProveData: proveOutput,
	}, m.GetIndentCtx(r))
}

func (c *appContext) getMoneroTransactions(w http.ResponseWriter, r *http.Request) {
	// get latest txs for monero
	txs, err := c.DataSource.GetMoneroLastestTransactions()
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	writeJSON(w, txs, m.GetIndentCtx(r))
}

func (c *appContext) getMoneroMempool(w http.ResponseWriter, r *http.Request) {
	mempool, err := c.DataSource.GetMoneroMempoolDetail()
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	writeJSON(w, mempool, m.GetIndentCtx(r))
}

func (c *appContext) getMoneroNetworkInfo(w http.ResponseWriter, r *http.Request) {
	netInfo, err := c.DataSource.GetMoneroNetworkInfo()
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	writeJSON(w, netInfo, m.GetIndentCtx(r))
}

func (c *appContext) getMoneroBlockSummary(w http.ResponseWriter, r *http.Request) {
	blockhash, _ := m.GetBlockHashStrCtx(r)
	if blockhash == "" {
		// get block idx
		blockidx, _ := m.GetMultichainBlockHeightCtx(r)
		if blockidx < 0 {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		blockhash = fmt.Sprintf("%d", blockidx)
	}

	blockDetail, err := c.DataSource.GetMoneroBlockDetail(blockhash)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	writeJSON(w, blockDetail, m.GetIndentCtx(r))
}

func (c *appContext) getMoneroTransactionRaw(w http.ResponseWriter, r *http.Request) {
	txhash, err := m.GetTxhashStrCtx(r)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	txRaw, err := c.DataSource.GetMoneroRawTransaction(txhash)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	writeJSON(w, txRaw, m.GetIndentCtx(r))
}

func (c *appContext) getMoneroTransactionDetail(w http.ResponseWriter, r *http.Request) {
	txhash, err := m.GetTxhashStrCtx(r)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	txDetail, err := c.DataSource.GetMoneroTransaction(txhash)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	writeJSON(w, txDetail, m.GetIndentCtx(r))
}

func (c *appContext) getAvgBlockTime(w http.ResponseWriter, r *http.Request) {
	chartType := "duration-btw-blocks"
	avgBlockTime, _ := c.charts.GetAverageBlockTime(chartType)
	writeJSON(w, avgBlockTime, m.GetIndentCtx(r))
}

func (c *appContext) MutilchainChartTypeData(w http.ResponseWriter, r *http.Request) {
	chainType := chi.URLParam(r, "chaintype")
	if chainType == "" {
		return
	}
	chartType := m.GetChartTypeCtx(r)
	bin := r.URL.Query().Get("bin")
	// Support the deprecated URL parameter "zoom".
	if bin == "" {
		bin = r.URL.Query().Get("zoom")
	}

	mutilchainChartData := c.GetMutilchainChartData(chainType)
	if mutilchainChartData == nil {
		return
	}
	axis := r.URL.Query().Get("axis")
	chartData, err := mutilchainChartData.Chart(chartType, bin, axis)
	if err != nil {
		http.NotFound(w, r)
		log.Warnf(`Error fetching chart %q at bin level '%s': %v`, chartType, bin, err)
		return
	}
	writeJSONBytes(w, chartData)
}

func (c *appContext) GetMutilchainChartData(chainType string) *cache.MutilchainChartData {
	switch chainType {
	case mutilchain.TYPEBTC:
		return c.BtcCharts
	case mutilchain.TYPELTC:
		return c.LtcCharts
	case mutilchain.TYPEXMR:
		return c.XmrCharts
	default:
		return nil
	}
}

func (c *appContext) getExchangeData(w http.ResponseWriter, r *http.Request) {
	type ExchangeStateMap struct {
		ChainType string                       `json:"chain_type"`
		Exchanges []*exchanges.TokenedExchange `json:"exchanges,omitempty"`
	}

	result := make([]ExchangeStateMap, 0)
	//get decred chart state
	if !c.ChainDisabledMap[mutilchain.TYPEDCR] {
		result = append(result, ExchangeStateMap{
			ChainType: mutilchain.TYPEDCR,
			Exchanges: c.xcBot.State().DecredOrderedExchanges(),
		})
	}

	for _, chain := range dbtypes.MutilchainList {
		if c.ChainDisabledMap[chain] {
			continue
		}
		_, orderedExchagnes := c.xcBot.State().MutilchainVolumeOrderedExchanges(chain)
		result = append(result, ExchangeStateMap{
			ChainType: chain,
			Exchanges: orderedExchagnes,
		})
	}
	writeJSON(w, result, m.GetIndentCtx(r))
}

// route: chainchart/market/{token}/candlestick/{bin}
func (c *appContext) getMutilchainCandlestickChart(w http.ResponseWriter, r *http.Request) {
	chainType := chi.URLParam(r, "chaintype")
	if chainType == "" {
		return
	}
	if chainType == mutilchain.TYPEDCR {
		c.getCandlestickChart(w, r)
		return
	}
	if c.xcBot == nil {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	token := m.RetrieveExchangeTokenCtx(r)
	bin := m.RetrieveStickWidthCtx(r)
	if token == "" || bin == "" {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	chart, err := c.xcBot.MutilchainQuickSticks(token, bin, chainType)
	if err != nil {
		apiLog.Infof("QuickSticks error: %v", err)
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	writeJSONBytes(w, chart)
}

// route: /market/{token}/candlestick/{bin}
func (c *appContext) getCandlestickChart(w http.ResponseWriter, r *http.Request) {
	if c.xcBot == nil {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	token := m.RetrieveExchangeTokenCtx(r)
	bin := m.RetrieveStickWidthCtx(r)
	if token == "" || bin == "" {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}

	chart, err := c.xcBot.QuickSticks(token, bin)
	if err != nil {
		apiLog.Infof("QuickSticks error: %v", err)
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	writeJSONBytes(w, chart)
}

func (c *appContext) getMutilchainDepthSubmarketChart(w http.ResponseWriter, r *http.Request) {
	chainType := chi.URLParam(r, "chaintype")
	if chainType == "" {
		return
	}
	if chainType == mutilchain.TYPEDCR {
		c.getDepthChart(w, r)
		return
	}
	if c.xcBot == nil {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	token := m.RetrieveExchangeTokenCtx(r)
	if token == "" {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	chart, err := c.xcBot.MutilchainQuickSubMarketDepth(token, chainType)
	if err != nil {
		apiLog.Infof("QuickDepth error: %v", err)
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	writeJSONBytes(w, chart)
}

func (c *appContext) getMutilchainDepthChart(w http.ResponseWriter, r *http.Request) {
	chainType := chi.URLParam(r, "chaintype")
	if chainType == "" {
		return
	}
	if chainType == mutilchain.TYPEDCR {
		c.getDepthChart(w, r)
		return
	}
	if c.xcBot == nil {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	token := m.RetrieveExchangeTokenCtx(r)
	if token == "" {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}

	chart, err := c.xcBot.MutilchainQuickDepth(token, chainType)
	if err != nil {
		apiLog.Infof("QuickDepth error: %v", err)
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	writeJSONBytes(w, chart)
}

// route: /market/{token}/depth
func (c *appContext) getDepthSubMarketChart(w http.ResponseWriter, r *http.Request) {
	if c.xcBot == nil {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	token := m.RetrieveExchangeTokenCtx(r)
	if token == "" {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}

	chart, err := c.xcBot.QuickSubMarketDepth(token)
	if err != nil {
		apiLog.Infof("QuickDepth error: %v", err)
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	writeJSONBytes(w, chart)
}

// route: /market/{token}/depth
func (c *appContext) getDepthChart(w http.ResponseWriter, r *http.Request) {
	if c.xcBot == nil {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	token := m.RetrieveExchangeTokenCtx(r)
	if token == "" {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}

	chart, err := c.xcBot.QuickDepth(token)
	if err != nil {
		apiLog.Infof("QuickDepth error: %v", err)
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	writeJSONBytes(w, chart)
}

func (c *appContext) getAddressTransactions(w http.ResponseWriter, r *http.Request) {
	addresses, err := m.GetAddressCtx(r, c.Params)
	if err != nil || len(addresses) > 1 {
		http.Error(w, http.StatusText(422), 422)
		return
	}
	address := addresses[0]

	count := int64(m.GetNCtx(r))
	skip := int64(m.GetMCtx(r))
	if count <= 0 {
		count = 10
	} else if count > 8000 {
		count = 8000
	}
	if skip <= 0 {
		skip = 0
	}

	txs, err := c.DataSource.AddressTransactionDetails(address, count, skip, dbtypes.AddrTxnAll)
	if dbtypes.IsTimeoutErr(err) {
		apiLog.Errorf("AddressTransactionDetails: %v", err)
		http.Error(w, "Database timeout.", http.StatusServiceUnavailable)
		return
	}

	if txs == nil || err != nil {
		http.Error(w, http.StatusText(422), 422)
		return
	}
	writeJSON(w, txs, m.GetIndentCtx(r))
}

func (c *appContext) getMultichainDBAddressTransactions(w http.ResponseWriter, r *http.Request) {
	chainType, address := m.GetMultichainAddressCtx(r)
	if chainType == "" || address == "" {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	// this function only support for utxo based blockchain
	if chainType == mutilchain.TYPEXMR {
		apiLog.Errorf("This API not support for %s", chainType)
		http.Error(w, http.StatusText(422), 422)
		return
	}
	count := int64(m.GetNCtx(r))
	skip := int64(m.GetMCtx(r))
	if count <= 0 {
		count = 10
	} else if count > 8000 {
		count = 8000
	}

	if skip <= 0 {
		skip = 0
	}

	addrInfo, err := c.DataSource.MutilchainAPIAddressTransactionDetails(address, chainType, count, skip)
	if dbtypes.IsTimeoutErr(err) {
		apiLog.Errorf("MutilchainAPIAddressTransactionDetails: %v", err)
		http.Error(w, "Database timeout.", http.StatusServiceUnavailable)
		return
	}

	if addrInfo == nil || err != nil {
		http.Error(w, http.StatusText(422), 422)
		return
	}
	writeJSON(w, *addrInfo, m.GetIndentCtx(r))
}

func (c *appContext) getMutilchainAddressTransactions(w http.ResponseWriter, r *http.Request) {
	if c.IsCrawlerUserAgent(r.UserAgent(), externalapi.GetIP(r)) {
		return
	}
	addresses, err := m.GetAddressCtx(r, c.Params)
	if err != nil || len(addresses) > 1 {
		http.Error(w, http.StatusText(422), 422)
		return
	}
	address := addresses[0]
	chainType := chi.URLParam(r, "chaintype")
	if chainType == "" {
		return
	}
	count := int64(m.GetNCtx(r))
	skip := int64(m.GetMCtx(r))
	if count <= 0 {
		count = 10
	} else if count > 8000 {
		count = 8000
	}
	if skip <= 0 {
		skip = 0
	}

	txs, err := c.DataSource.MutilchainAddressTransactionDetails(address, chainType, count, skip)
	if dbtypes.IsTimeoutErr(err) {
		apiLog.Errorf("AddressTransactionDetails: %v", err)
		http.Error(w, "Database timeout.", http.StatusServiceUnavailable)
		return
	}

	if txs == nil || err != nil {
		http.Error(w, http.StatusText(422), 422)
		return
	}
	writeJSON(w, txs, m.GetIndentCtx(r))
}

// getAddressTransactionsRaw handles the various /address/{addr}/.../raw API
// endpoints.
func (c *appContext) getAddressesTxs(w http.ResponseWriter, r *http.Request) {
	addresses := chi.URLParam(r, "addresses")
	if addresses == "" {
		http.Error(w, http.StatusText(422), 422)
		return
	}
	result := make(map[string][]*apitypes.AddressTxRaw)
	//get addresses array
	addressArr := strings.Split(addresses, ",")
	for _, address := range addressArr {
		if strings.TrimSpace(address) == "" {
			continue
		}
		txs := c.DataSource.GetAddressTransactionsRawWithSkip(address, int(10000), int(0))
		if txs == nil || len(txs) < 1 {
			continue
		}
		result[address] = txs
	}
	writeJSON(w, result, m.GetIndentCtx(r))
}

// broadcast tx to network
func (c *appContext) broadcastTx(w http.ResponseWriter, r *http.Request) {
	// Look up any spending transactions for each output of this transaction
	// when the client requests spends with the URL query ?spends=true.
	txhex := ""
	if txhex = r.URL.Query().Get("hex"); txhex == "" {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	txid, err := c.DataSource.SendRawTransaction(txhex)
	if err != nil {
		apiLog.Errorf("Broadcast transaction failed. Error: %v", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	writeJSON(w, txid, m.GetIndentCtx(r))
}

// getAddressTransactionsRaw handles the various /address/{addr}/.../raw API
// endpoints.
func (c *appContext) getAddressTransactionsRaw(w http.ResponseWriter, r *http.Request) {
	addresses, err := m.GetAddressCtx(r, c.Params)
	if err != nil || len(addresses) > 1 {
		http.Error(w, http.StatusText(422), 422)
		return
	}
	address := addresses[0]

	count := int64(m.GetNCtx(r))
	skip := int64(m.GetMCtx(r))
	if count <= 0 {
		count = 10
	} else if count > 1000 {
		count = 1000
	}
	if skip <= 0 {
		skip = 0
	}

	txs := c.DataSource.GetAddressTransactionsRawWithSkip(address, int(count), int(skip))
	if txs == nil {
		http.Error(w, http.StatusText(422), 422)
		return
	}

	writeJSON(w, txs, m.GetIndentCtx(r))
}

// getAgendaData processes a request for agenda chart data from /agenda/{agendaId}.
func (c *appContext) getAgendaData(w http.ResponseWriter, r *http.Request) {
	agendaID := m.GetAgendaIdCtx(r)
	if agendaID == "" {
		http.Error(w, http.StatusText(422), 422)
		return
	}
	chartDataByTime, err := c.DataSource.AgendaVotes(agendaID, 0)
	if dbtypes.IsTimeoutErr(err) {
		apiLog.Errorf("AgendaVotes timeout error %v", err)
		http.Error(w, "Database timeout.", http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}

	chartDataByHeight, err := c.DataSource.AgendaVotes(agendaID, 1)
	if dbtypes.IsTimeoutErr(err) {
		apiLog.Errorf("AgendaVotes timeout error: %v", err)
		http.Error(w, "Database timeout.", http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}

	data := &apitypes.AgendaAPIResponse{
		ByHeight: chartDataByHeight,
		ByTime:   chartDataByTime,
	}

	writeJSON(w, data, "")
}

func (c *appContext) getTSpendVoteChartData(w http.ResponseWriter, r *http.Request) {
	txHash := m.GetTspendTxIdCtx(r)
	if txHash == "" {
		http.Error(w, http.StatusText(422), 422)
		return
	}
	chartDataByTime, err := c.DataSource.TSpendTransactionVotes(txHash, 0)
	if dbtypes.IsTimeoutErr(err) {
		apiLog.Errorf("TSpendTransactionVotes timeout error %v", err)
		http.Error(w, "Database timeout.", http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}

	chartDataByHeight, err := c.DataSource.TSpendTransactionVotes(txHash, 1)
	if dbtypes.IsTimeoutErr(err) {
		apiLog.Errorf("TSpendTransactionVotes timeout error: %v", err)
		http.Error(w, "Database timeout.", http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}

	data := &apitypes.AgendaAPIResponse{
		ByHeight: chartDataByHeight,
		ByTime:   chartDataByTime,
	}

	writeJSON(w, data, "")
}

func (c *appContext) getExchanges(w http.ResponseWriter, r *http.Request) {
	if c.xcBot == nil {
		http.Error(w, "Exchange monitoring disabled.", http.StatusServiceUnavailable)
		return
	}
	// Don't provide any info if the bot is in the failed state.
	if c.xcBot.IsFailed() {
		http.Error(w, "No exchange data available", http.StatusNotFound)
		return
	}

	code := r.URL.Query().Get("code")
	var state *exchanges.ExchangeBotState
	if code != "" && code != c.xcBot.BtcIndex {
		var err error
		state, err = c.xcBot.ConvertedState(code)
		if err != nil {
			http.Error(w, fmt.Sprintf("No exchange data for code %q", html.EscapeString(code)), http.StatusNotFound)
			return
		}
	} else {
		state = c.xcBot.State()
	}
	writeJSON(w, state, m.GetIndentCtx(r))
}

func (c *appContext) getExchangeRates(w http.ResponseWriter, r *http.Request) {
	if c.xcBot == nil {
		http.Error(w, "Exchange rate monitoring disabled.", http.StatusServiceUnavailable)
		return
	}
	// Don't provide any info if the bot is in the failed state.
	if c.xcBot.IsFailed() {
		http.Error(w, "No exchange rate data available", http.StatusNotFound)
		return
	}

	code := r.URL.Query().Get("code")
	var rates *exchanges.ExchangeRates
	if code != "" && code != c.xcBot.BtcIndex {
		var err error
		rates, err = c.xcBot.ConvertedRates(code)
		if err != nil {
			http.Error(w, fmt.Sprintf("No exchange rate data for code %q", html.EscapeString(code)), http.StatusNotFound)
			return
		}
	} else {
		rates = c.xcBot.Rates()
	}

	writeJSON(w, rates, m.GetIndentCtx(r))
}

func (c *appContext) getCurrencyCodes(w http.ResponseWriter, r *http.Request) {
	if c.xcBot == nil {
		http.Error(w, "Exchange monitoring disabled.", http.StatusServiceUnavailable)
		return
	}

	codes := c.xcBot.AvailableIndices()
	if len(codes) == 0 {
		http.Error(w, "No codes found.", http.StatusNotFound)
		return
	}
	writeJSON(w, codes, m.GetIndentCtx(r))
}

// getAgendasData returns high level agendas details that includes Name,
// Description, Vote Version, VotingDone height, Activated, HardForked,
// StartTime and ExpireTime.
func (c *appContext) getAgendasData(w http.ResponseWriter, _ *http.Request) {
	agendas, err := c.AgendaDB.AllAgendas()
	if err != nil {
		apiLog.Errorf("agendadb AllAgendas error: %v", err)
		http.Error(w, "agendadb.AllAgendas failed.", http.StatusServiceUnavailable)
		return
	}

	voteMilestones, err := c.DataSource.AllAgendas()
	if err != nil {
		apiLog.Errorf("AllAgendas timeout error: %v", err)
		http.Error(w, "Database timeout.", http.StatusServiceUnavailable)
	}

	data := make([]apitypes.AgendasInfo, 0, len(agendas))
	for index := range agendas {
		val := agendas[index]
		agendaMilestone := voteMilestones[val.ID]
		agendaMilestone.StartTime = time.Unix(int64(val.StartTime), 0).UTC()
		agendaMilestone.ExpireTime = time.Unix(int64(val.ExpireTime), 0).UTC()

		data = append(data, apitypes.AgendasInfo{
			Name:        val.ID,
			Description: val.Description,
			VoteVersion: val.VoteVersion,
			MileStone:   &agendaMilestone,
			Mask:        val.Mask,
		})
	}
	writeJSON(w, data, "")
}

func (c *appContext) StakeVersionLatestCtx(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := m.StakeVersionLatestCtx(r, c.DataSource.GetStakeVersionsLatest)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (c *appContext) BlockHashPathAndIndexCtx(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := m.BlockHashPathAndIndexCtx(r, c.DataSource)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (c *appContext) BlockIndexLatestCtx(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := m.BlockIndexLatestCtx(r, c.DataSource)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (c *appContext) getBlockHeightCtx(r *http.Request) (int64, error) {
	return m.GetBlockHeightCtx(r, c.DataSource)
}

func (c *appContext) getBlockHashCtx(r *http.Request) (string, error) {
	hash, err := m.GetBlockHashCtx(r)
	if err != nil {
		idx := int64(m.GetBlockIndexCtx(r))
		hash, err = c.DataSource.GetBlockHash(idx)
		if err != nil {
			apiLog.Errorf("Unable to GetBlockHash: %v", err)
			return "", err
		}
	}
	return hash, nil
}

func (c *appContext) IsCrawlerUserAgent(userAgent, ip string) bool {
	if strings.Contains(userAgent, "facebookexternalhit") {
		return true
	}
	//check isCrawler
	if crawlerdetect.IsCrawler(userAgent) {
		return true
	}
	return agents.IsCrawler(userAgent)
}

// IsCrawlerUserAgent return if is crawler user agent
func (c *appContext) IsCrawlerUserAgentAdvance(userAgent, ip string) bool {
	if strings.Contains(userAgent, "facebookexternalhit") {
		return true
	}
	//check isCrawler
	if crawlerdetect.IsCrawler(userAgent) {
		return true
	}
	isCrawler := agents.IsCrawler(userAgent)
	if isCrawler {
		return true
	}
	ipRange := utils.GetIPRange(ip)
	// check if is on black list
	inBlackList, err := c.DataSource.CheckIPRangeOnBlackList(ipRange)
	if err != nil || inBlackList {
		return true
	}
	now := uint64(time.Now().Unix())
	// remove all agent has duration >= 15s
	remainList := make([]*externalapi.IPRangeAccessData, 0)
	for _, ipRangeAccessData := range externalapi.AccessDataIPRanges {
		// check duration with last time
		duration := now - ipRangeAccessData.LastTime
		if duration >= 15 {
			continue
		}
		remainList = append(remainList, ipRangeAccessData)
	}
	externalapi.AccessDataIPRanges = remainList
	// count on temp agents
	// check exist on TempAgent
	var handlerAgent *externalapi.IPRangeAccessData
	var existIndex int
	for index, iprangeAccess := range externalapi.AccessDataIPRanges {
		if iprangeAccess.IpRange == ipRange {
			handlerAgent = iprangeAccess
			existIndex = index
			break
		}
	}
	// if not exist on temp list
	if handlerAgent == nil {
		externalapi.AccessDataIPRanges = append(externalapi.AccessDataIPRanges, &externalapi.IPRangeAccessData{
			IpRange:  ipRange,
			GetCount: 1,
			Duration: 0,
			LastTime: now,
		})
		return false
	}
	// if exist on temp list
	handlerAgent.GetCount++
	handlerAgent.Duration += now - handlerAgent.LastTime
	handlerAgent.LastTime = now
	externalapi.AccessDataIPRanges[existIndex] = handlerAgent
	// if access count is 8 times in about 15s, add to black list
	if handlerAgent.GetCount >= 8 {
		// remove from temp agents
		externalapi.AccessDataIPRanges = append(externalapi.AccessDataIPRanges[:existIndex], externalapi.AccessDataIPRanges[existIndex+1:]...)
		if handlerAgent.Duration < 15 {
			// add to blacklist
			err := c.DataSource.InsertIPRangeToBlackList(ipRange, "Too many visits in a short period of time")
			if err != nil {
				log.Errorf("Add agent to black list failed, ipRange: %s", ipRange)
				return true
			}
			log.Warnf("Added ip range: %s to black list", ipRange)
			return true
		}
	}
	return false
}
