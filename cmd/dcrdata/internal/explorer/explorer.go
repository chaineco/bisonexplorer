// Copyright (c) 2018-2022, The Decred developers
// Copyright (c) 2017, The dcrdata developers
// See LICENSE for details.

// Package explorer handles the block explorer subsystem for generating the
// explorer pages.
package explorer

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/signal"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/btcutil"
	btcchaincfg "github.com/btcsuite/btcd/chaincfg"
	btcwire "github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/blockchain/stake/v5"
	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrd/dcrutil/v4"
	chainjson "github.com/decred/dcrd/rpc/jsonrpc/types/v4"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrdata/exchanges/v3"
	"github.com/decred/dcrdata/gov/v6/agendas"
	pitypes "github.com/decred/dcrdata/gov/v6/politeia/types"
	"github.com/decred/dcrdata/v8/blockdata"
	"github.com/decred/dcrdata/v8/blockdata/blockdatabtc"
	"github.com/decred/dcrdata/v8/blockdata/blockdataltc"
	"github.com/decred/dcrdata/v8/db/cache"
	"github.com/decred/dcrdata/v8/db/dbtypes"
	"github.com/decred/dcrdata/v8/explorer/types"
	"github.com/decred/dcrdata/v8/mempool"
	"github.com/decred/dcrdata/v8/mutilchain"
	"github.com/decred/dcrdata/v8/mutilchain/externalapi"
	pstypes "github.com/decred/dcrdata/v8/pubsub/types"
	"github.com/decred/dcrdata/v8/txhelpers"
	humanize "github.com/dustin/go-humanize"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	ltcjson "github.com/ltcsuite/ltcd/btcjson"
	ltcchaincfg "github.com/ltcsuite/ltcd/chaincfg"
	"github.com/ltcsuite/ltcd/ltcutil"
	ltcwire "github.com/ltcsuite/ltcd/wire"
	"github.com/rs/cors"
)

const (
	// maxExplorerRows and minExplorerRows are the limits on the number of
	// blocks/time-window rows that may be shown on the explorer pages.
	maxExplorerRows = 400
	minExplorerRows = 10

	// syncStatusInterval is the frequency with startup synchronization progress
	// signals are sent to websocket clients.
	syncStatusInterval = 2 * time.Second

	// defaultAddressRows is the default number of rows to be shown on the
	// address page table.
	defaultAddressRows int64 = 20

	// MaxAddressRows is an upper limit on the number of rows that may be shown
	// on the address page table.
	MaxAddressRows int64 = 160

	MaxTreasuryRows int64 = 200

	testnetNetName = "Testnet"
)

// explorerDataSource implements extra data retrieval functions that require a
// faster solution than RPC, or additional functionality.
type explorerDataSource interface {
	BlockHeight(hash string) (int64, error)
	MutilchainBlockHeight(hash string, chainType string) (int64, error)
	Height() int64
	MutilchainHeight(chainType string) int64
	HeightDB() (int64, error)
	MutilchainHeightDB(chainType string) (int64, error)
	BlockHash(height int64) (string, error)
	SpendingTransaction(fundingTx string, vout uint32) (string, uint32, int8, error)
	MutilchainSpendingTransaction(fundingTxID string, fundingTxVout uint32, chainType string) (string, uint32, error)
	SpendingTransactions(fundingTxID string) ([]string, []uint32, []uint32, error)
	MutilchainSpendingTransactions(fundingTxID string, chainType string) ([]string, []uint32, []uint32, error)
	PoolStatusForTicket(txid string) (dbtypes.TicketSpendType, dbtypes.TicketPoolStatus, error)
	TreasuryBalance() (*dbtypes.TreasuryBalance, error)
	TreasuryBalanceWithPeriod(year int64, month int64) (*dbtypes.TreasuryBalance, error)
	TreasuryTxns(n, offset int64, txType stake.TxType) ([]*dbtypes.TreasuryTx, error)
	TreasuryTxnsWithPeriod(n, offset int64, txType stake.TxType, year int64, month int64) ([]*dbtypes.TreasuryTx, error)
	GetAtomicSwapList(n, offset int64, pair, status, searchKey string) ([]*dbtypes.AtomicSwapFullData, int64, error)
	CountRefundContract() (int64, error)
	AddressHistory(address string, N, offset int64, txnType dbtypes.AddrTxnViewType, year int64, month int64) ([]*dbtypes.AddressRow, *dbtypes.AddressBalance, error)
	MutilchainAddressHistory(address string, N, offset int64, txnType dbtypes.AddrTxnViewType, chainType string) ([]*dbtypes.MutilchainAddressRow, *dbtypes.AddressBalance, error)
	AddressData(address string, N, offset int64, txnType dbtypes.AddrTxnViewType, year int64, month int64) (*dbtypes.AddressInfo, error)
	MutilchainAddressData(address string, N, offset int64, txnType dbtypes.AddrTxnViewType, chainType string) (*dbtypes.AddressInfo, error)
	DevBalance() (*dbtypes.AddressBalance, error)
	FillAddressTransactions(addrInfo *dbtypes.AddressInfo) error
	BlockMissedVotes(blockHash string) ([]string, error)
	TicketMiss(ticketHash string) (string, int64, error)
	SideChainBlocks() ([]*dbtypes.BlockStatus, error)
	DisapprovedBlocks() ([]*dbtypes.BlockStatus, error)
	BlockStatus(hash string) (dbtypes.BlockStatus, error)
	BlockStatuses(height int64) ([]*dbtypes.BlockStatus, error)
	BlockFlags(hash string) (bool, bool, error)
	TicketPoolVisualization(interval dbtypes.TimeBasedGrouping) (*dbtypes.PoolTicketsData, *dbtypes.PoolTicketsData, *dbtypes.PoolTicketsData, int64, error)
	TransactionBlocks(hash string) ([]*dbtypes.BlockStatus, []uint32, error)
	MutilchainTransactionBlocks(hash string, chainType string) ([]*dbtypes.BlockStatus, []uint32, error)
	Transaction(txHash string) ([]*dbtypes.Tx, error)
	MutilchainTransaction(txHash string, chainType string) ([]*dbtypes.Tx, error)
	VinsForTx(*dbtypes.Tx) (vins []dbtypes.VinTxProperty, prevPkScripts []string, scriptVersions []uint16, err error)
	MutilchainVinsForTx(tx *dbtypes.Tx, chainType string) (vins []dbtypes.VinTxProperty, prevPkScripts []string, scriptVersions []uint16, err error)
	VoutsForTx(*dbtypes.Tx) ([]dbtypes.Vout, error)
	MutilchainVoutsForTx(*dbtypes.Tx, string) ([]dbtypes.Vout, error)
	PosIntervals(limit, offset uint64) ([]*dbtypes.BlocksGroupedInfo, error)
	TimeBasedIntervals(timeGrouping dbtypes.TimeBasedGrouping, limit, offset uint64) ([]*dbtypes.BlocksGroupedInfo, error)
	AgendasVotesSummary(agendaID string) (summary *dbtypes.AgendaSummary, err error)
	BlockTimeByHeight(height int64) (int64, error)
	GetChainParams() *chaincfg.Params
	GetBTCChainParams() *btcchaincfg.Params
	GetLTCChainParams() *ltcchaincfg.Params
	GetExplorerBlock(hash string) *types.BlockInfo
	GetMutilchainExplorerBlock(hash, chainType string) *types.BlockInfo
	GetBTCExplorerBlock(hash string) *types.BlockInfo
	GetLTCExplorerBlock(hash string) *types.BlockInfo
	GetExplorerBlocks(start int, end int) []*types.BlockBasic
	GetLTCExplorerBlocks(start int, end int) []*types.BlockBasic
	GetBTCExplorerBlocks(start int, end int) []*types.BlockBasic
	GetBlockHeight(hash string) (int64, error)
	GetBlockHash(idx int64) (string, error)
	GetMutilchainBlockHash(idx int64, chainType string) (string, error)
	GetDaemonMutilchainBlockHash(idx int64, chainType string) (string, error)
	GetExplorerTx(txid string) *types.TxInfo
	GetMutilchainExplorerTx(txid string, chainType string) *types.TxInfo
	GetTip() (*types.WebBasicBlock, error)
	DecodeRawTransaction(txhex string) (*chainjson.TxRawResult, error)
	SendRawTransaction(txhex string) (string, error)
	GetTransactionByHash(txid string) (*wire.MsgTx, error)
	GetLTCTransactionByHash(txid string) (*ltcutil.Tx, error)
	GetBTCTransactionByHash(txid string) (*btcutil.Tx, error)
	GetHeight() (int64, error)
	GetMutilchainHeight(chainType string) (int64, error)
	TxHeight(txid *chainhash.Hash) (height int64)
	DCP0010ActivationHeight() int64
	DCP0011ActivationHeight() int64
	DCP0012ActivationHeight() int64
	BlockSubsidy(height int64, voters uint16) *chainjson.GetBlockSubsidyResult
	GetExplorerFullBlocks(start int, end int) []*types.BlockInfo
	GetMutilchainExplorerFullBlocks(chainType string, start, end int) []*types.BlockInfo
	CurrentDifficulty() (float64, error)
	Difficulty(timestamp int64) float64
	MutilchainDifficulty(timestamp int64, chainType string) float64
	GetNeededSyncProposalTokens(tokens []string) (syncTokens []string, err error)
	AddProposalMeta(proposalMetaData []map[string]string) (err error)
	MutilchainGetTransactionCount(chainType string) int64
	MutilchainGetTotalVoutsCount(chainType string) int64
	MutilchainGetTotalAddressesCount(chainType string) int64
	MutilchainGetBlockchainInfo(chainType string) (*mutilchain.BlockchainInfo, error)
	MutilchainValidBlockhash(hash string, chainType string) bool
	MutilchainValidTxhash(hash string, chainType string) bool
	MutilchainBestBlockTime(chainType string) int64
	GetDecredBlockchainSize() int64
	GetDecredTotalTransactions() int64
	SyncLast20LTCBlocks(nodeHeight int32) error
	SyncLast20BTCBlocks(nodeHeight int32) error
	GetDBBlockDetailInfo(chainType string, height int64) *dbtypes.MutilchainDBBlockInfo
	SyncAndGet24hMetricsInfo(bestBlockHeight int64, chainType string) (*dbtypes.Block24hInfo, error)
	SyncAddressSummary() error
	SyncTreasurySummary() error
	GetMutilchainMempoolTxTime(txid string, chainType string) int64
	GetPeerCount() (int, error)
	GetBlockchainSummaryInfo() (addrCount, outputs int64, err error)
	GetAtomicSwapSummary() (txCount, amount, oldestContract int64, err error)
	GetSwapFullData(txid, swapType string) ([]*dbtypes.AtomicSwapFullData, error)
	GetSwapType(txid string) string
	GetMultichainSwapType(txid, chainType string) (string, error)
	GetMultichainSwapFullData(txid, swapType, chainType string) (*dbtypes.AtomicSwapFullData, string, error)
	GetMutilchainVoutIndexsOfContract(contractTx, chainType string) ([]int, error)
	GetMutilchainVinIndexsOfRedeem(spendTx, chainType string) ([]int, error)
	GetLast5PoolDataList() ([]*dbtypes.PoolDataItem, error)
	GetExplorerBlockBasic(height int) *types.BlockBasic
	GetAvgBlockFormattedSize() (string, error)
	GetBwDashData() (int64, int64, int64)
	GetAvgTxFee() (int64, error)
	GetTicketsSummaryInfo() (*dbtypes.TicketsSummaryInfo, error)
	Get24hActiveAddressesCount() int64
	Get24hStakingInfo() (poolvalue, missed int64, err error)
	Get24hTreasuryBalanceChange() (treasuryBalanceChange int64, err error)
	InsertToBlackList(agent, ip, note string) error
	CheckOnBlackList(agent, ip string) (bool, error)
	GetBlockSwapGroupFullData(blockTxs []string) ([]*dbtypes.AtomicSwapFullData, error)
	GetLastMultichainPoolDataList(chainType string, startHeight int64) ([]*dbtypes.MultichainPoolDataItem, error)
	GetMultichainStats(chainType string) (*externalapi.ChainStatsData, error)
}

type PoliteiaBackend interface {
	ProposalsLastSync() int64
	ProposalsSync() error
	ProposalsAll(offset, rowsCount int, filterByVoteStatus ...int) ([]*pitypes.ProposalRecord, int, error)
	CountByStatus(countAll bool, filterByVoteStatus ...int) int
	CountProposals(votesStatus map[string]string) map[string]string
	ProposalByToken(token string) (*pitypes.ProposalRecord, error)
	ProposalsApprovedMetadata(tokens []string, proposalList []*pitypes.ProposalRecord) ([]map[string]string, error)
	GetAllProposals() ([]*pitypes.ProposalRecord, error)
}

// agendaBackend implements methods that manage agendas db data.
type agendaBackend interface {
	AgendaInfo(agendaID string) (*agendas.AgendaTagged, error)
	AllAgendas() (agendas []*agendas.AgendaTagged, err error)
	UpdateAgendas() error
}

// ChartDataSource provides data from the charts cache.
type ChartDataSource interface {
	AnonymitySet() uint64
}

type AgendaDetail struct {
	*agendas.AgendaTagged
	Title             string
	DescriptionDetail string
	ApprovalRate      float64
}

// links to be passed with common page data.
type links struct {
	CoinbaseComment string
	POSExplanation  string
	APIDocs         string
	InsightAPIDocs  string
	Github          string
	License         string
	NetParams       string
	DownloadLink    string
	// Testnet and below are set via dcrdata config.
	Testnet       string
	Mainnet       string
	TestnetSearch string
	MainnetSearch string
	OnionURL      string
}

var explorerLinks = &links{
	CoinbaseComment: "https://github.com/decred/dcrd/blob/2a18beb4d56fe59d614a7309308d84891a0cba96/chaincfg/genesis.go#L17-L53",
	POSExplanation:  "https://docs.decred.org/proof-of-stake/overview/",
	APIDocs:         "https://github.com/decred/dcrdata#apis",
	InsightAPIDocs:  "https://github.com/decred/dcrdata/blob/master/docs/Insight_API_documentation.md",
	Github:          "https://github.com/decred/dcrdata",
	License:         "https://github.com/decred/dcrdata/blob/master/LICENSE",
	NetParams:       "https://github.com/decred/dcrd/blob/master/chaincfg/params.go",
	DownloadLink:    "https://decred.org/wallets/",
}

// TicketStatusText generates the text to display on the explorer's transaction
// page for the "POOL STATUS" field.
func TicketStatusText(s dbtypes.TicketSpendType, p dbtypes.TicketPoolStatus) string {
	switch p {
	case dbtypes.PoolStatusLive:
		return "In Live Ticket Pool"
	case dbtypes.PoolStatusVoted:
		return "Voted"
	case dbtypes.PoolStatusExpired:
		switch s {
		case dbtypes.TicketUnspent:
			return "Expired, Unrevoked"
		case dbtypes.TicketRevoked:
			return "Expired, Revoked"
		default:
			return "invalid ticket state"
		}
	case dbtypes.PoolStatusMissed:
		switch s {
		case dbtypes.TicketUnspent:
			return "Missed, Unrevoked"
		case dbtypes.TicketRevoked:
			return "Missed, Revoked"
		default:
			return "invalid ticket state"
		}
	default:
		return "Immature"
	}
}

type pageData struct {
	sync.RWMutex
	BlockInfo      *types.BlockInfo
	BlockchainInfo *chainjson.GetBlockChainInfoResult
	HomeInfo       *types.HomeInfo
	SummaryInfo    *types.SummaryInfo
	Block24hInfo   *dbtypes.Block24hInfo
	Syncing24h     bool
}

type BtcPageData struct {
	sync.RWMutex
	BlockInfo      *types.BlockInfo
	BlockDetails   []*types.BlockInfo
	BlockchainInfo *btcjson.GetBlockChainInfoResult
	HomeInfo       *types.HomeInfo
	Syncing24h     bool
}

type LtcPageData struct {
	sync.RWMutex
	BlockInfo      *types.BlockInfo
	BlockDetails   []*types.BlockInfo
	BlockchainInfo *ltcjson.GetBlockChainInfoResult
	HomeInfo       *types.HomeInfo
	Syncing24h     bool
}

type ExplorerUI struct {
	Mux              *chi.Mux
	dataSource       explorerDataSource
	chartSource      ChartDataSource
	BtcChartSource   *cache.MutilchainChartData
	LtcChartSource   *cache.MutilchainChartData
	agendasSource    agendaBackend
	voteTracker      *agendas.VoteTracker
	proposals        PoliteiaBackend
	dbsSyncing       atomic.Value
	devPrefetch      bool
	templates        templates
	WsHub            *WebsocketHub
	pageData         *pageData
	BtcPageData      *BtcPageData
	LtcPageData      *LtcPageData
	ChainParams      *chaincfg.Params
	BtcChainParams   *btcchaincfg.Params
	LtcChainParams   *ltcchaincfg.Params
	ChainDisabledMap map[string]bool
	Version          string
	NetName          string
	MeanVotingBlocks int64
	xcBot            *exchanges.ExchangeBot
	xcDone           chan struct{}
	// displaySyncStatusPage indicates if the sync status page is the only web
	// page that should be accessible during DB synchronization.
	displaySyncStatusPage atomic.Value
	politeiaURL           string

	invsMtx         sync.RWMutex
	invs            *types.MempoolInfo
	LtcMempoolInfo  *types.MutilchainMempoolInfo
	BtcMempoolInfo  *types.MutilchainMempoolInfo
	premine         int64
	CoinCaps        []string
	CoinCapDataList []*dbtypes.MarketCapData
}

// AreDBsSyncing is a thread-safe way to fetch the boolean in dbsSyncing.
func (exp *ExplorerUI) AreDBsSyncing() bool {
	syncing, ok := exp.dbsSyncing.Load().(bool)
	return ok && syncing
}

// SetDBsSyncing is a thread-safe way to update dbsSyncing.
func (exp *ExplorerUI) SetDBsSyncing(syncing bool) {
	exp.dbsSyncing.Store(syncing)
	exp.WsHub.SetDBsSyncing(syncing)
}

func (exp *ExplorerUI) reloadTemplates() error {
	return exp.templates.reloadTemplates()
}

// See reloadsig*.go for an exported method
func (exp *ExplorerUI) reloadTemplatesSig(sig os.Signal) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, sig)

	go func() {
		for {
			sigr := <-sigChan
			log.Infof("Received %s", sig)
			if sigr == sig {
				if err := exp.reloadTemplates(); err != nil {
					log.Error(err)
					continue
				}
				log.Infof("Explorer UI html templates reparsed.")
			}
		}
	}()
}

// StopWebsocketHub stops the websocket hub
func (exp *ExplorerUI) StopWebsocketHub() {
	if exp == nil {
		return
	}
	log.Info("Stopping websocket hub.")
	exp.WsHub.Stop()
	close(exp.xcDone)
}

// ExplorerConfig is the configuration settings for explorerUI.
type ExplorerConfig struct {
	DataSource       explorerDataSource
	ChartSource      ChartDataSource
	UseRealIP        bool
	AppVersion       string
	DevPrefetch      bool
	Viewsfolder      string
	XcBot            *exchanges.ExchangeBot
	AgendasSource    agendaBackend
	Tracker          *agendas.VoteTracker
	Proposals        PoliteiaBackend
	PoliteiaURL      string
	MainnetLink      string
	TestnetLink      string
	OnionAddress     string
	ReloadHTML       bool
	ChainDisabledMap map[string]bool
	CoinCaps         []string
}

// New returns an initialized instance of explorerUI
func New(cfg *ExplorerConfig) *ExplorerUI {
	exp := new(ExplorerUI)
	exp.Mux = chi.NewRouter()
	exp.dataSource = cfg.DataSource
	exp.chartSource = cfg.ChartSource
	// Allocate Mempool fields.
	exp.invs = new(types.MempoolInfo)
	exp.LtcMempoolInfo = new(types.MutilchainMempoolInfo)
	exp.BtcMempoolInfo = new(types.MutilchainMempoolInfo)
	exp.Version = cfg.AppVersion
	exp.devPrefetch = cfg.DevPrefetch
	exp.xcBot = cfg.XcBot
	exp.xcDone = make(chan struct{})
	exp.agendasSource = cfg.AgendasSource
	exp.voteTracker = cfg.Tracker
	exp.proposals = cfg.Proposals
	exp.politeiaURL = cfg.PoliteiaURL
	exp.ChainDisabledMap = cfg.ChainDisabledMap
	exp.CoinCaps = cfg.CoinCaps
	explorerLinks.Mainnet = cfg.MainnetLink
	explorerLinks.Testnet = cfg.TestnetLink
	explorerLinks.MainnetSearch = cfg.MainnetLink + "search?search="
	explorerLinks.TestnetSearch = cfg.TestnetLink + "search?search="
	if cfg.OnionAddress != "" {
		explorerLinks.OnionURL = fmt.Sprintf("http://%s/", cfg.OnionAddress)
	}

	// explorerDataSource is an interface that could have a value of pointer type.
	if exp.dataSource == nil || reflect.ValueOf(exp.dataSource).IsNil() {
		log.Errorf("An explorerDataSource (PostgreSQL backend) is required.")
		return nil
	}

	if cfg.UseRealIP {
		exp.Mux.Use(middleware.RealIP)
	}

	params := exp.dataSource.GetChainParams()
	btcParams := exp.dataSource.GetBTCChainParams()
	ltcParams := exp.dataSource.GetLTCChainParams()
	exp.ChainParams = params
	exp.BtcChainParams = btcParams
	exp.LtcChainParams = ltcParams
	exp.NetName = netName(exp.ChainParams)
	exp.MeanVotingBlocks = txhelpers.CalcMeanVotingBlocks(params)
	exp.premine = params.BlockOneSubsidy()

	// Development subsidy address of the current network
	devSubsidyAddress, err := dbtypes.DevSubsidyAddress(params)
	if err != nil {
		log.Warnf("explorer.New: %v", err)
	}
	log.Debugf("Organization address: %s", devSubsidyAddress)

	exp.pageData = &pageData{
		BlockInfo: new(types.BlockInfo),
		HomeInfo: &types.HomeInfo{
			DevAddress: devSubsidyAddress,
			Params: types.ChainParams{
				WindowSize:       exp.ChainParams.StakeDiffWindowSize,
				RewardWindowSize: exp.ChainParams.SubsidyReductionInterval,
				BlockTime:        exp.ChainParams.TargetTimePerBlock.Nanoseconds(),
				MeanVotingBlocks: exp.MeanVotingBlocks,
			},
			TreasuryBalance: &dbtypes.TreasuryBalance{},
			PoolInfo: types.TicketPoolInfo{
				Target: uint32(exp.ChainParams.TicketPoolSize) * uint32(exp.ChainParams.TicketsPerBlock),
			},
		},
		SummaryInfo:  &types.SummaryInfo{},
		Block24hInfo: &dbtypes.Block24hInfo{},
	}

	exp.BtcPageData = &BtcPageData{
		BlockInfo: new(types.BlockInfo),
		HomeInfo: &types.HomeInfo{
			Params: types.ChainParams{
				RewardWindowSize: int64(exp.BtcChainParams.SubsidyReductionInterval),
				BlockTime:        exp.BtcChainParams.TargetTimePerBlock.Nanoseconds(),
			},
		},
	}

	exp.LtcPageData = &LtcPageData{
		BlockInfo: new(types.BlockInfo),
		HomeInfo: &types.HomeInfo{
			Params: types.ChainParams{
				RewardWindowSize: int64(exp.LtcChainParams.SubsidyReductionInterval),
				BlockTime:        exp.LtcChainParams.TargetTimePerBlock.Nanoseconds(),
			},
		},
	}

	log.Infof("Mean Voting Blocks calculated: %d", exp.pageData.HomeInfo.Params.MeanVotingBlocks)

	commonTemplates := []string{"extras"}
	exp.templates = newTemplates(cfg.Viewsfolder, cfg.ReloadHTML, commonTemplates, makeTemplateFuncMap(exp.ChainParams))

	tmpls := []string{"home", "decred_home", "blocks", "mempool", "block", "tx", "address",
		"rawtx", "status", "parameters", "agenda", "agendas", "charts",
		"sidechains", "disapproved", "ticketpool", "visualblocks", "statistics",
		"windows", "timelisting", "addresstable", "proposals", "proposal",
		"market", "insight_root", "attackcost", "treasury", "treasurytable",
		"verify_message", "stakingreward", "finance_report", "finance_detail",
		"home_report", "chain_home", "chain_blocks", "chain_block", "chain_tx",
		"chain_address", "chain_mempool", "chain_charts", "chain_market",
		"chain_addresstable", "supply", "marketlist", "chain_parameters",
		"whatsnew", "chain_visualblocks", "bwdash", "atomicswaps", "atomicswaps_table",
		"about"}

	for _, name := range tmpls {
		if err := exp.templates.addTemplate(name); err != nil {
			log.Errorf("Unable to create new html template: %v", err)
			return nil
		}
	}

	exp.addRoutes()

	exp.WsHub = NewWebsocketHub()

	go exp.WsHub.run()

	go exp.watchExchanges()

	return exp
}

// Height returns the height of the current block data.
func (exp *ExplorerUI) Height() int64 {
	exp.pageData.RLock()
	defer exp.pageData.RUnlock()

	if exp.pageData.BlockInfo.BlockBasic == nil {
		// If exp.pageData.BlockInfo.BlockBasic has not yet been set return:
		return -1
	}

	return exp.pageData.BlockInfo.Height
}

// LastBlock returns the last block hash, height and time.
func (exp *ExplorerUI) LastBlock() (lastBlockHash string, lastBlock int64, lastBlockTime int64) {
	exp.pageData.RLock()
	defer exp.pageData.RUnlock()

	if exp.pageData.BlockInfo.BlockBasic == nil {
		// If exp.pageData.BlockInfo.BlockBasic has not yet been set return:
		lastBlock, lastBlockTime = -1, -1
		return
	}

	lastBlock = exp.pageData.BlockInfo.Height
	lastBlockTime = exp.pageData.BlockInfo.BlockTime.UNIX()
	lastBlockHash = exp.pageData.BlockInfo.Hash
	return
}

// MempoolInventory safely retrieves the current mempool inventory.
func (exp *ExplorerUI) MempoolInventory() *types.MempoolInfo {
	exp.invsMtx.RLock()
	defer exp.invsMtx.RUnlock()
	return exp.invs
}

func (exp *ExplorerUI) MutilchainMempoolInfo(chainType string) *types.MutilchainMempoolInfo {
	exp.invsMtx.RLock()
	defer exp.invsMtx.RUnlock()
	switch chainType {
	case mutilchain.TYPEBTC:
		return exp.BtcMempoolInfo
	case mutilchain.TYPELTC:
		return exp.LtcMempoolInfo
	default:
		return nil
	}
}

// MempoolID safely fetches the current mempool inventory ID.
func (exp *ExplorerUI) MempoolID() uint64 {
	exp.invsMtx.RLock()
	defer exp.invsMtx.RUnlock()
	return exp.invs.ID()
}

// MempoolSignal returns the mempool signal channel, which is to be used by the
// mempool package's MempoolMonitor as a send-only channel.
func (exp *ExplorerUI) MempoolSignal() chan<- pstypes.HubMessage {
	return exp.WsHub.HubRelay
}

// StoreMPData stores mempool data. It is advisable to pass a copy of the
// []types.MempoolTx so that it may be modified (e.g. sorted) without affecting
// other MempoolDataSavers.
func (exp *ExplorerUI) StoreMPData(_ *mempool.StakeData, _ []types.MempoolTx, inv *types.MempoolInfo) {
	// Get exclusive access to the Mempool field.
	exp.invsMtx.Lock()
	exp.invs = inv
	exp.invsMtx.Unlock()
	log.Debugf("Updated mempool details for the explorerUI.")
}

func (exp *ExplorerUI) StoreLTCMPData(_ []types.MempoolTx, inv *types.MutilchainMempoolInfo) {
	// Get exclusive access to the Mempool field.
	exp.invsMtx.Lock()
	exp.LtcMempoolInfo = inv
	exp.invsMtx.Unlock()
	log.Debugf("Updated mempool details for the explorerUI.")
}

func (exp *ExplorerUI) StoreBTCMPData(_ []types.MempoolTx, inv *types.MutilchainMempoolInfo) {
	// Get exclusive access to the Mempool field.
	exp.invsMtx.Lock()
	exp.BtcMempoolInfo = inv
	exp.invsMtx.Unlock()
	log.Debugf("Updated mempool details for the explorerUI.")
}

func (exp *ExplorerUI) StoreMutilchainMPData(chainType string, inv *types.MutilchainMempoolInfo) {
	// Get exclusive access to the Mempool field.
	exp.invsMtx.Lock()
	switch chainType {
	case mutilchain.TYPEBTC:
		exp.BtcMempoolInfo = inv
	case mutilchain.TYPELTC:
		exp.LtcMempoolInfo = inv
	}
	exp.invsMtx.Unlock()
	log.Debugf("Updated mutilchain mempool details for the explorerUI.")
}

// Store implements BlockDataSaver.
func (exp *ExplorerUI) Store(blockData *blockdata.BlockData, msgBlock *wire.MsgBlock) error {
	// Retrieve block data for the passed block hash.
	newBlockData := exp.dataSource.GetExplorerBlock(msgBlock.BlockHash().String())

	// Use the latest block's blocktime to get the last 24hr timestamp.
	day := 24 * time.Hour
	targetTimePerBlock := float64(exp.ChainParams.TargetTimePerBlock)

	// Hashrate change over last day
	timestamp := newBlockData.BlockTime.T.Add(-day).Unix()
	last24hrDifficulty := exp.dataSource.Difficulty(timestamp)
	last24HrHashRate := dbtypes.CalculateHashRate(last24hrDifficulty, targetTimePerBlock)

	// Hashrate change over last month
	timestamp = newBlockData.BlockTime.T.Add(-30 * day).Unix()
	lastMonthDifficulty := exp.dataSource.Difficulty(timestamp)
	lastMonthHashRate := dbtypes.CalculateHashRate(lastMonthDifficulty, targetTimePerBlock)

	difficulty := blockData.Header.Difficulty
	hashrate := dbtypes.CalculateHashRate(difficulty, targetTimePerBlock)

	// If BlockData contains non-nil PoolInfo, compute actual percentage of DCR
	// supply staked.
	stakePerc := 45.0
	if blockData.PoolInfo != nil {
		stakePerc = blockData.PoolInfo.Value / dcrutil.Amount(blockData.ExtraInfo.CoinSupply).ToCoin()
	}

	treasuryBalance, err := exp.dataSource.TreasuryBalance()
	if err != nil {
		log.Errorf("Store: TreasuryBalance failed: %v", err)
		treasuryBalance = &dbtypes.TreasuryBalance{}
	}

	totalSize := exp.dataSource.GetDecredBlockchainSize()

	// Get average block size
	formattedAvgBlockSize, err := exp.dataSource.GetAvgBlockFormattedSize()
	if err != nil {
		log.Errorf("AvgBlockSize: Get Average block size failed: %v", err)
	}
	// Get average tx fees
	avgTxFee, err := exp.dataSource.GetAvgTxFee()
	if err != nil {
		log.Errorf("GetAvgTxFee: Get Average tx fees failed: %v", err)
	}

	posSubsPerVote := dcrutil.Amount(blockData.ExtraInfo.NextBlockSubsidy.PoS).ToCoin() /
		float64(exp.ChainParams.TicketsPerBlock)

	// Update pageData with block data and chain (home) info.
	p := exp.pageData
	p.Lock()

	// Store current block and blockchain data.
	p.BlockInfo = newBlockData
	p.BlockchainInfo = blockData.BlockchainInfo
	// Update HomeInfo.
	p.HomeInfo.HashRate = hashrate
	p.HomeInfo.HashRateChangeDay = 100 * (hashrate - last24HrHashRate) / last24HrHashRate
	p.HomeInfo.HashRateChangeMonth = 100 * (hashrate - lastMonthHashRate) / lastMonthHashRate
	p.HomeInfo.CoinSupply = blockData.ExtraInfo.CoinSupply
	p.HomeInfo.CoinValueSupply = blockData.ExtraInfo.CoinValueSupply
	p.HomeInfo.StakeDiff = blockData.CurrentStakeDiff.CurrentStakeDifficulty
	p.HomeInfo.NextExpectedStakeDiff = blockData.EstStakeDiff.Expected
	p.HomeInfo.NextExpectedBoundsMin = blockData.EstStakeDiff.Min
	p.HomeInfo.NextExpectedBoundsMax = blockData.EstStakeDiff.Max
	p.HomeInfo.IdxBlockInWindow = blockData.IdxBlockInWindow
	p.HomeInfo.IdxInRewardWindow = int(newBlockData.Height%exp.ChainParams.SubsidyReductionInterval) + 1
	p.HomeInfo.Difficulty = difficulty
	p.HomeInfo.TreasuryBalance = treasuryBalance
	p.HomeInfo.NBlockSubsidy.Dev = blockData.ExtraInfo.NextBlockSubsidy.Developer
	p.HomeInfo.NBlockSubsidy.PoS = blockData.ExtraInfo.NextBlockSubsidy.PoS
	p.HomeInfo.NBlockSubsidy.PoW = blockData.ExtraInfo.NextBlockSubsidy.PoW
	p.HomeInfo.NBlockSubsidy.Total = blockData.ExtraInfo.NextBlockSubsidy.Total
	p.HomeInfo.TotalSize = totalSize
	p.HomeInfo.FormattedSize = humanize.Bytes(uint64(p.HomeInfo.TotalSize))
	p.HomeInfo.FormattedAvgBlockSize = formattedAvgBlockSize
	p.HomeInfo.TxFeeAvg = avgTxFee
	// If BlockData contains non-nil PoolInfo, copy values.
	p.HomeInfo.PoolInfo = types.TicketPoolInfo{}
	if blockData.PoolInfo != nil {
		tpTarget := uint32(exp.ChainParams.TicketPoolSize) * uint32(exp.ChainParams.TicketsPerBlock)
		p.HomeInfo.PoolInfo = types.TicketPoolInfo{
			Size:          blockData.PoolInfo.Size,
			Value:         blockData.PoolInfo.Value,
			ValAvg:        blockData.PoolInfo.ValAvg,
			Percentage:    stakePerc * 100,
			PercentTarget: 100 * float64(blockData.PoolInfo.Size) / float64(tpTarget),
			Target:        tpTarget,
		}
	}

	p.HomeInfo.TicketReward = 100 * posSubsPerVote /
		blockData.CurrentStakeDiff.CurrentStakeDifficulty
	// The actual reward of a ticket needs to also take into consideration the
	// ticket maturity (time from ticket purchase until its eligible to vote)
	// and coinbase maturity (time after vote until funds distributed to ticket
	// holder are available to use).
	avgSSTxToSSGenMaturity := exp.MeanVotingBlocks +
		int64(exp.ChainParams.TicketMaturity) +
		int64(exp.ChainParams.CoinbaseMaturity)
	p.HomeInfo.RewardPeriod = fmt.Sprintf("%.2f days", float64(avgSSTxToSSGenMaturity)*
		exp.ChainParams.TargetTimePerBlock.Hours()/24)

	// If exchange monitoring is enabled, set the exchange rate.
	if exp.xcBot != nil {
		exchangeConversion := exp.xcBot.Conversion(1.0)
		if exchangeConversion != nil {
			conversion := types.Conversion(*exchangeConversion)
			p.HomeInfo.ExchangeRate = &conversion
		} else {
			log.Errorf("No rate conversion available yet.")
		}
	}

	p.Unlock()

	// Signal to the websocket hub that a new block was received, but do not
	// block Store(), and do not hang forever in a goroutine waiting to send.
	go func() {
		select {
		case exp.WsHub.HubRelay <- pstypes.HubMessage{Signal: sigNewBlock}:
		case <-time.After(time.Second * 10):
			log.Errorf("sigNewBlock send failed: Timeout waiting for WebsocketHub.")
		}
	}()

	log.Debugf("Got new block %d for the explorer.", newBlockData.Height)

	// handler summary info and send to socket
	go func() {
		// handler summary info
		totalTransactions := exp.dataSource.GetDecredTotalTransactions()
		// get peer count
		peerCount, _ := exp.dataSource.GetPeerCount()
		// Get additional blockchain info
		totalAddresses, totalOutputs, _ := exp.dataSource.GetBlockchainSummaryInfo()
		// Get atomic swaps summary info
		swapsTotalContract, swapsTotalAmount, _, _ := exp.dataSource.GetAtomicSwapSummary()
		// Get atomic swaps refund contract count
		refundCount, _ := exp.dataSource.CountRefundContract()
		// Get last 5 pool info
		poolResult, err := exp.dataSource.GetLast5PoolDataList()
		if err != nil {
			log.Errorf("PoolAPI: Get Pool info from Pool API failed: %v", err)
		}
		// Get vsp list
		vspList, err := externalapi.GetVSPList()
		if err != nil {
			log.Errorf("VspAPI: Get vsp list failed: %v", err)
		}
		// Get tickets summary
		ticketSummaryInfo, err := exp.dataSource.GetTicketsSummaryInfo()
		if err != nil {
			log.Errorf("GetTicketsSummaryInfo: Get tickets summary info failed: %v", err)
		}
		// Get swaps list for blocks
		swaps, err := exp.dataSource.GetBlockSwapGroupFullData(newBlockData.Txids)
		if err != nil {
			log.Errorf("Get swaps full data for block txs failed: %v", err)
			swaps = make([]*dbtypes.AtomicSwapFullData, 0)
		}
		// calculator total bison wallet vol
		bwVol, bwLast30DaysVol, _ := exp.dataSource.GetBwDashData()
		p.Lock()
		p.SummaryInfo.PeerCount = int64(peerCount)
		p.SummaryInfo.TotalTransactions = totalTransactions
		p.SummaryInfo.TotalAddresses = totalAddresses
		p.SummaryInfo.TotalOutputs = totalOutputs
		p.SummaryInfo.SwapsTotalContract = swapsTotalContract
		p.SummaryInfo.SwapsTotalAmount = swapsTotalAmount
		p.SummaryInfo.RefundCount = refundCount
		p.SummaryInfo.PoolDataList = poolResult
		p.SummaryInfo.VSPList = vspList
		p.SummaryInfo.BisonWalletVol = bwVol
		p.SummaryInfo.BWLast30DaysVol = bwLast30DaysVol
		p.SummaryInfo.TicketsSummary = ticketSummaryInfo
		p.SummaryInfo.GroupSwaps = swaps
		p.Unlock()
		select {
		case exp.WsHub.HubRelay <- pstypes.HubMessage{Signal: sigSummaryInfo}:
		case <-time.After(time.Second * 10):
			log.Errorf("sigSummaryInfo send failed: Timeout waiting for WebsocketHub.")
		}
		log.Debugf("Update new Decred summary infos for the pubsubhub.")
	}()

	// handler summary 24h info and send to socket
	go func(height int64) {
		//Get 24h metrics summary info
		p.Lock()
		//if syncing, ignore
		if p.Syncing24h {
			p.Unlock()
			return
		}
		p.Unlock()
		summary24h, err24h := exp.dataSource.SyncAndGet24hMetricsInfo(height, mutilchain.TYPEDCR)
		// get active addr num count
		activeAddr := exp.dataSource.Get24hActiveAddressesCount()
		poolvalue24hBefore, misses, err := exp.dataSource.Get24hStakingInfo()
		if err != nil {
			log.Errorf("Get24hStakingInfo failed: %v", err)
			return
		}
		treasuryBalance24hChange, err := exp.dataSource.Get24hTreasuryBalanceChange()
		if err != nil {
			log.Errorf("Get24hTreasuryBalanceChange failed: %v", err)
			return
		}
		// get bw vol 24h
		_, _, bw24hVol := exp.dataSource.GetBwDashData()
		p.Lock()
		if err24h == nil {
			p.Block24hInfo = summary24h
			p.Block24hInfo.ActiveAddresses = activeAddr
			p.Block24hInfo.TotalPowReward = p.Block24hInfo.Blocks * p.HomeInfo.NBlockSubsidy.PoW
			p.Block24hInfo.DCRSupply = p.Block24hInfo.TotalPowReward * 100
			currentPoolValue, err := dcrutil.NewAmount(p.HomeInfo.PoolInfo.Value)
			if err == nil {
				p.Block24hInfo.StakedDCR = int64(currentPoolValue) - poolvalue24hBefore
			}
			p.Block24hInfo.Missed = misses
			p.Block24hInfo.PosReward = p.Block24hInfo.TotalPowReward * 89
			p.Block24hInfo.Voted = p.Block24hInfo.Blocks * 5
			p.Block24hInfo.TreasuryBalanceChange = treasuryBalance24hChange
			p.Block24hInfo.BisonWalletVol = bw24hVol
			stakeDiffAmount, err := dcrutil.NewAmount(p.HomeInfo.StakeDiff)
			if err != nil {
				p.Unlock()
				log.Errorf("Convert to amount failed: %v", err)
				return
			}
			p.Block24hInfo.NumTickets = int64(math.Abs(math.Round(float64(p.Block24hInfo.StakedDCR) / float64(stakeDiffAmount))))
		} else {
			p.Unlock()
			log.Errorf("Sync DCR 24h Metrics Failed: %v", err24h)
			return
		}
		p.Syncing24h = false
		p.Unlock()

		select {
		case exp.WsHub.HubRelay <- pstypes.HubMessage{Signal: sigSummary24h}:
		case <-time.After(time.Second * 10):
			log.Errorf("sigSummary24h send failed: Timeout waiting for WebsocketHub.")
		}
		log.Debugf("Update new Decred summary 24h infos for the pubsubhub.")
	}(int64(blockData.Header.Height))

	// Do not run remaining updates when blockchain sync is running.
	if exp.AreDBsSyncing() {
		return nil
	}

	// Simulate the annual staking rate.
	go func(height int64, sdiff float64, supply int64) {
		ASR, _ := exp.simulateASR(1000, false, stakePerc,
			dcrutil.Amount(supply).ToCoin(),
			float64(height), sdiff)
		p.Lock()
		p.HomeInfo.ASR = ASR
		p.Unlock()
	}(newBlockData.Height, blockData.CurrentStakeDiff.CurrentStakeDifficulty,
		blockData.ExtraInfo.CoinSupply) // eval args now instead of in closure

	// Project fund balance, not useful while syncing.
	if exp.devPrefetch {
		go exp.updateDevFundBalance()
	}

	// Trigger a vote info refresh.
	if exp.voteTracker != nil {
		go exp.voteTracker.Refresh()
	}

	// Update proposals data every 5 blocks
	if (newBlockData.Height%5 == 0) && exp.proposals != nil {
		// Update the proposal DB. This is run asynchronously since it involves
		// a query to Politeia (a remote system) and we do not want to block
		// execution.
		go func() {
			err := exp.proposals.ProposalsSync()
			if err != nil {
				log.Errorf("(PoliteiaBackend).ProposalsSync: %v", err)
			}
		}()
	}

	// Update consensus agendas data every 5 blocks.
	if newBlockData.Height%5 == 0 {
		go func() {
			err := exp.agendasSource.UpdateAgendas()
			if err != nil {
				log.Errorf("(agendaBackend).UpdateAgendas: %v", err)
			}
		}()
	}

	//run sync address summary data
	go func() {
		err := exp.dataSource.SyncAddressSummary()
		if err != nil {
			log.Errorf("Sync address summary failed: %v", err)
		}
		err = exp.dataSource.SyncTreasurySummary()
		if err != nil {
			log.Errorf("Sync treasury summary failed: %v", err)
		}
	}()
	return nil
}

// Store implements BlockDataSaver.
func (exp *ExplorerUI) BTCStore(blockData *blockdatabtc.BlockData, msgBlock *btcwire.MsgBlock) error {
	// Retrieve block data for the passed block hash.
	newBlockData := exp.dataSource.GetBTCExplorerBlock(msgBlock.BlockHash().String())

	// Use the latest block's blocktime to get the last 24hr timestamp.
	// day := 24 * time.Hour
	targetTimePerBlock := float64(exp.BtcChainParams.TargetTimePerBlock)

	// // Hashrate change over last day
	// timestamp := newBlockData.BlockTime.T.Add(-day).Unix()
	// last24hrDifficulty := exp.dataSource.MutilchainDifficulty(timestamp, mutilchain.TYPEBTC)
	// last24HrHashRate := dbtypes.CalculateHashRate(last24hrDifficulty, targetTimePerBlock)

	// // Hashrate change over last month
	// timestamp = newBlockData.BlockTime.T.Add(-30 * day).Unix()
	// lastMonthDifficulty := exp.dataSource.MutilchainDifficulty(timestamp, mutilchain.TYPEBTC)
	// lastMonthHashRate := dbtypes.CalculateHashRate(lastMonthDifficulty, targetTimePerBlock)

	totalTransactionCount := int64(0)
	chainSize := int64(0)
	difficulty := float64(0)
	coinSupply := int64(0)
	coinValueSupply := float64(0)
	var chainErr error
	var blockchainInfo *mutilchain.BlockchainInfo
	//Get transactions total count
	blockchainInfo, chainErr = exp.dataSource.MutilchainGetBlockchainInfo(mutilchain.TYPEBTC)
	if chainErr != nil {
		difficulty = blockData.Header.Difficulty
		totalTransactionCount = exp.dataSource.MutilchainGetTransactionCount(mutilchain.TYPEBTC)
		chainSize = blockData.ExtraInfo.BlockchainSize
		coinSupply = blockData.ExtraInfo.CoinSupply
	} else {
		totalTransactionCount = blockchainInfo.TotalTransactions
		chainSize = exp.GetMultichainBlockchainSize(mutilchain.TYPEBTC)
		difficulty = blockchainInfo.Difficulty
		coinValueSupply = blockchainInfo.CoinSupply
	}
	coinSupplyAmount, err := btcutil.NewAmount(coinValueSupply)
	if err == nil {
		coinSupply = int64(coinSupplyAmount)
	}
	hashrate := dbtypes.CalculateHashRate(difficulty, targetTimePerBlock)
	hasSwapData := len(newBlockData.GroupSwaps) > 0
	var swapsTotalContract, swapsTotalAmount, refundCount int64
	if hasSwapData {
		// Get atomic swaps summary info
		swapsTotalContract, swapsTotalAmount, _, _ = exp.dataSource.GetAtomicSwapSummary()
		// Get atomic swaps refund contract count
		refundCount, _ = exp.dataSource.CountRefundContract()
	}
	//TODO: Open later
	//totalVoutsCount := exp.dataSource.MutilchainGetTotalVoutsCount(mutilchain.TYPEBTC)
	//totalAddressesCount := exp.dataSource.MutilchainGetTotalAddressesCount(mutilchain.TYPEBTC)
	// err = exp.dataSource.SyncLast20BTCBlocks(blockData.Header.Height)
	// if err != nil {
	// 	log.Error(err)
	// } else {
	// 	log.Infof("Sync last 25 BTC Blocks successfully")
	// }
	//get and set 25 last block
	blocks := exp.dataSource.GetMutilchainExplorerFullBlocks(mutilchain.TYPEBTC, int(newBlockData.Height)-MultichainHomepageBlocksMaxCount, int(newBlockData.Height))
	// get last 5 block pools
	last5BlockPools, err := exp.dataSource.GetLastMultichainPoolDataList(mutilchain.TYPEBTC, newBlockData.Height)
	if err != nil {
		log.Errorf("Get BTC last 10 block pools failed: ", err)
	}
	newBlockData.PoolDataList = last5BlockPools
	log.Debugf("Get last 10 BTC Blocks pool successfully")
	// get multichain stats from blockchair
	totalAddresses := int64(0)
	totalOutputs := int64(0)
	volume24h := int64(0)
	nodes := int64(0)
	avgTxFees24h := int64(0)
	chainStats, err := exp.dataSource.GetMultichainStats(mutilchain.TYPEBTC)
	if err != nil {
		log.Warnf("BTC: Get multichain stats failed. %v", err)
	} else {
		totalAddresses = chainStats.HodlingAddresses
		totalOutputs = chainStats.Outputs
		volume24h = chainStats.Volume24h
		nodes = chainStats.Nodes
		avgTxFees24h = chainStats.AverageTransactionFee24h
		log.Debugf("BTC: Get Multichain stats successfully")
	}
	// Update pageData with block data and chain (home) info.
	p := exp.BtcPageData
	p.Lock()

	// Store current block and blockchain data.
	p.BlockInfo = newBlockData
	p.BlockchainInfo = blockData.BlockchainInfo

	// Update HomeInfo.
	p.HomeInfo.HashRate = hashrate
	// p.HomeInfo.HashRateChangeDay = 100 * (hashrate - last24HrHashRate) / last24HrHashRate
	// p.HomeInfo.HashRateChangeMonth = 100 * (hashrate - lastMonthHashRate) / lastMonthHashRate
	p.HomeInfo.CoinSupply = coinSupply
	p.HomeInfo.CoinValueSupply = coinValueSupply
	p.HomeInfo.Difficulty = difficulty
	p.HomeInfo.TotalTransactions = totalTransactionCount
	//p.HomeInfo.TotalOutputs = totalVoutsCount
	//p.HomeInfo.TotalAddresses = totalAddressesCount
	p.HomeInfo.TotalSize = chainSize
	p.HomeInfo.FormattedSize = humanize.Bytes(uint64(chainSize))
	p.HomeInfo.IdxInRewardWindow = int(newBlockData.Height%int64(exp.BtcChainParams.SubsidyReductionInterval)) + 1
	p.HomeInfo.NBlockSubsidy.Total = blockData.ExtraInfo.NextBlockReward
	p.HomeInfo.BlockReward = blockData.ExtraInfo.BlockReward
	p.HomeInfo.SubsidyInterval = int64(exp.BtcChainParams.SubsidyReductionInterval)
	p.HomeInfo.TotalAddresses = totalAddresses
	p.HomeInfo.TotalOutputs = totalOutputs
	p.HomeInfo.Nodes = nodes
	p.HomeInfo.Volume24h = volume24h
	p.HomeInfo.TxFeeAvg24h = avgTxFees24h
	if hasSwapData {
		p.HomeInfo.SwapsTotalContract = swapsTotalContract
		p.HomeInfo.SwapsTotalAmount = swapsTotalAmount
		p.HomeInfo.RefundCount = refundCount
	}
	p.BlockDetails = blocks
	p.Unlock()
	go func() {
		select {
		case exp.WsHub.HubRelay <- pstypes.HubMessage{Signal: sigNewBTCBlock}:
		case <-time.After(time.Second * 10):
			log.Errorf("sigNewBTCBlock send failed: Timeout waiting for WebsocketHub.")
		}
	}()

	go func(height int64) {
		//Get 24h metrics summary info
		p.Lock()
		//if syncing, ignore
		if p.Syncing24h {
			p.Unlock()
			return
		}
		p.Unlock()
		summary24h, err24h := exp.dataSource.SyncAndGet24hMetricsInfo(height, mutilchain.TYPEBTC)
		p.Lock()
		if err24h == nil {
			p.HomeInfo.Block24hInfo = summary24h
		} else {
			log.Errorf("Sync BTC 24h Metrics Failed: %v", err24h)
		}
		p.Syncing24h = false
		p.Unlock()
	}(int64(blockData.Header.Height))
	return nil
}

func (exp *ExplorerUI) LTCStore(blockData *blockdataltc.BlockData, msgBlock *ltcwire.MsgBlock) error {
	// Retrieve block data for the passed block hash.
	newBlockData := exp.dataSource.GetLTCExplorerBlock(msgBlock.BlockHash().String())
	// Use the latest block's blocktime to get the last 24hr timestamp.
	// day := 24 * time.Hour
	targetTimePerBlock := float64(exp.LtcChainParams.TargetTimePerBlock)

	// Hashrate change over last day
	// timestamp := newBlockData.BlockTime.T.Add(-day).Unix()
	// last24hrDifficulty := exp.dataSource.MutilchainDifficulty(timestamp, mutilchain.TYPELTC)
	// last24HrHashRate := dbtypes.CalculateHashRate(last24hrDifficulty, targetTimePerBlock)

	// Hashrate change over last month
	// timestamp = newBlockData.BlockTime.T.Add(-30 * day).Unix()
	// lastMonthDifficulty := exp.dataSource.MutilchainDifficulty(timestamp, mutilchain.TYPELTC)
	// lastMonthHashRate := dbtypes.CalculateHashRate(lastMonthDifficulty, targetTimePerBlock)

	totalTransactionCount := int64(0)
	chainSize := int64(0)
	difficulty := float64(0)
	coinSupply := int64(0)
	coinValueSupply := float64(0)
	var chainErr error
	var blockchainInfo *mutilchain.BlockchainInfo
	//Get transactions total count
	blockchainInfo, chainErr = exp.dataSource.MutilchainGetBlockchainInfo(mutilchain.TYPELTC)
	if chainErr != nil {
		difficulty = blockData.Header.Difficulty
		totalTransactionCount = exp.dataSource.MutilchainGetTransactionCount(mutilchain.TYPELTC)
		chainSize = blockData.ExtraInfo.BlockchainSize
		coinSupply = blockData.ExtraInfo.CoinSupply
	} else {
		totalTransactionCount = blockchainInfo.TotalTransactions
		chainSize = exp.GetMultichainBlockchainSize(mutilchain.TYPELTC)
		difficulty = blockchainInfo.Difficulty
		coinValueSupply = blockchainInfo.CoinSupply
	}
	coinSupplyAmount, err := ltcutil.NewAmount(coinValueSupply)
	if err == nil {
		coinSupply = int64(coinSupplyAmount)
	}
	hashrate := dbtypes.CalculateHashRate(difficulty, targetTimePerBlock)
	hasSwapData := len(newBlockData.GroupSwaps) > 0
	var swapsTotalContract, swapsTotalAmount, refundCount int64
	if hasSwapData {
		// Get atomic swaps summary info
		swapsTotalContract, swapsTotalAmount, _, _ = exp.dataSource.GetAtomicSwapSummary()
		// Get atomic swaps refund contract count
		refundCount, _ = exp.dataSource.CountRefundContract()
	}
	//TODO: Open later
	//totalVoutsCount := exp.dataSource.MutilchainGetTotalVoutsCount(mutilchain.TYPELTC)
	//totalAddressesCount := exp.dataSource.MutilchainGetTotalAddressesCount(mutilchain.TYPELTC)
	err = exp.dataSource.SyncLast20LTCBlocks(blockData.Header.Height)
	if err != nil {
		log.Error(err)
	}
	//get and set 25 last block
	blocks := exp.dataSource.GetMutilchainExplorerFullBlocks(mutilchain.TYPELTC, int(newBlockData.Height)-MultichainHomepageBlocksMaxCount, int(newBlockData.Height))
	// get last 5 block pools
	last5BlockPools, err := exp.dataSource.GetLastMultichainPoolDataList(mutilchain.TYPELTC, newBlockData.Height)
	if err != nil {
		log.Errorf("Get LTC last 10 block pools failed: ", err)
	}
	newBlockData.PoolDataList = last5BlockPools
	log.Debugf("Get last 10 LTC Blocks pool successfully")
	// get multichain stats from blockchair
	totalAddresses := int64(0)
	totalOutputs := int64(0)
	volume24h := int64(0)
	nodes := int64(0)
	avgTxFees24h := int64(0)
	chainStats, err := exp.dataSource.GetMultichainStats(mutilchain.TYPELTC)
	if err != nil {
		log.Warnf("LTC: Get multichain stats failed. %v", err)
	} else {
		totalAddresses = chainStats.HodlingAddresses
		totalOutputs = chainStats.Outputs
		volume24h = chainStats.Volume24h
		nodes = chainStats.Nodes
		avgTxFees24h = chainStats.AverageTransactionFee24h
		log.Debugf("LTC: Get Multichain stats successfully")
	}
	// Update pageData with block data and chain (home) info.
	p := exp.LtcPageData
	p.Lock()

	// Store current block and blockchain data.
	p.BlockInfo = newBlockData
	p.BlockchainInfo = blockData.BlockchainInfo

	// Update HomeInfo.
	p.HomeInfo.HashRate = hashrate
	// p.HomeInfo.HashRateChangeDay = 100 * (hashrate - last24HrHashRate) / last24HrHashRate
	// p.HomeInfo.HashRateChangeMonth = 100 * (hashrate - lastMonthHashRate) / lastMonthHashRate
	p.HomeInfo.CoinSupply = coinSupply
	p.HomeInfo.CoinValueSupply = coinValueSupply
	p.HomeInfo.Difficulty = difficulty
	p.HomeInfo.TotalTransactions = totalTransactionCount
	//p.HomeInfo.TotalOutputs = totalVoutsCount
	//p.HomeInfo.TotalAddresses = totalAddressesCount
	p.HomeInfo.TotalSize = chainSize
	p.HomeInfo.FormattedSize = humanize.Bytes(uint64(chainSize))
	p.HomeInfo.IdxInRewardWindow = int(newBlockData.Height%int64(exp.LtcChainParams.SubsidyReductionInterval)) + 1
	p.HomeInfo.NBlockSubsidy.Total = blockData.ExtraInfo.NextBlockReward
	p.HomeInfo.BlockReward = blockData.ExtraInfo.BlockReward
	p.HomeInfo.SubsidyInterval = int64(exp.LtcChainParams.SubsidyReductionInterval)
	p.HomeInfo.TotalAddresses = totalAddresses
	p.HomeInfo.TotalOutputs = totalOutputs
	p.HomeInfo.Nodes = nodes
	p.HomeInfo.Volume24h = volume24h
	p.HomeInfo.TxFeeAvg24h = avgTxFees24h
	if hasSwapData {
		p.HomeInfo.SwapsTotalContract = swapsTotalContract
		p.HomeInfo.SwapsTotalAmount = swapsTotalAmount
		p.HomeInfo.RefundCount = refundCount
	}
	p.BlockDetails = blocks
	p.Unlock()
	go func() {
		select {
		case exp.WsHub.HubRelay <- pstypes.HubMessage{Signal: sigNewLTCBlock}:
		case <-time.After(time.Second * 10):
			log.Errorf("sigNewLTCBlock send failed: Timeout waiting for WebsocketHub.")
		}
	}()
	go func(height int64) {
		//Get 24h metrics summary info
		p.Lock()
		//if syncing, ignore
		if p.Syncing24h {
			p.Unlock()
			return
		}
		p.Unlock()
		summary24h, err24h := exp.dataSource.SyncAndGet24hMetricsInfo(height, mutilchain.TYPELTC)
		p.Lock()
		if err24h == nil {
			p.HomeInfo.Block24hInfo = summary24h
		} else {
			log.Errorf("Sync LTC 24h Metrics Failed: %v", err24h)
		}
		p.Syncing24h = false
		p.Unlock()
	}(int64(blockData.Header.Height))
	return nil
}

func (exp *ExplorerUI) GetMultichainBlockchainSize(chainType string) int64 {
	mutilchainChartData := exp.GetMutilchainChartData(chainType)
	if mutilchainChartData == nil {
		return 0
	}
	chartData, err := mutilchainChartData.Chart("blockchain-size", "day", "time")
	if err != nil {
		return 0
	}
	var chainSizeData mutilchain.MultichainChainSizeChartData
	err = json.Unmarshal(chartData, &chainSizeData)
	if err != nil {
		return 0
	}
	maxTime := int64(0)
	timeIndex := -1
	for index, time := range chainSizeData.T {
		if time > maxTime {
			maxTime = time
			timeIndex = index
		}
	}
	if timeIndex > 0 {
		return chainSizeData.Size[timeIndex]
	}
	return 0
}

func (exp *ExplorerUI) GetMutilchainChartData(chainType string) *cache.MutilchainChartData {
	switch chainType {
	case mutilchain.TYPEBTC:
		return exp.BtcChartSource
	case mutilchain.TYPELTC:
		return exp.LtcChartSource
	default:
		return nil
	}
}

// ChartsUpdated should be called when a chart update completes.
func (exp *ExplorerUI) ChartsUpdated() {
	anonSet := exp.chartSource.AnonymitySet()
	exp.pageData.Lock()
	if exp.pageData.HomeInfo.CoinSupply > 0 {
		exp.pageData.HomeInfo.MixedPercent = float64(anonSet) / float64(exp.pageData.HomeInfo.CoinSupply) * 100
	}
	exp.pageData.Unlock()
}

func (exp *ExplorerUI) updateDevFundBalance() {
	// yield processor to other goroutines
	runtime.Gosched()

	devBalance, err := exp.dataSource.DevBalance()
	if err == nil && devBalance != nil {
		exp.pageData.Lock()
		exp.pageData.HomeInfo.DevFund = devBalance.TotalUnspent
		exp.pageData.Unlock()
	} else {
		log.Errorf("ExplorerUI.updateDevFundBalance failed: %v", err)
	}
}

type loggerFunc func(string, ...interface{})

func (lw loggerFunc) Printf(str string, args ...interface{}) {
	lw(str, args...)
}

func (exp *ExplorerUI) addRoutes() {
	exp.Mux.Use(middleware.Logger)
	exp.Mux.Use(middleware.Recoverer)
	corsMW := cors.Default()
	corsMW.Log = loggerFunc(log.Tracef)
	exp.Mux.Use(corsMW.Handler)

	redirect := func(url string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			x := chi.URLParam(r, "x")
			if x != "" {
				x = "/" + x
			}
			http.Redirect(w, r, "/"+url+x, http.StatusPermanentRedirect)
		}
	}
	exp.Mux.Get("/", redirect("blocks"))

	exp.Mux.Get("/block/{x}", redirect("block"))

	exp.Mux.Get("/tx/{x}", redirect("tx"))

	exp.Mux.Get("/address/{x}", redirect("address"))

	exp.Mux.Get("/decodetx", redirect("decodetx"))

	exp.Mux.Get("/stats", redirect("statistics"))
}

// Simulate ticket purchase and re-investment over a full year for a given
// starting amount of DCR and calculation parameters.  Generate a TEXT table of
// the simulation results that can optionally be used for future expansion of
// dcrdata functionality.
func (exp *ExplorerUI) simulateASR(StartingDCRBalance float64, IntegerTicketQty bool,
	CurrentStakePercent float64, ActualCoinbase float64, CurrentBlockNum float64,
	ActualTicketPrice float64) (ASR float64, ReturnTable string) {

	// Calculations are only useful on mainnet.  Short circuit calculations if
	// on any other version of chain params.
	if exp.ChainParams.Name != "mainnet" {
		return 0, ""
	}

	BlocksPerDay := 86400 / exp.ChainParams.TargetTimePerBlock.Seconds()
	BlocksPerYear := 365 * BlocksPerDay
	TicketsPurchased := float64(0)

	votesPerBlock := exp.ChainParams.VotesPerBlock()

	StakeRewardAtBlock := func(blocknum float64) float64 {
		Subsidy := exp.dataSource.BlockSubsidy(int64(blocknum), votesPerBlock)
		return dcrutil.Amount(Subsidy.PoS / int64(votesPerBlock)).ToCoin()
	}

	MaxCoinSupplyAtBlock := func(blocknum float64) float64 {
		// 4th order poly best fit curve to Decred mainnet emissions plot.
		// Curve fit was done with 0 Y intercept and Pre-Mine added after.

		return (-9e-19*math.Pow(blocknum, 4) +
			7e-12*math.Pow(blocknum, 3) -
			2e-05*math.Pow(blocknum, 2) +
			29.757*blocknum + 76963 +
			1680000) // Premine 1.68M
	}

	CoinAdjustmentFactor := ActualCoinbase / MaxCoinSupplyAtBlock(CurrentBlockNum)

	TheoreticalTicketPrice := func(blocknum float64) float64 {
		ProjectedCoinsCirculating := MaxCoinSupplyAtBlock(blocknum) * CoinAdjustmentFactor * CurrentStakePercent
		TicketPoolSize := (float64(exp.MeanVotingBlocks) + float64(exp.ChainParams.TicketMaturity) +
			float64(exp.ChainParams.CoinbaseMaturity)) * float64(exp.ChainParams.TicketsPerBlock)
		return ProjectedCoinsCirculating / TicketPoolSize
	}
	TicketAdjustmentFactor := ActualTicketPrice / TheoreticalTicketPrice(CurrentBlockNum)

	// Prepare for simulation
	simblock := CurrentBlockNum
	TicketPrice := ActualTicketPrice
	DCRBalance := StartingDCRBalance

	ReturnTable += "\n\nBLOCKNUM        DCR  TICKETS TKT_PRICE TKT_REWRD  ACTION\n"
	ReturnTable += fmt.Sprintf("%8d  %9.2f %8.1f %9.2f %9.2f    INIT\n",
		int64(simblock), DCRBalance, TicketsPurchased,
		TicketPrice, StakeRewardAtBlock(simblock))

	for simblock < (BlocksPerYear + CurrentBlockNum) {
		// Simulate a Purchase on simblock
		TicketPrice = TheoreticalTicketPrice(simblock) * TicketAdjustmentFactor

		if IntegerTicketQty {
			// Use this to simulate integer qtys of tickets up to max funds
			TicketsPurchased = math.Floor(DCRBalance / TicketPrice)
		} else {
			// Use this to simulate ALL funds used to buy tickets - even fractional tickets
			// which is actually not possible
			TicketsPurchased = (DCRBalance / TicketPrice)
		}

		DCRBalance -= (TicketPrice * TicketsPurchased)
		ReturnTable += fmt.Sprintf("%8d  %9.2f %8.1f %9.2f %9.2f     BUY\n",
			int64(simblock), DCRBalance, TicketsPurchased,
			TicketPrice, StakeRewardAtBlock(simblock))

		// Move forward to average vote
		simblock += (float64(exp.ChainParams.TicketMaturity) + float64(exp.MeanVotingBlocks))
		ReturnTable += fmt.Sprintf("%8d  %9.2f %8.1f %9.2f %9.2f    VOTE\n",
			int64(simblock), DCRBalance, TicketsPurchased,
			(TheoreticalTicketPrice(simblock) * TicketAdjustmentFactor), StakeRewardAtBlock(simblock))

		// Simulate return of funds
		DCRBalance += (TicketPrice * TicketsPurchased)

		// Simulate reward
		DCRBalance += (StakeRewardAtBlock(simblock) * TicketsPurchased)
		TicketsPurchased = 0

		// Move forward to coinbase maturity
		simblock += float64(exp.ChainParams.CoinbaseMaturity)

		ReturnTable += fmt.Sprintf("%8d  %9.2f %8.1f %9.2f %9.2f  REWARD\n",
			int64(simblock), DCRBalance, TicketsPurchased,
			(TheoreticalTicketPrice(simblock) * TicketAdjustmentFactor), StakeRewardAtBlock(simblock))

		// Need to receive funds before we can use them again so add 1 block
		simblock++
	}

	// Scale down to exactly 365 days
	SimulationReward := ((DCRBalance - StartingDCRBalance) / StartingDCRBalance) * 100
	ASR = (BlocksPerYear / (simblock - CurrentBlockNum)) * SimulationReward
	ReturnTable += fmt.Sprintf("ASR over 365 Days is %.2f.\n", ASR)
	return
}

func (exp *ExplorerUI) watchExchanges() {
	if exp.xcBot == nil {
		return
	}
	xcChans := exp.xcBot.UpdateChannels()

	sendXcUpdate := func(isFiat bool, token string, updater *exchanges.ExchangeState) {
		xcState := exp.xcBot.State()
		var chainType string
		switch updater.Symbol {
		case exchanges.BTCSYMBOL:
			chainType = mutilchain.TYPEBTC
		case exchanges.LTCSYMBOL:
			chainType = mutilchain.TYPELTC
		default:
			chainType = mutilchain.TYPEDCR
		}
		update := &WebsocketExchangeUpdate{
			Updater: WebsocketMiniExchange{
				Token:     token,
				ChainType: chainType,
				Price:     updater.Price,
				Volume:    updater.BaseVolume,
				Change:    updater.Change,
			},
			IsFiatIndex:     isFiat,
			BtcIndex:        exp.xcBot.BtcIndex,
			Price:           xcState.GetMutilchainPrice(chainType),
			BtcPrice:        xcState.BtcPrice,
			Volume:          xcState.GetMutilchainVolumn(chainType),
			DCRBTCPrice:     xcState.DCRBTCPrice,
			DCRBTCVolume:    xcState.DCRBTCVolume,
			DCRUSD24hChange: xcState.DCRUSD24hChange,
			DCRBTC24hChange: xcState.DCRBTC24hChange,
			LowPrice:        xcState.LowPrice,
			HighPrice:       xcState.HighPrice,
		}
		update.USDVolume = update.Price * update.Volume
		select {
		case exp.WsHub.xcChan <- update:
		default:
			log.Warnf("Failed to send WebsocketExchangeUpdate on WebsocketHub channel")
		}
	}

	for {
		select {
		case update := <-xcChans.Exchange:
			sendXcUpdate(false, update.Token, update.State)
		case update := <-xcChans.Index:
			indexState, found := exp.xcBot.State().FiatIndices[update.Token]
			if !found {
				log.Errorf("Index state not found when preparing websocket update")
				continue
			}
			sendXcUpdate(true, update.Token, indexState)
		case <-xcChans.Quit:
			log.Warnf("ExchangeBot has quit.")
			return
		case <-exp.xcDone:
			return
		}
	}
}

func (exp *ExplorerUI) getExchangeState() *exchanges.ExchangeBotState {
	if exp.xcBot == nil || exp.xcBot.IsFailed() {
		return nil
	}
	return exp.xcBot.State()
}

// mempoolTime is the TimeDef that the transaction was received in DCRData, or
// else a zero-valued TimeDef if no transaction is found.
func (exp *ExplorerUI) mempoolTime(txid string) types.TimeDef {
	exp.invsMtx.RLock()
	defer exp.invsMtx.RUnlock()
	tx, found := exp.invs.Tx(txid)
	if !found {
		return types.NewTimeDefFromUNIX(0)
	}
	return types.NewTimeDefFromUNIX(tx.Time)
}
