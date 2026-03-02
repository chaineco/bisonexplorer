// Copyright (c) 2018-2022, The Decred developers
// Copyright (c) 2017, The dcrdata developers
// See LICENSE for details.

package dcrpg

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	apitypes "github.com/decred/dcrdata/v8/api/types"
	"github.com/decred/dcrdata/v8/blockdata/blockdataltc"
	"github.com/decred/dcrdata/v8/db/dbtypes"
	exptypes "github.com/decred/dcrdata/v8/explorer/types"
	"github.com/decred/dcrdata/v8/mutilchain"
	"github.com/decred/dcrdata/v8/mutilchain/ltcrpcutils"
	"github.com/decred/dcrdata/v8/txhelpers"
	"github.com/decred/dcrdata/v8/txhelpers/ltctxhelper"
	"github.com/decred/dcrdata/v8/utils"
	humanize "github.com/dustin/go-humanize"
	ltcjson "github.com/ltcsuite/ltcd/btcjson"
	ltc_chaincfg "github.com/ltcsuite/ltcd/chaincfg"
	ltc_chainhash "github.com/ltcsuite/ltcd/chaincfg/chainhash"
	"github.com/ltcsuite/ltcd/ltcutil"
	ltcClient "github.com/ltcsuite/ltcd/rpcclient"
	ltcwire "github.com/ltcsuite/ltcd/wire"

	"github.com/decred/dcrdata/db/dcrpg/v8/internal"
)

// UseLTCMempoolChecker sets the Litecoin mempool checker for the ChainDB.
func (pgb *ChainDB) UseLTCMempoolChecker(mp ltcrpcutils.MempoolAddressChecker) {
	pgb.ltcMp = mp
}

// LTCBestBlock returns the best Litecoin block hash and height.
func (pgb *ChainDB) LTCBestBlock() (*ltc_chainhash.Hash, int64) {
	if pgb.LtcBestBlock == nil {
		return nil, 0
	}
	pgb.LtcBestBlock.Mtx.RLock()
	defer pgb.LtcBestBlock.Mtx.RUnlock()
	hash, _ := ltc_chainhash.NewHashFromStr(pgb.LtcBestBlock.Hash)
	return hash, pgb.LtcBestBlock.Height
}

// CheckCreateLtcSwapsTable checks if the LTC swaps table exists, or creates it.
func (pgb *ChainDB) CheckCreateLtcSwapsTable() (err error) {
	return checkExistAndCreateLtcSwapsTable(pgb.db)
}

// GetLTCSwapFullDataByContractTx returns atomic swap data for a LTC contract transaction.
func (pgb *ChainDB) GetLTCSwapFullDataByContractTx(contractTx, groupTx string) (spends []*dbtypes.AtomicSwapTxData, err error) {
	// get contract spends data
	var rows *sql.Rows
	rows, err = pgb.db.QueryContext(pgb.ctx, internal.SelectLTCAtomicSpendsByContractTx, contractTx, groupTx)
	if err != nil {
		return nil, err
	}

	defer rows.Close()
	for rows.Next() {
		var spendData dbtypes.AtomicSwapTxData
		err = rows.Scan(&spendData.Txid, &spendData.Vin, &spendData.Height, &spendData.Value, &spendData.LockTime)
		if err != nil {
			return
		}
		spendData.LockTimeDisp = utils.DateTimeWithoutTimeZone(spendData.LockTime)
		// get spend tx time
		var txHash *ltc_chainhash.Hash
		txHash, err = ltc_chainhash.NewHashFromStr(spendData.Txid)
		if err != nil {
			return
		}
		var txRaw *ltcjson.TxRawResult
		txRaw, err = ltcrpcutils.WithTimeout(func() (*ltcjson.TxRawResult, error) {
			return pgb.LtcClient.GetRawTransactionVerbose(txHash)
		})
		if err != nil {
			return
		}
		spendData.Time = txRaw.Time
		spendData.TimeDisp = utils.DateTimeWithoutTimeZone(spendData.Time)
		spends = append(spends, &spendData)
	}
	err = rows.Err()
	if err != nil {
		return
	}
	return
}

// GetLTCAtomicSwapTarget returns atomic swap detail of LTC.
func (pgb *ChainDB) GetLTCAtomicSwapTarget(groupTx string) (*dbtypes.AtomicSwapForTokenData, error) {
	targetData := &dbtypes.AtomicSwapForTokenData{
		Contracts: make([]*dbtypes.AtomicSwapTxData, 0),
		Results:   make([]*dbtypes.AtomicSwapTxData, 0),
	}
	// Get contract txs with
	rows, err := pgb.db.QueryContext(pgb.ctx, internal.SelectLTCContractListByGroupTx, groupTx)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var contractData dbtypes.AtomicSwapTxData
		err = rows.Scan(&contractData.Txid, &contractData.Value)
		if err != nil {
			return nil, err
		}
		// get spends of contract
		spendDatas, err := pgb.GetLTCSwapFullDataByContractTx(contractData.Txid, groupTx)
		if err != nil {
			return nil, err
		}
		// check and insert to Source spends data
		for _, spend := range spendDatas {
			exist := false
			for index, existSpend := range targetData.Results {
				if spend.Txid == existSpend.Txid {
					exist = true
					existSpend.Value += spend.Value
					targetData.Results[index] = existSpend
					break
				}
			}
			if !exist {
				targetData.Results = append(targetData.Results, spend)
			}
		}

		contractTxHash, err := ltc_chainhash.NewHashFromStr(contractData.Txid)
		if err != nil {
			return nil, err
		}
		contractTxRaw, err := ltcrpcutils.WithTimeout(func() (*ltcjson.TxRawResult, error) {
			return pgb.LtcClient.GetRawTransactionVerbose(contractTxHash)
		})
		if err != nil {
			return nil, err
		}
		targetTxRaw, err := ltcrpcutils.WithTimeout(func() (*ltcutil.Tx, error) {
			return pgb.LtcClient.GetRawTransaction(contractTxHash)
		})
		if err != nil {
			return nil, err
		}
		targetBlockHash, err := ltc_chainhash.NewHashFromStr(contractTxRaw.BlockHash)
		if err != nil {
			return nil, err
		}
		targetBlockHeader, err := ltcrpcutils.WithTimeout(func() (*ltcjson.GetBlockHeaderVerboseResult, error) {
			return pgb.LtcClient.GetBlockHeaderVerbose(targetBlockHash)
		})
		if err != nil {
			return nil, err
		}
		contractData.Height = int64(targetBlockHeader.Height)
		contractFees, err := txhelpers.CalculateLTCTxFee(pgb.LtcClient, targetTxRaw.MsgTx())
		if err != nil {
			return nil, err
		}
		contractData.Fees = int64(contractFees)
		contractData.Time = contractTxRaw.Time
		contractData.TimeDisp = utils.DateTimeWithoutTimeZone(contractData.Time)
		targetData.TotalAmount += contractData.Value
		targetData.Contracts = append(targetData.Contracts, &contractData)
	}
	err = rows.Err()
	if err != nil {
		return nil, err
	}
	return targetData, nil
}

// UpdateLTCChainState updates the Litecoin blockchain state.
func (pgb *ChainDB) UpdateLTCChainState(blockChainInfo *ltcjson.GetBlockChainInfoResult) {
	if pgb == nil {
		return
	}
	if blockChainInfo == nil {
		log.Errorf("chainjson.GetBlockChainInfoResult data passed is empty")
		return
	}

	chainInfo := dbtypes.BlockChainData{
		Chain:                  blockChainInfo.Chain,
		BestHeight:             int64(blockChainInfo.Blocks),
		BestBlockHash:          blockChainInfo.BestBlockHash,
		Difficulty:             uint32(blockChainInfo.Difficulty),
		VerificationProgress:   blockChainInfo.VerificationProgress,
		ChainWork:              blockChainInfo.ChainWork,
		IsInitialBlockDownload: blockChainInfo.InitialBlockDownload,
	}

	pgb.deployments.mtx.Lock()
	pgb.deployments.ltcChainInfo = &chainInfo
	pgb.deployments.mtx.Unlock()
}

// Store satisfies BlockDataSaver. Blocks stored this way are considered valid
// and part of mainchain. Store should not be used for batch block processing;
// instead, use StoreBlock and specify appropriate flags.
func (pgb *ChainDB) LTCStore(blockData *blockdataltc.BlockData, msgBlock *ltcwire.MsgBlock) error {
	// This function must handle being run when pgb is nil (not constructed).
	if pgb == nil || pgb.LtcBestBlock == nil {
		return nil
	}
	// update blockchain state
	pgb.UpdateLTCChainState(blockData.BlockchainInfo)
	if !pgb.ChainDBDisabled {
		isValid := true
		// Since Store should not be used in batch block processing, address'
		// spending information is updated.
		updateAddressesSpendingInfo := true

		_, _, err := pgb.StoreLTCBlock(pgb.LtcClient, msgBlock, isValid, updateAddressesSpendingInfo)
		if err != nil {
			return err
		}
		if pgb.SyncChainDBFlag {
			// TODO: go pgb.SyncOneLTCWholeBlock(pgb.LtcClient, msgBlock)
		}
	} else {
		go func() {
			err := pgb.SyncLast20LTCBlocks(blockData.Header.Height)
			if err != nil {
				log.Error(err)
			} else {
				log.Infof("Sync last 20 LTC Blocks successfully")
			}
		}()
	}
	//update best ltc block
	pgb.LtcBestBlock.Hash = blockData.Header.Hash
	pgb.LtcBestBlock.Height = int64(blockData.Header.Height)
	pgb.LtcBestBlock.Time = blockData.Header.Time
	// Signal updates to any subscribed heightClients.
	pgb.SignalLTCHeight(uint32(blockData.Header.Height))
	// sync for ltc atomic swap
	go pgb.SyncLTCAtomicSwapData(int64(blockData.Header.Height))
	// sync for block txcount
	go pgb.SyncLTCMetaInfo()
	return nil
}

// GetLTCBlockByHash returns the height of a LTC block by its hash.
func (pgb *ChainDB) GetLTCBlockByHash(hash string) (int64, error) {
	blockHash, err := ltc_chainhash.NewHashFromStr(hash)
	if err != nil {
		log.Errorf("LTC: Invalid block hash %s", hash)
		return 0, err
	}
	blockRst, err := ltcrpcutils.WithTimeout(func() (*ltcjson.GetBlockVerboseResult, error) {
		return pgb.LtcClient.GetBlockVerbose(blockHash)
	})
	if err != nil {
		log.Errorf("LTC: Get msg block failed %s", hash)
		return 0, err
	}
	return blockRst.Height, nil
}

// GetLTCDaemonTransaction gets an *apitypes.MultichainTxRaw for a given LTC transaction ID.
func (pgb *ChainDB) GetLTCDaemonTransaction(txid string) (*apitypes.MultichainTxRaw, error) {
	txHash, err := ltc_chainhash.NewHashFromStr(txid)
	if err != nil {
		return nil, err
	}
	txraw, err := ltcrpcutils.WithTimeout(func() (*ltcjson.TxRawResult, error) {
		return pgb.LtcClient.GetRawTransactionVerbose(txHash)
	})
	if err != nil {
		log.Errorf("GetLTCDaemonTransaction failed for %v: %v", txid, err)
		return nil, err
	}
	result := &apitypes.MultichainTxRaw{
		Hex:           txraw.Hex,
		Txid:          txraw.Txid,
		Hash:          txraw.Hash,
		Size:          txraw.Size,
		Vsize:         txraw.Vsize,
		Weight:        txraw.Weight,
		Version:       txraw.Version,
		LockTime:      txraw.LockTime,
		BlockHash:     txraw.BlockHash,
		Confirmations: txraw.Confirmations,
		Time:          txraw.Time,
		Blocktime:     txraw.Blocktime,
	}
	if len(txraw.Vin) > 0 {
		mulVins := make([]apitypes.MultichainTxIn, 0)
		for _, vin := range txraw.Vin {
			mulVin := apitypes.MultichainTxIn{
				Coinbase: vin.Coinbase,
				Txid:     vin.Txid,
				Vout:     vin.Vout,
				Sequence: vin.Sequence,
				Witness:  vin.Witness,
			}
			if vin.ScriptSig != nil {
				mulVin.ScriptSig = &apitypes.MultichainScriptSig{
					Asm: vin.ScriptSig.Asm,
					Hex: vin.ScriptSig.Hex,
				}
			}
			mulVins = append(mulVins, mulVin)
		}
		result.Vin = mulVins
	}

	if len(txraw.Vout) > 0 {
		mulVouts := make([]apitypes.MultichainTxOut, 0)
		for _, vout := range txraw.Vout {
			mulVouts = append(mulVouts, apitypes.MultichainTxOut{
				Value: vout.Value,
				N:     vout.N,
				ScriptPubKeyDecoded: apitypes.MultichainScriptPubKey{
					Asm:       vout.ScriptPubKey.Asm,
					Hex:       vout.ScriptPubKey.Hex,
					ReqSigs:   vout.ScriptPubKey.ReqSigs,
					Type:      vout.ScriptPubKey.Type,
					Addresses: vout.ScriptPubKey.Addresses,
				},
			})
		}
		result.Vout = mulVouts
	}
	return result, nil
}

// GetLTCAllTxIn gets all Litecoin transaction inputs for a transaction.
func (pgb *ChainDB) GetLTCAllTxIn(txid string) ([]*apitypes.MultichainTxIn, error) {
	txhash, err := ltc_chainhash.NewHashFromStr(txid)
	if err != nil {
		return nil, err
	}
	tx, err := ltcrpcutils.WithTimeout(func() (*ltcjson.TxRawResult, error) {
		return pgb.LtcClient.GetRawTransactionVerbose(txhash)
	})
	if err != nil {
		log.Warnf("[LTC] Unknown transaction %s", txid)
		return nil, err
	}
	txins := tx.Vin
	allTxIn := make([]*apitypes.MultichainTxIn, 0, len(txins))
	for _, txin := range txins {
		mulTxIn := &apitypes.MultichainTxIn{
			Coinbase: txin.Coinbase,
			Txid:     txin.Txid,
			Vout:     txin.Vout,
			Sequence: txin.Sequence,
			Witness:  txin.Witness,
		}
		if txin.ScriptSig != nil {
			mulTxIn.ScriptSig = &apitypes.MultichainScriptSig{
				Asm: txin.ScriptSig.Asm,
				Hex: txin.ScriptSig.Hex,
			}
		}
		allTxIn = append(allTxIn, mulTxIn)
	}
	return allTxIn, nil
}

// GetLTCAllTxOut gets all Litecoin transaction outputs for a transaction.
func (pgb *ChainDB) GetLTCAllTxOut(txid string) ([]*apitypes.MultichainTxOut, error) {
	txhash, err := ltc_chainhash.NewHashFromStr(txid)
	if err != nil {
		return nil, err
	}
	tx, err := ltcrpcutils.WithTimeout(func() (*ltcjson.TxRawResult, error) {
		return pgb.LtcClient.GetRawTransactionVerbose(txhash)
	})
	if err != nil {
		log.Warnf("[LTC] Unknown transaction %s", txid)
		return nil, err
	}
	txouts := tx.Vout
	allTxOut := make([]*apitypes.MultichainTxOut, 0, len(txouts))
	for i := range txouts {
		spk := &tx.Vout[i].ScriptPubKey
		allTxOut = append(allTxOut, &apitypes.MultichainTxOut{
			Value: txouts[i].Value,
			N:     txouts[i].N,
			ScriptPubKeyDecoded: apitypes.MultichainScriptPubKey{
				Asm:       spk.Asm,
				Hex:       spk.Hex,
				ReqSigs:   spk.ReqSigs,
				Type:      spk.Type,
				Addresses: spk.Addresses,
			},
		})
	}
	return allTxOut, nil
}

// GetLTCChainParams returns the Litecoin chain parameters.
func (pgb *ChainDB) GetLTCChainParams() *ltc_chaincfg.Params {
	return pgb.ltcChainParams
}

// GetLTCBlockVerbose fetches the *ltcjson.GetBlockVerboseResult for a given block height.
func (pgb *ChainDB) GetLTCBlockVerbose(idx int) *ltcjson.GetBlockVerboseResult {
	block := ltcrpcutils.GetBlockVerbose(pgb.LtcClient, int64(idx))
	return block
}

// GetLTCBlockVerboseTx fetches the *ltcjson.GetBlockVerboseTxResult for a given block height.
func (pgb *ChainDB) GetLTCBlockVerboseTx(idx int) *ltcjson.GetBlockVerboseTxResult {
	block := ltcrpcutils.GetBlockVerboseTx(pgb.LtcClient, int64(idx))
	return block
}

// makeLTCExplorerBlockBasicFromTxResult creates a BlockBasic from LTC block data with params.
func makeLTCExplorerBlockBasicFromTxResult(data *ltcjson.GetBlockVerboseTxResult, params *ltc_chaincfg.Params) *exptypes.BlockBasic {
	total := float64(0)
	for _, tx := range data.RawTx {
		for _, vout := range tx.Vout {
			total += vout.Value
		}
	}
	numReg := len(data.RawTx)

	block := &exptypes.BlockBasic{
		Height:         data.Height,
		Hash:           data.Hash,
		Version:        data.Version,
		Size:           data.Size,
		Valid:          true,
		MainChain:      true,
		Transactions:   numReg,
		TxCount:        uint32(numReg),
		BlockTime:      exptypes.NewTimeDefFromUNIX(data.Time),
		BlockTimeUnix:  data.Time,
		FormattedBytes: humanize.Bytes(uint64(data.Size)),
		Total:          total,
	}

	return block
}

// makeLTCExplorerBlockBasic creates a BlockBasic from LTC block data.
func makeLTCExplorerBlockBasic(data *ltcjson.GetBlockVerboseTxResult) *exptypes.BlockBasic {
	total := float64(0)
	for _, tx := range data.RawTx {
		for _, vout := range tx.Vout {
			total += vout.Value
		}
	}
	numReg := len(data.RawTx)

	block := &exptypes.BlockBasic{
		Height:         data.Height,
		Hash:           data.Hash,
		Version:        data.Version,
		Size:           data.Size,
		Valid:          true,
		MainChain:      true,
		Transactions:   numReg,
		TxCount:        uint32(numReg),
		BlockTime:      exptypes.NewTimeDefFromUNIX(data.Time),
		BlockTimeUnix:  data.Time,
		FormattedBytes: humanize.Bytes(uint64(data.Size)),
		Total:          total,
	}

	return block
}

// prefetchLTCPrevTxs concurrently fetches all unique previous transactions
// referenced by inputs across all transactions in a block. Returns a cache
// map of txid -> TxRawResult for fast lookup, eliminating the N+1 RPC pattern.
func prefetchLTCPrevTxs(client *ltcClient.Client, rawTxs []ltcjson.TxRawResult) map[string]*ltcjson.TxRawResult {
	cache := make(map[string]*ltcjson.TxRawResult)

	// Index block transactions by txid (intra-block references)
	for i := range rawTxs {
		cache[rawTxs[i].Txid] = &rawTxs[i]
	}

	// Collect unique prevout txids not already in the block
	needed := make(map[string]struct{})
	for _, tx := range rawTxs {
		for _, vin := range tx.Vin {
			if vin.IsCoinBase() || vin.Txid == "" {
				continue
			}
			if _, ok := cache[vin.Txid]; !ok {
				needed[vin.Txid] = struct{}{}
			}
		}
	}

	if len(needed) == 0 {
		return cache
	}

	// Fetch concurrently with limited workers
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)

	for txid := range needed {
		wg.Add(1)
		go func(tid string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			txResult, err := ltcrpcutils.GetRawTransactionByTxidStr(client, tid)
			if err == nil && txResult != nil {
				mu.Lock()
				cache[tid] = txResult
				mu.Unlock()
			}
		}(txid)
	}

	wg.Wait()
	return cache
}

// makeLTCExplorerTxBasic creates a TxBasic from LTC transaction data.
// prevTxCache is an optional pre-fetched cache of previous transactions; if nil, RPCs are made per-input.
func makeLTCExplorerTxBasic(client *ltcClient.Client, data *ltcjson.TxRawResult, msgTx *ltcwire.MsgTx, prevTxCache map[string]*ltcjson.TxRawResult) *exptypes.TxBasic {
	isCoinbase := len(data.Vin) > 0 && data.Vin[0].IsCoinBase()
	sent := txhelpers.TotalLTCVout(data.Vout).ToBTC()
	fees := float64(0)
	if !isCoinbase {
		spent := LTCTotalTotalSpentVin(client, msgTx, prevTxCache)
		spentCoin := utils.MultichainAtomicToCoin(spent, mutilchain.TYPELTC)
		fees = spentCoin - sent
	}
	tx := &exptypes.TxBasic{
		TxID:          data.Txid,
		Version:       int32(data.Version),
		FormattedSize: humanize.Bytes(uint64(len(data.Hex) / 2)),
		Total:         sent,
		Coinbase:      isCoinbase,
		FeeCoin:       fees,
	}
	return tx
}

// LTCTotalTotalSpentVin calculates the total spent value from transaction inputs.
// prevTxCache is an optional pre-fetched cache; if nil or missing an entry, falls back to RPC.
func LTCTotalTotalSpentVin(client *ltcClient.Client, msgTx *ltcwire.MsgTx, prevTxCache map[string]*ltcjson.TxRawResult) int64 {
	var spent int64
	for _, txin := range msgTx.TxIn {
		txid := txin.PreviousOutPoint.Hash.String()
		var txInResult *ltcjson.TxRawResult
		if prevTxCache != nil {
			txInResult = prevTxCache[txid]
		}
		if txInResult == nil {
			var txinErr error
			txInResult, txinErr = ltcrpcutils.GetRawTransactionByTxidStr(client, txid)
			if txinErr != nil {
				continue
			}
		}
		unitAmount := dbtypes.GetLTCValueInFromRawTransction(txInResult, txin)
		spent += unitAmount
	}
	return spent
}

// trimmedLTCTxInfoFromMsgTx creates a TrimmedTxInfo from LTC transaction data.
func trimmedLTCTxInfoFromMsgTx(client *ltcClient.Client, txraw *ltcjson.TxRawResult, msgTx *ltcwire.MsgTx, params *ltc_chaincfg.Params, prevTxCache map[string]*ltcjson.TxRawResult) *exptypes.TrimmedTxInfo {
	txBasic := makeLTCExplorerTxBasic(client, txraw, msgTx, prevTxCache)

	tx := &exptypes.TrimmedTxInfo{
		TxBasic:   txBasic,
		VinCount:  len(txraw.Vin),
		VoutCount: len(txraw.Vout),
	}
	return tx
}

// GetLTCContractInfo extracts atomic swap contract information from a LTC spend transaction.
func (pgb *ChainDB) GetLTCContractInfo(spendTx string, spendVin uint32) (contractAddr, recipientAddr,
	refundAddr string, contractScript []byte, isRefund bool, err error) {
	var tx *ltcutil.Tx
	tx, err = pgb.GetLTCTransactionByHash(spendTx)
	if err != nil {
		return
	}
	if len(tx.MsgTx().TxIn) <= int(spendVin) {
		err = fmt.Errorf("LTC: spend vin invalid")
		return
	}
	var contractData *ltctxhelper.AtomicSwapContractPushes
	contractData, contractScript, _, isRefund, err = ltctxhelper.ExtractSwapDataFromWitness(tx.MsgTx().TxIn[spendVin].Witness, pgb.ltcChainParams)
	if err != nil {
		return
	}
	contractAddr = contractData.ContractAddress.String()
	recipientAddr = contractData.RecipientAddress.String()
	refundAddr = contractData.RefundAddress.String()
	return
}

// GetLTCExplorerBlock returns detailed block information for the explorer.
func (pgb *ChainDB) GetLTCExplorerBlock(hash string) *exptypes.BlockInfo {
	pgb.ltcLastExplorerBlock.Lock()
	if pgb.ltcLastExplorerBlock.hash == hash {
		defer pgb.ltcLastExplorerBlock.Unlock()
		return pgb.ltcLastExplorerBlock.blockInfo
	}
	pgb.ltcLastExplorerBlock.Unlock()

	data := pgb.GetLTCBlockVerboseTxByHash(hash)
	if data == nil {
		log.Error("Unable to get block for block hash " + hash)
		return nil
	}

	b := makeLTCExplorerBlockBasicFromTxResult(data, pgb.ltcChainParams)

	// Explorer Block Info
	block := &exptypes.BlockInfo{
		BlockBasic:    b,
		Confirmations: data.Confirmations,
		PoWHash:       b.Hash,
		Nonce:         data.Nonce,
		Bits:          data.Bits,
		Difficulty:    data.Difficulty,
		PreviousHash:  data.PreviousHash,
		NextHash:      data.NextHash,
	}

	// Pre-fetch all previous transactions referenced by inputs in this block concurrently.
	prevTxCache := prefetchLTCPrevTxs(pgb.LtcClient, data.RawTx)

	txs := make([]*exptypes.TrimmedTxInfo, 0, block.Transactions)
	txids := make([]string, 0)
	totalSent := float64(0)
	totalFees := float64(0)
	totalNumVins := int64(0)
	totalNumVouts := int64(0)
	for i := range data.RawTx {
		tx := &data.RawTx[i]
		msgTx, err := txhelpers.MsgLTCTxFromHex(tx.Hex, int32(tx.Version))
		if err != nil {
			log.Errorf("Unknown transaction %s: %v", tx.Txid, err)
			return nil
		}
		exptx := trimmedLTCTxInfoFromMsgTx(pgb.LtcClient, tx, msgTx, pgb.ltcChainParams, prevTxCache)
		totalSent += exptx.Total
		totalFees += exptx.FeeCoin
		totalNumVins += int64(exptx.VinCount)
		totalNumVouts += int64(exptx.VoutCount)
		txs = append(txs, exptx)
		txids = append(txids, exptx.TxID)
	}
	block.Tx = txs
	block.Txids = txids
	sortTx := func(txs []*exptypes.TrimmedTxInfo) {
		sort.Slice(txs, func(i, j int) bool {
			return txs[i].Total > txs[j].Total
		})
	}
	sortTx(block.Tx)
	block.TotalSent = totalSent
	block.Fees = utils.BTCToSatoshi(totalFees)
	block.TotalInputs = totalNumVins
	block.TotalOutputs = totalNumVouts

	pgb.ltcLastExplorerBlock.Lock()
	pgb.ltcLastExplorerBlock.hash = hash
	pgb.ltcLastExplorerBlock.blockInfo = block
	pgb.ltcLastExplorerBlock.difficulties = make(map[int64]float64) // used by the Difficulty method
	pgb.ltcLastExplorerBlock.Unlock()
	swapsData, err := pgb.GetMultichainBlockSwapGroupFullData(block.Txids, mutilchain.TYPELTC)
	if err != nil {
		log.Errorf("%s: Get swaps full data for block txs failed: %v", mutilchain.TYPELTC, err)
		block.GroupSwaps = make([]*dbtypes.AtomicSwapFullData, 0)
	} else {
		block.GroupSwaps = swapsData
	}
	return block
}

// GetLTCExplorerBlocks creates a slice of exptypes.BlockBasic beginning at start
// and decreasing in block height to end, not including end.
func (pgb *ChainDB) GetLTCExplorerBlocks(start int, end int) []*exptypes.BlockBasic {
	if start < end {
		return nil
	}
	count := start - end
	summaries := make([]*exptypes.BlockBasic, count)

	// Fetch all blocks concurrently to avoid sequential RPC bottleneck.
	var wg sync.WaitGroup
	for idx := 0; idx < count; idx++ {
		wg.Add(1)
		go func(slot, height int) {
			defer wg.Done()
			data := pgb.GetLTCBlockVerboseTx(height)
			block := new(exptypes.BlockBasic)
			if data != nil {
				block = makeLTCExplorerBlockBasic(data)
			}
			summaries[slot] = block
		}(idx, start-idx)
	}
	wg.Wait()
	return summaries
}

// LtcTxResult returns the raw LTC transaction result and block height.
func (pgb *ChainDB) LtcTxResult(txhash *ltc_chainhash.Hash) (*ltcjson.TxRawResult, int64, error) {
	txraw, err := ltcrpcutils.WithTimeout(func() (*ltcjson.TxRawResult, error) {
		return pgb.LtcClient.GetRawTransactionVerbose(txhash)
	})
	if err != nil {
		return nil, 0, fmt.Errorf("LTC: GetRawTransactionVerbose failed for %v: %w", txhash, err)
	}
	var blockHeight int64
	if txraw.BlockHash != "" {
		blockHeight, _ = pgb.GetMutilchainBlockHeightByHash(txraw.BlockHash, mutilchain.TYPELTC)
	}
	return txraw, blockHeight, nil
}

// GetLTCExplorerTx returns detailed transaction information for the explorer.
func (pgb *ChainDB) GetLTCExplorerTx(txid string) *exptypes.TxInfo {
	if pgb.LtcClient == nil {
		return &exptypes.TxInfo{}
	}
	txhash, err := ltc_chainhash.NewHashFromStr(txid)
	if err != nil {
		log.Errorf("Invalid transaction hash %s", txid)
		return nil
	}

	txraw, blockHeight, err := pgb.LtcTxResult(txhash)
	if err != nil {
		log.Errorf("Mutilchain Tx Info: %v", err)
		return nil
	}

	msgTx, err := txhelpers.LTCMsgTxFromHex(txraw.Hex, int32(txraw.Version))
	if err != nil {
		log.Errorf("Cannot create MsgTx for tx %v: %v", txhash, err)
		return nil
	}

	txBasic := makeLTCExplorerTxBasic(pgb.LtcClient, txraw, msgTx, nil)
	tx := &exptypes.TxInfo{
		TxBasic:       txBasic,
		BlockHash:     txraw.BlockHash,
		BlockHeight:   blockHeight,
		Confirmations: int64(txraw.Confirmations),
		Time:          exptypes.NewTimeDefFromUNIX(txraw.Time),
	}

	// tree := txType stake.TxTypeRegular
	var totalVin float64
	inputs := make([]exptypes.MutilchainVin, 0, len(txraw.Vin))
	for i := range txraw.Vin {
		vin := &txraw.Vin[i]
		// The addresses are may only be obtained by decoding the previous
		// output's pkscript.
		var addresses []string
		// The vin amount is now correct in most cases, but get it from the
		// previous output anyway and compare the values for information.
		valueIn, _ := ltcutil.NewAmount(float64(vin.Vout))
		// Do not attempt to look up prevout if it is a coinbase or stakebase
		// input, which does not spend a previous output.
		prevOut := &msgTx.TxIn[i].PreviousOutPoint
		if !txhelpers.IsLTCZeroHash(prevOut.Hash) {
			// Store the vin amount for comparison.
			valueIn0 := valueIn

			addresses, valueIn, err = txhelpers.LTCOutPointAddresses(
				prevOut, pgb.LtcClient, pgb.ltcChainParams)
			if err != nil {
				log.Warnf("Failed to get outpoint address from txid: %v", err)
				continue
			}
			// See if getrawtransaction had correct vin amounts. It should
			// except for votes on side chain blocks.
			if valueIn != valueIn0 {
				log.Debugf("vin amount in: prevout RPC = %v, vin's amount = %v",
					valueIn, valueIn0)
			}
		}

		// Assemble and append this vin.
		coinIn := valueIn.ToBTC()
		totalVin += coinIn
		vinBlockHeight := int64(0)
		vinHash, err := ltc_chainhash.NewHashFromStr(vin.Txid)
		if err != nil {
			log.Errorf("LTC: Invalid vin transaction hash %s", vin.Txid)
		} else {
			txraw, err := ltcrpcutils.WithTimeout(func() (*ltcjson.TxRawResult, error) {
				return pgb.LtcClient.GetRawTransactionVerbose(vinHash)
			})
			if err != nil {
				log.Errorf("LTC: GetRawTransactionVerbose failed for %v: %w", vinHash, err)
			} else {
				vinBlockHeight, _ = pgb.GetMutilchainBlockHeightByHash(txraw.BlockHash, mutilchain.TYPELTC)
			}
		}
		inputs = append(inputs, exptypes.MutilchainVin{
			Txid:            vin.Txid,
			Coinbase:        vin.Coinbase,
			Vout:            vin.Vout,
			Sequence:        vin.Sequence,
			Witness:         vin.Witness,
			Addresses:       addresses,
			FormattedAmount: humanize.Commaf(coinIn),
			Index:           uint32(i),
			AmountIn:        coinIn,
			BlockHeight:     vinBlockHeight,
		})
	}
	tx.MutilchainVin = inputs

	CoinbaseMaturityInHours := (pgb.ltcChainParams.TargetTimePerBlock.Hours() * float64(pgb.ltcChainParams.CoinbaseMaturity))
	tx.MaturityTimeTill = ((float64(pgb.ltcChainParams.CoinbaseMaturity) -
		float64(tx.Confirmations)) / float64(pgb.ltcChainParams.CoinbaseMaturity)) * CoinbaseMaturityInHours

	outputs := make([]exptypes.Vout, 0, len(txraw.Vout))
	var totalVout float64
	for i, vout := range txraw.Vout {
		// Determine spent status with gettxout, including mempool.
		txout, err := ltcrpcutils.WithTimeout(func() (*ltcjson.GetTxOutResult, error) {
			return pgb.LtcClient.GetTxOut(txhash, uint32(i), true)
		})
		if err != nil {
			log.Warnf("Failed to determine if tx out is spent for output %d of tx %s: %v", i, txid, err)
		}
		var opReturn string
		var opTAdd bool
		if strings.HasPrefix(vout.ScriptPubKey.Asm, "OP_RETURN") {
			opReturn = vout.ScriptPubKey.Asm
		} else {
			opTAdd = strings.HasPrefix(vout.ScriptPubKey.Asm, "OP_TADD")
		}
		// Get a consistent script class string from dbtypes.ScriptClass.
		outputs = append(outputs, exptypes.Vout{
			Addresses:       vout.ScriptPubKey.Addresses,
			Amount:          vout.Value,
			FormattedAmount: humanize.Commaf(vout.Value),
			OP_RETURN:       opReturn,
			OP_TADD:         opTAdd,
			Spent:           txout == nil,
			Index:           vout.N,
		})
		totalVout += vout.Value
	}
	tx.Vout = outputs

	// Initialize the spending transaction slice for safety.
	tx.SpendingTxns = make([]exptypes.TxInID, len(outputs))
	tx.FeeCoin = totalVin - totalVout
	return tx
}

// LTCDifficulty returns the Litecoin difficulty for a given timestamp.
func (pgb *ChainDB) LTCDifficulty(timestamp int64) float64 {
	pgb.ltcLastExplorerBlock.Lock()
	diff, ok := pgb.ltcLastExplorerBlock.difficulties[timestamp]
	pgb.ltcLastExplorerBlock.Unlock()
	if ok {
		return diff
	}

	diff, err := RetrieveMutilchainDiff(pgb.ctx, pgb.db, timestamp, mutilchain.TYPELTC)
	if err != nil {
		log.Errorf("LTC: Unable to retrieve difficulty: %v", err)
		return -1
	}
	pgb.ltcLastExplorerBlock.Lock()
	pgb.ltcLastExplorerBlock.difficulties[timestamp] = diff
	pgb.ltcLastExplorerBlock.Unlock()
	return diff
}

// SignalLTCHeight signals all height clients about a new LTC block height.
func (pgb *ChainDB) SignalLTCHeight(height uint32) {
	for i, c := range pgb.ltcHeightClients {
		select {
		case c <- height:
		case <-time.NewTimer(time.Minute).C:
			log.Criticalf("(*LTCDBDataSaver).SignalLTCHeight: ltcHeightClients[%d] timed out. Forcing a shutdown.", i)
			pgb.shutdownDcrdata()
		}
	}
}

// GetLTCBlockHashTime returns the hash and timestamp of a LTC block by height.
func (pgb *ChainDB) GetLTCBlockHashTime(height int32) (string, int64, error) {
	blockhash, err := ltcrpcutils.WithTimeout(func() (*ltc_chainhash.Hash, error) {
		return pgb.LtcClient.GetBlockHash(int64(height))
	})
	if err != nil {
		return "", 0, err
	}
	blockRst, rstErr := ltcrpcutils.WithTimeout(func() (*ltcwire.MsgBlock, error) {
		return pgb.LtcClient.GetBlock(blockhash)
	})
	if rstErr != nil {
		return "", 0, rstErr
	}
	return blockhash.String(), blockRst.Header.Timestamp.Unix(), nil
}

// GetLTCBestBlock retrieves the best LTC block from the node.
func (pgb *ChainDB) GetLTCBestBlock() error {
	type bestBlockResult struct {
		hash   *ltc_chainhash.Hash
		height int32
	}
	result, err := ltcrpcutils.WithTimeout(func() (*bestBlockResult, error) {
		hash, height, err := pgb.LtcClient.GetBestBlock()
		if err != nil {
			return nil, err
		}
		return &bestBlockResult{hash, height}, nil
	})
	ltcTime := int64(0)
	if err != nil {
		return fmt.Errorf("Unable to get block from ltc node: %v", err)
	}
	ltcHash, ltcHeight := result.hash, result.height
	blockhash, err := ltcrpcutils.WithTimeout(func() (*ltc_chainhash.Hash, error) {
		return pgb.LtcClient.GetBlockHash(int64(ltcHeight))
	})
	if err == nil {
		blockRst, rstErr := ltcrpcutils.WithTimeout(func() (*ltcjson.GetBlockVerboseResult, error) {
			return pgb.LtcClient.GetBlockVerbose(blockhash)
		})
		if rstErr == nil {
			ltcTime = blockRst.Time
		}
	}
	//create bestblock object
	bestBlock := &MutilchainBestBlock{
		Height: int64(ltcHeight),
		Hash:   ltcHash.String(),
		Time:   ltcTime,
	}
	pgb.LtcBestBlock = bestBlock
	return nil
}
