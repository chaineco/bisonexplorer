// Copyright (c) 2018-2021, The Decred developers
// Copyright (c) 2017, The dcrdata developers
// See LICENSE for details.

package dcrpg

import (
	"bytes"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/decred/dcrdata/db/dcrpg/v8/internal/mutilchainquery"
	apitypes "github.com/decred/dcrdata/v8/api/types"
	"github.com/decred/dcrdata/v8/db/dbtypes"
	"github.com/decred/dcrdata/v8/mutilchain"
	"github.com/decred/dcrdata/v8/mutilchain/ltcrpcutils"
	"github.com/decred/dcrdata/v8/txhelpers"
	"github.com/decred/dcrdata/v8/txhelpers/ltctxhelper"
	"github.com/ltcsuite/ltcd/chaincfg"
	ltcchainhash "github.com/ltcsuite/ltcd/chaincfg/chainhash"
	"github.com/ltcsuite/ltcd/ltcutil"
	ltcClient "github.com/ltcsuite/ltcd/rpcclient"
	"github.com/ltcsuite/ltcd/wire"
)

func (db *ChainDB) SyncLTCChainDBAsync(res chan dbtypes.SyncResult,
	client *ltcClient.Client, quit chan struct{}, updateAllAddresses, newIndexes bool) {
	if db == nil {
		res <- dbtypes.SyncResult{
			Height: -1,
			Error:  fmt.Errorf("ChainDB LTC (psql) disabled"),
		}
		return
	}
	height, err := db.SyncLTCChainDB(client, quit, newIndexes, updateAllAddresses)
	res <- dbtypes.SyncResult{
		Height: height,
		Error:  err,
	}
}

func (db *ChainDB) SyncLTCChainDB(client *ltcClient.Client, quit chan struct{},
	newIndexes, updateAllAddresses bool) (int64, error) {
	// Get chain servers's best block
	type ltcBestBlockResult struct {
		hash   *ltcchainhash.Hash
		height int32
	}
	bestBlockRes, err := ltcrpcutils.WithTimeout(func() (*ltcBestBlockResult, error) {
		hash, height, err := ltcrpcutils.GetBestBlock(client)
		if err != nil {
			return nil, err
		}
		return &ltcBestBlockResult{hash, int32(height)}, nil
	})
	if err != nil {
		return -1, fmt.Errorf("GetBestBlock LTC failed: %v", err)
	}
	nodeHeight := bestBlockRes.height
	// Total and rate statistics
	var totalTxs, totalVins, totalVouts int64
	var lastTxs, lastVins, lastVouts int64
	tickTime := 20 * time.Second
	ticker := time.NewTicker(tickTime)
	startTime := time.Now()
	o := sync.Once{}
	speedReporter := func() {
		ticker.Stop()
		totalElapsed := time.Since(startTime).Seconds()
		if int64(totalElapsed) == 0 {
			return
		}
		totalVoutPerSec := totalVouts / int64(totalElapsed)
		totalTxPerSec := totalTxs / int64(totalElapsed)
		log.Infof("Avg. speed: %d tx/s, %d vout/s", totalTxPerSec, totalVoutPerSec)
	}
	speedReport := func() { o.Do(speedReporter) }
	defer speedReport()

	startingHeight, err := db.MutilchainHeightDB(mutilchain.TYPELTC)
	lastBlock := int64(startingHeight)
	if err != nil {
		if err == sql.ErrNoRows {
			lastBlock = -1
			log.Info("blocks table is empty, starting fresh.")
		} else {
			return -1, fmt.Errorf("RetrieveBestBlockHeight: %v", err)
		}
	}

	// Remove indexes/constraints before bulk import
	blocksToSync := int64(nodeHeight) - lastBlock
	reindexing := newIndexes || blocksToSync > int64(nodeHeight)/2
	if reindexing {
		log.Info("LTC: Large bulk load: Removing indexes and disabling duplicate checks.")
		err = db.DeindexAllMutilchain(mutilchain.TYPELTC)
		if err != nil && !strings.Contains(err.Error(), "does not exist") && !strings.Contains(err.Error(), "不存在") {
			return lastBlock, err
		}
		db.MutilchainEnableDuplicateCheckOnInsert(false, mutilchain.TYPELTC)
	} else {
		db.MutilchainEnableDuplicateCheckOnInsert(true, mutilchain.TYPELTC)
	}

	// Start rebuilding
	startHeight := lastBlock + 1
	for ib := startHeight; ib <= int64(nodeHeight); ib++ {
		// check for quit signal
		select {
		case <-quit:
			log.Infof("Rescan cancelled at height %d.", ib)
			return ib - 1, nil
		default:
		}

		if (ib-1)%ltcRescanLogBlockChunk == 0 || ib == startHeight {
			if ib == 0 {
				log.Infof("Scanning genesis block.")
			} else {
				endRangeBlock := ltcRescanLogBlockChunk * (1 + (ib-1)/ltcRescanLogBlockChunk)
				if endRangeBlock > int64(nodeHeight) {
					endRangeBlock = int64(nodeHeight)
				}
				log.Infof("Processing blocks %d to %d...", ib, endRangeBlock)
			}
		}
		select {
		case <-ticker.C:
			blocksPerSec := float64(ib-lastBlock) / tickTime.Seconds()
			txPerSec := float64(totalTxs-lastTxs) / tickTime.Seconds()
			vinsPerSec := float64(totalVins-lastVins) / tickTime.Seconds()
			voutPerSec := float64(totalVouts-lastVouts) / tickTime.Seconds()
			log.Infof("(%3d blk/s,%5d tx/s,%5d vin/sec,%5d vout/s)", int64(blocksPerSec),
				int64(txPerSec), int64(vinsPerSec), int64(voutPerSec))
			lastBlock, lastTxs = ib, totalTxs
			lastVins, lastVouts = totalVins, totalVouts
		default:
		}

		block, blockHash, err := ltcrpcutils.GetBlock(ib, client)
		if err != nil {
			return ib - 1, fmt.Errorf("GetBlock failed (%s): %v", blockHash, err)
		}
		var numVins, numVouts int64
		if numVins, numVouts, err = db.StoreLTCBlock(client, block.MsgBlock(), true, !updateAllAddresses); err != nil {
			return ib - 1, fmt.Errorf("LTC StoreBlock failed: %v", err)
		}
		totalVins += numVins
		totalVouts += numVouts

		numRTx := int64(len(block.Transactions()))
		totalTxs += numRTx
		// totalRTxs += numRTx
		// totalSTxs += numSTx

		// update height, the end condition for the loop
		bestBlockUpd, err := ltcrpcutils.WithTimeout(func() (*ltcBestBlockResult, error) {
			hash, height, err := ltcrpcutils.GetBestBlock(client)
			if err != nil {
				return nil, err
			}
			return &ltcBestBlockResult{hash, int32(height)}, nil
		})
		if err != nil {
			return ib, fmt.Errorf("GetBestBlock failed: %v", err)
		}
		nodeHeight = bestBlockUpd.height
	}

	speedReport()

	if reindexing || newIndexes {
		if err = db.IndexAllMutilchain(mutilchain.TYPELTC); err != nil {
			return int64(nodeHeight), fmt.Errorf("IndexAllMutilchain failed: %v", err)
		}
		if !updateAllAddresses {
			err = db.IndexMutilchainAddressesTable(mutilchain.TYPELTC)
		}
	}

	if updateAllAddresses {
		// Remove existing indexes not on funding txns
		_ = db.DeindexMutilchainAddressesTable(mutilchain.TYPELTC) // ignore errors for non-existent indexes
		log.Infof("Populating spending tx info in address table...")
		numAddresses, err := db.UpdateMutilchainSpendingInfoInAllAddresses(mutilchain.TYPELTC)
		if err != nil {
			log.Errorf("UpdateSpendingInfoInAllAddresses for LTC FAILED: %v", err)
		}
		log.Infof("Updated %d rows of address table", numAddresses)
		if err = db.IndexMutilchainAddressesTable(mutilchain.TYPELTC); err != nil {
			log.Errorf("IndexLTCAddressTable FAILED: %v", err)
		}
	}
	db.MutilchainEnableDuplicateCheckOnInsert(true, mutilchain.TYPELTC)
	log.Infof("Sync finished at height %d. Delta: %d blocks, %d transactions, %d ins, %d outs",
		nodeHeight, int64(nodeHeight)-startHeight+1, totalTxs, totalVins, totalVouts)

	return int64(nodeHeight), err
}

func (pgb *ChainDB) SyncLast20LTCBlocks(nodeHeight int32) error {
	if !pgb.ltc20BlocksSyncMtx.TryLock() {
		log.Debugf("LTC last 20 blocks sync already running, skipping")
		return nil
	}
	defer pgb.ltc20BlocksSyncMtx.Unlock()
	// Total and rate statistics
	var totalTxs, totalVins, totalVouts int64
	startHeight := nodeHeight - 25
	//Delete all blocks data and blocks related data older than start block
	//Delete vins, vouts
	err := DeleteVinsOfOlderThan20Blocks(pgb.ctx, pgb.db, mutilchain.TYPELTC, int64(startHeight))
	if err != nil {
		return err
	}
	err = DeleteVoutsOfOlderThan20Blocks(pgb.ctx, pgb.db, mutilchain.TYPELTC, int64(startHeight))
	if err != nil {
		return err
	}
	// Start rebuilding
	for ib := startHeight; ib <= nodeHeight; ib++ {
		block, blockHash, err := ltcrpcutils.GetBlock(int64(ib), pgb.LtcClient)
		if err != nil {
			return fmt.Errorf("LTC: GetBlock failed (%s): %v", blockHash, err)
		}
		var numVins, numVouts int64
		//check exist on DB
		exist, err := CheckBlockExistOnDB(pgb.ctx, pgb.db, mutilchain.TYPELTC, int64(ib))
		if err != nil {
			return fmt.Errorf("LTC: Check exist block (%d) on db failed: %v", ib, err)
		}
		// if exist
		if exist {
			// sync and update for block
			dbBlockInfo, err := RetrieveBlockInfo(pgb.ctx, pgb.db, mutilchain.TYPELTC, int64(ib))
			if err != nil {
				return fmt.Errorf("LTC: Get block detail (%d) on db failed: %v", ib, err)
			}
			// if have summary info, ignore
			if dbBlockInfo.TxCount > 0 || dbBlockInfo.Inputs > 0 || dbBlockInfo.Outputs > 0 {
				continue
			}
			// if don't have any info, update summary info
			if numVins, numVouts, err = pgb.UpdateStoreLTCBlockInfo(pgb.LtcClient, block.MsgBlock(), int64(ib), false); err != nil {
				return fmt.Errorf("LTC UpdateStoreBlock failed: %v", err)
			}
		} else {
			if numVins, numVouts, err = pgb.StoreLTCBlockInfo(pgb.LtcClient, block.MsgBlock(), int64(ib), false); err != nil {
				return fmt.Errorf("LTC StoreBlock failed: %v", err)
			}
		}
		totalVins += numVins
		totalVouts += numVouts
		numRTx := int64(len(block.Transactions()))
		totalTxs += numRTx
		// update height, the end condition for the loop
		type ltcBestBlockResult struct {
			hash   *ltcchainhash.Hash
			height int32
		}
		bestBlockUpd, err := ltcrpcutils.WithTimeout(func() (*ltcBestBlockResult, error) {
			hash, height, err := ltcrpcutils.GetBestBlock(pgb.LtcClient)
			if err != nil {
				return nil, err
			}
			return &ltcBestBlockResult{hash, int32(height)}, nil
		})
		if err != nil {
			return fmt.Errorf("LTC: GetBestBlock failed: %v", err)
		}
		nodeHeight = bestBlockUpd.height
	}
	log.Debugf("LTC: Sync last 20 Blocks of LTC finished at height %d. Delta: %d blocks, %d transactions, %d ins, %d outs",
		nodeHeight, int64(nodeHeight)-int64(startHeight)+1, totalTxs, totalVins, totalVouts)
	return err
}

func (pgb *ChainDB) UpdateStoreLTCBlockInfo(client *ltcClient.Client, msgBlock *wire.MsgBlock, height int64, allSync bool) (numVins int64, numVouts int64, err error) {
	log.Infof("LTC: Start update sync block info. Height: %d", height)
	// Convert the wire.MsgBlock to a dbtypes.Block
	dbBlock := dbtypes.MsgLTCBlockToDBBlock(client, msgBlock, pgb.ltcChainParams)
	// regular transactions
	resChanReg := make(chan storeTxnsResult)
	go func() {
		resChanReg <- pgb.getLTCTxnsInfo(client, dbBlock, msgBlock,
			pgb.ltcChainParams)
	}()

	errReg := <-resChanReg
	dbBlock.NumVins = uint32(errReg.numVins)
	dbBlock.NumVouts = uint32(errReg.numVouts)
	dbBlock.Fees = uint64(errReg.fees)
	numVins = errReg.numVins
	numVouts = errReg.numVouts
	dbBlock.TotalSent = uint64(errReg.totalSent)
	// Store the block now that it has all it's transaction PK IDs
	_, err = UpdateMutilchainBlock(pgb.db, dbBlock, true, mutilchain.TYPELTC)
	if err != nil {
		log.Errorf("LTC: UpdateBlock failed: %v", err)
		return
	}
	log.Infof("LTC: Finish update sync block info. Height: %d", height)
	return
}

// StoreLTCBlockInfo. Store only blockinfo. For get summary info of block (Not use when sync blockchain data)
func (pgb *ChainDB) StoreLTCBlockInfo(client *ltcClient.Client, msgBlock *wire.MsgBlock, height int64, allSync bool) (numVins int64, numVouts int64, err error) {
	log.Infof("LTC: Start sync block info. Height: %d", height)
	// Convert the wire.MsgBlock to a dbtypes.Block
	dbBlock := dbtypes.MsgLTCBlockToDBBlock(client, msgBlock, pgb.ltcChainParams)
	dbtx, err := pgb.db.Begin()
	if err != nil {
		err = fmt.Errorf("LTC: Begin sql tx: %v", err)
		log.Error(err)
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = dbtx.Rollback()
		}
	}()
	// regular transactions
	resChanReg := make(chan storeTxnsResult)
	go func() {
		resChanReg <- pgb.getLTCTxnsInfo(client, dbBlock, msgBlock,
			pgb.ltcChainParams)
	}()

	errReg := <-resChanReg
	dbBlock.NumVins = uint32(errReg.numVins)
	dbBlock.NumVouts = uint32(errReg.numVouts)
	dbBlock.Fees = uint64(errReg.fees)
	numVins = errReg.numVins
	numVouts = errReg.numVouts
	dbBlock.TotalSent = uint64(errReg.totalSent)
	// Store the block now that it has all it's transaction PK IDs
	_, err = InsertMutilchainBlock(dbtx, dbBlock, true, pgb.ltcDupChecks, mutilchain.TYPELTC)
	if err != nil {
		log.Error("LTC: InsertBlock:", err)
		return
	}
	// Commit the tx
	if cerr := dbtx.Commit(); cerr != nil {
		err = fmt.Errorf("LTC: commit tx: %v", cerr)
		log.Error(err)
		return
	}
	committed = true
	log.Infof("LTC: Finish sync block info. Height: %d", height)
	return
}

// StoreBlock processes the input wire.MsgBlock, and saves to the data tables.
// The number of vins, and vouts stored are also returned.
func (pgb *ChainDB) StoreLTCBlock(client *ltcClient.Client, msgBlock *wire.MsgBlock,
	isValid, updateAddressesSpendingInfo bool) (numVins int64, numVouts int64, err error) {
	// Convert the wire.MsgBlock to a dbtypes.Block
	dbBlock := dbtypes.MsgLTCBlockToDBBlock(client, msgBlock, pgb.ltcChainParams)
	// Extract transactions and their vouts, and insert vouts into their pg table,
	// returning their DB PKs, which are stored in the corresponding transaction
	// data struct. Insert each transaction once they are updated with their
	// vouts' IDs, returning the transaction PK ID, which are stored in the
	// containing block data struct.
	dbtx, err := pgb.db.Begin()
	if err != nil {
		err = fmt.Errorf("BTC: Begin sql tx: %v", err)
		log.Error(err)
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = dbtx.Rollback()
		}
	}()
	// regular transactions
	txRes := pgb.storeLTCTxns(dbtx, client, dbBlock, msgBlock,
		pgb.ltcChainParams, &dbBlock.TxDbIDs, updateAddressesSpendingInfo, true, true)
	if txRes.err != nil {
		err = txRes.err
		log.Errorf("LTC: storeLTCTxns failed: %v", err)
		return
	}
	numVins = txRes.numVins
	numVouts = txRes.numVouts
	dbBlock.NumVins = uint32(numVins)
	dbBlock.NumVouts = uint32(numVouts)
	if txRes.fees < 0 {
		dbBlock.Fees = 0
	} else {
		dbBlock.Fees = uint64(txRes.fees)
	}
	if txRes.totalSent < 0 {
		dbBlock.TotalSent = 0
	} else {
		dbBlock.TotalSent = uint64(txRes.totalSent)
	}
	// Store the block now that it has all it's transaction PK IDs
	var blockDbID uint64
	blockDbID, err = InsertMutilchainBlock(dbtx, dbBlock, isValid, pgb.ltcDupChecks, mutilchain.TYPELTC)
	if err != nil {
		log.Error("InsertBlock:", err)
		return
	}
	pgb.ltcLastBlock[msgBlock.BlockHash()] = blockDbID

	// pgb.LtcBestBlock = &MutilchainBestBlock{
	// 	Height: int64(dbBlock.Height),
	// 	Hash:   dbBlock.Hash,
	// }

	err = InsertMutilchainBlockPrevNext(dbtx, blockDbID, dbBlock.Hash,
		dbBlock.PreviousHash, "", mutilchain.TYPELTC)
	if err != nil && err != sql.ErrNoRows {
		log.Error("InsertBlockPrevNext:", err)
		return
	}

	pgb.LtcBestBlock.Mtx.Lock()
	pgb.LtcBestBlock.Height = int64(dbBlock.Height)
	pgb.LtcBestBlock.Hash = dbBlock.Hash
	pgb.LtcBestBlock.Mtx.Unlock()

	// Update last block in db with this block's hash as it's next. Also update
	// isValid flag in last block if votes in this block invalidated it.
	lastBlockHash := msgBlock.Header.PrevBlock
	lastBlockDbID, ok := pgb.ltcLastBlock[lastBlockHash]
	if ok {
		log.Infof("LTC: Setting last block %s. Height: %d", lastBlockHash, dbBlock.Height)
		err = UpdateMutilchainLastBlock(dbtx, lastBlockDbID, false, mutilchain.TYPELTC)
		if err != nil {
			log.Error("UpdateLastBlock:", err)
			return
		}
		err = UpdateMutilchainBlockNext(dbtx, lastBlockDbID, dbBlock.Hash, mutilchain.TYPELTC)
		if err != nil {
			log.Error("UpdateBlockNext:", err)
			return
		}
	}
	// Commit the tx
	if cerr := dbtx.Commit(); cerr != nil {
		err = fmt.Errorf("LTC: commit tx: %v", cerr)
		log.Error(err)
		return
	}
	committed = true
	return
}

func (pgb *ChainDB) storeLTCTxns(sqlTx *sql.Tx, client *ltcClient.Client, block *dbtypes.Block, msgBlock *wire.MsgBlock,
	chainParams *chaincfg.Params, TxDbIDs *[]uint64,
	updateAddressesSpendingInfo, onlyTxInsert, allSync bool) storeTxnsResult {
	dbTransactions, dbTxVouts, dbTxVins := dbtypes.ExtractLTCBlockTransactions(client, block,
		msgBlock, chainParams)
	var txRes storeTxnsResult
	dbAddressRows := make([][]dbtypes.MutilchainAddressRow, len(dbTransactions))
	var totalAddressRows int
	var err error
	for it, dbtx := range dbTransactions {
		dbtx.VoutDbIds, dbAddressRows[it], err = InsertMutilchainVouts(sqlTx, dbTxVouts[it], pgb.ltcDupChecks, mutilchain.TYPELTC)
		if err != nil && err != sql.ErrNoRows {
			log.Error("InsertVouts:", err)
			txRes.err = err
			return txRes
		}
		totalAddressRows += len(dbAddressRows[it])
		txRes.numVouts += int64(len(dbtx.VoutDbIds))
		if err == sql.ErrNoRows || len(dbTxVouts[it]) != len(dbtx.VoutDbIds) {
			log.Warnf("Incomplete Vout insert.")
		}

		dbtx.VinDbIds, err = InsertMutilchainVins(sqlTx, dbTxVins[it], mutilchain.TYPELTC, pgb.ltcDupChecks)
		if err != nil && err != sql.ErrNoRows {
			log.Error("InsertVins:", err)
			txRes.err = err
			return txRes
		}
		for _, dbTxVout := range dbTxVouts[it] {
			txRes.totalSent += int64(dbTxVout.Value)
		}
		txRes.numVins += int64(len(dbtx.VinDbIds))
		txRes.fees += dbtx.Fees
	}

	if allSync {
		// Get the tx PK IDs for storage in the blocks table
		*TxDbIDs, err = InsertMutilchainTxns(sqlTx, dbTransactions, pgb.ltcDupChecks, mutilchain.TYPELTC)
		if err != nil && err != sql.ErrNoRows {
			log.Error("InsertTxns:", err)
			txRes.err = err
			return txRes
		}
		if !onlyTxInsert {
			return txRes
		}
		// Store tx Db IDs as funding tx in AddressRows and rearrange
		dbAddressRowsFlat := make([]*dbtypes.MutilchainAddressRow, 0, totalAddressRows)
		for it, txDbID := range *TxDbIDs {
			// Set the tx ID of the funding transactions
			for iv := range dbAddressRows[it] {
				// Transaction that pays to the address
				dba := &dbAddressRows[it][iv]
				dba.FundingTxDbID = txDbID
				// Funding tx hash, vout id, value, and address are already assigned
				// by InsertVouts. Only the funding tx DB ID was needed.
				dbAddressRowsFlat = append(dbAddressRowsFlat, dba)
			}
		}
		// Insert each new AddressRow, absent spending fields
		_, err = InsertMutilchainAddressOuts(sqlTx, dbAddressRowsFlat, mutilchain.TYPELTC, pgb.ltcDupChecks)
		if err != nil {
			log.Error("LTC: InsertAddressOuts:", err)
			txRes.err = err
			return txRes
		}
		if !updateAddressesSpendingInfo {
			return txRes
		}
		// Check the new vins and update sending tx data in Addresses table
		for it, txDbID := range *TxDbIDs {
			for iv := range dbTxVins[it] {
				// Transaction that spends an outpoint paying to >=0 addresses
				vin := &dbTxVins[it][iv]
				vinDbID := dbTransactions[it].VinDbIds[iv]
				// skip coinbase inputs
				if bytes.Equal(zeroHashStringBytes, []byte(vin.PrevTxHash)) {
					continue
				}
				var numAddressRowsSet int64
				numAddressRowsSet, err = SetMutilchainSpendingForFundingOP(sqlTx,
					vin.PrevTxHash, vin.PrevTxIndex, // funding
					txDbID, vin.TxID, vin.TxIndex, vinDbID, mutilchain.TYPELTC) // spending
				if err != nil {
					log.Errorf("SetSpendingForFundingOP: %v", err)
				}
				txRes.numAddresses += numAddressRowsSet
			}
		}
	}
	return txRes
}

func (pgb *ChainDB) getLTCTxnsInfo(client *ltcClient.Client, block *dbtypes.Block, msgBlock *wire.MsgBlock,
	chainParams *chaincfg.Params) storeTxnsResult {
	dbTransactions := dbtypes.ExtractLTCBlockTransactionsSimpleInfo(client, block,
		msgBlock, chainParams)
	var txRes storeTxnsResult
	for _, dbtx := range dbTransactions {
		txRes.numVouts += int64(dbtx.NumVout)
		txRes.totalSent += dbtx.Sent
		txRes.numVins += int64(dbtx.NumVin)
		txRes.fees += dbtx.Fees
	}
	return txRes
}

func (pgb *ChainDB) StoreLTCWholeBlock(client *ltcClient.Client, msgBlock *wire.MsgBlock, conflictCheck, updateAddressSpendInfo bool) (numVins int64, numVouts int64, err error) {
	// Convert the wire.MsgBlock to a dbtypes.Block
	dbBlock := dbtypes.MsgLTCBlockToDBBlock(client, msgBlock, pgb.ltcChainParams)
	dbtx, err := pgb.db.Begin()
	if err != nil {
		err = fmt.Errorf("BTC: Begin sql tx: %v", err)
		log.Error(err)
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = dbtx.Rollback()
		}
	}()

	txRes := pgb.storeLTCWholeTxns(dbtx, client, dbBlock, msgBlock, conflictCheck, updateAddressSpendInfo,
		pgb.ltcChainParams)
	if txRes.err != nil {
		err = txRes.err
		log.Errorf("LTC: storeLTCWholeTxns failed: %v", err)
		return
	}

	numVins = txRes.numVins
	numVouts = txRes.numVouts
	dbBlock.NumVins = uint32(numVins)
	dbBlock.NumVouts = uint32(numVouts)
	if txRes.fees < 0 {
		dbBlock.Fees = 0
	} else {
		dbBlock.Fees = uint64(txRes.fees)
	}
	if txRes.totalSent < 0 {
		dbBlock.TotalSent = 0
	} else {
		dbBlock.TotalSent = uint64(txRes.totalSent)
	}
	// Store the block now that it has all it's transaction PK IDs
	_, err = InsertMutilchainWholeBlock(dbtx, dbBlock, true, conflictCheck, mutilchain.TYPELTC)
	if err != nil {
		log.Error("LTC: InsertBlock:", err)
		return
	}

	// update synced flag for block
	// log.Infof("LTC: Set synced flag for height: %d", dbBlock.Height)
	err = UpdateMutilchainSyncedStatus(dbtx, uint64(dbBlock.Height), mutilchain.TYPELTC)
	if err != nil {
		log.Error("LTC: UpdateLastBlock:", err)
		return
	}
	// Commit the tx
	if cerr := dbtx.Commit(); cerr != nil {
		err = fmt.Errorf("LTC: commit tx: %v", cerr)
		log.Error(err)
		return
	}
	committed = true
	return
}

func (pgb *ChainDB) storeLTCWholeTxns(sqlTx *sql.Tx, client *ltcClient.Client, block *dbtypes.Block, msgBlock *wire.MsgBlock, conflictCheck, addressSpendingUpdateInfo bool,
	chainParams *chaincfg.Params) storeTxnsResult {
	dbTransactions, dbTxVouts, dbTxVins := dbtypes.ExtractLTCBlockTransactions(client, block,
		msgBlock, chainParams)
	var txRes storeTxnsResult
	dbAddressRows := make([][]dbtypes.MutilchainAddressRow, len(dbTransactions))
	var totalAddressRows int
	var err error
	for it, dbtx := range dbTransactions {
		dbtx.VoutDbIds, dbAddressRows[it], err = InsertMutilchainWholeVouts(sqlTx, dbTxVouts[it], conflictCheck, mutilchain.TYPELTC)
		if err != nil && err != sql.ErrNoRows {
			log.Error("LTC: InsertVouts:", err)
			txRes.err = err
			return txRes
		}
		totalAddressRows += len(dbAddressRows[it])
		txRes.numVouts += int64(len(dbtx.VoutDbIds))
		if err == sql.ErrNoRows || len(dbTxVouts[it]) != len(dbtx.VoutDbIds) {
			log.Warnf("LTC: Incomplete Vout insert.")
		}
		dbtx.VinDbIds, err = InsertMutilchainWholeVins(sqlTx, dbTxVins[it], mutilchain.TYPELTC, conflictCheck)
		if err != nil && err != sql.ErrNoRows {
			log.Error("LTC: InsertVins:", err)
			txRes.err = err
			return txRes
		}
		for _, dbTxVout := range dbTxVouts[it] {
			txRes.totalSent += int64(dbTxVout.Value)
		}
		txRes.numVins += int64(len(dbtx.VinDbIds))
		txRes.fees += dbtx.Fees
	}
	// Get the tx PK IDs for storage in the blocks table
	TxDbIDs, err := InsertMutilchainTxns(sqlTx, dbTransactions, conflictCheck, mutilchain.TYPELTC)
	if err != nil && err != sql.ErrNoRows {
		log.Error("LTC: InsertTxns:", err)
		txRes.err = err
		return txRes
	}
	// Store tx Db IDs as funding tx in AddressRows and rearrange
	dbAddressRowsFlat := make([]*dbtypes.MutilchainAddressRow, 0, totalAddressRows)
	for it, txDbID := range TxDbIDs {
		// Set the tx ID of the funding transactions
		for iv := range dbAddressRows[it] {
			// Transaction that pays to the address
			dba := &dbAddressRows[it][iv]
			dba.FundingTxDbID = txDbID
			// Funding tx hash, vout id, value, and address are already assigned
			// by InsertVouts. Only the funding tx DB ID was needed.
			dbAddressRowsFlat = append(dbAddressRowsFlat, dba)
		}
	}
	// Insert each new AddressRow, absent spending fields
	_, err = InsertMutilchainAddressOuts(sqlTx, dbAddressRowsFlat, mutilchain.TYPELTC, conflictCheck)
	if err != nil {
		log.Error("LTC: InsertAddressOuts:", err)
		txRes.err = err
		return txRes
	}

	if !addressSpendingUpdateInfo {
		return txRes
	}
	// Check the new vins and update sending tx data in Addresses table
	for it, txDbID := range TxDbIDs {
		for iv := range dbTxVins[it] {
			// Transaction that spends an outpoint paying to >=0 addresses
			vin := &dbTxVins[it][iv]
			vinDbID := dbTransactions[it].VinDbIds[iv]
			// skip coinbase inputs
			if bytes.Equal(zeroHashStringBytes, []byte(vin.PrevTxHash)) {
				continue
			}
			var numAddressRowsSet int64
			numAddressRowsSet, err = SetMutilchainSpendingForFundingOP(sqlTx,
				vin.PrevTxHash, vin.PrevTxIndex, // funding
				txDbID, vin.TxID, vin.TxIndex, vinDbID, mutilchain.TYPELTC) // spending
			if err != nil {
				log.Errorf("LTC: SetSpendingForFundingOP: %v", err)
			}
			txRes.numAddresses += numAddressRowsSet
		}
	}
	// set address synced flag
	txRes.addressesSynced = true
	return txRes
}

func (pgb *ChainDB) GetLTCBlockData(hash string, height int64) (*apitypes.Block24hData, bool) {
	yeserDayTimeInt := time.Now().Add(-24 * time.Hour).Unix()
	//Get block verbose
	blockData := pgb.GetLTCBlockVerboseTxByHash(hash)
	if blockData.Time < yeserDayTimeInt {
		return nil, true
	}

	block := &apitypes.Block24hData{
		BlockHash:   blockData.Hash,
		BlockHeight: blockData.Height,
		BlockTime:   dbtypes.NewTimeDef(time.Unix(blockData.Time, 0)),
		NumTx:       int64(len(blockData.RawTx)),
	}

	var totalSent, totalSpent, totalFees, numVin, numVout int64
	for _, tx := range blockData.RawTx {
		msgTx, err := txhelpers.MsgLTCTxFromHex(tx.Hex, int32(tx.Version))
		if err != nil {
			log.Errorf("LTC: Unknown transaction %s: %v", tx.Txid, err)
			break
		}
		var sent int64
		for _, txout := range msgTx.TxOut {
			sent += txout.Value
		}
		numVout += int64(len(msgTx.TxOut))
		numVin += int64(len(msgTx.TxIn))
		var isCoinbase = len(tx.Vin) > 0 && tx.Vin[0].IsCoinBase()
		spent := int64(0)
		if !isCoinbase {
			for _, txin := range msgTx.TxIn {
				//Txin
				unitAmount := int64(0)
				//Get transaction by txin
				txInResult, txinErr := ltcrpcutils.GetRawTransactionByTxidStr(pgb.LtcClient, txin.PreviousOutPoint.Hash.String())
				if txinErr == nil {
					unitAmount = dbtypes.GetLTCValueInFromRawTransction(txInResult, txin)
					spent += unitAmount
				}
			}
			totalFees += spent - sent
		}

		totalSent += sent
		totalSpent += spent
	}
	block.Fees = totalFees
	block.Spent = totalSpent
	block.Sent = totalSent
	block.NumVin = numVin
	block.NumVout = numVout
	return block, false
}

func (pgb *ChainDB) SyncOneLTCWholeBlock(client *ltcClient.Client, msgBlock *wire.MsgBlock) (err error) {
	pgb.ltcWholeSyncMtx.Lock()
	defer pgb.ltcWholeSyncMtx.Unlock()
	_, _, err = pgb.StoreLTCWholeBlock(client, msgBlock, true, true)
	return err
}

func (pgb *ChainDB) SyncLTCWholeChain(newIndexes bool) {
	pgb.ltcWholeSyncMtx.Lock()
	defer pgb.ltcWholeSyncMtx.Unlock()
	// get remaining heights
	ltcBestBlockHeight := pgb.LtcBestBlock.Height
	rows, err := pgb.db.QueryContext(pgb.ctx, mutilchainquery.CreateSelectRemainingNotSyncedHeights(mutilchain.TYPELTC), ltcBestBlockHeight)
	if err != nil {
		log.Errorf("LTC: Query remaining syncing blocks height list failed: %v", err)
		return
	}
	remaingHeights, err := getRemainingHeightsFromSqlRows(rows)
	if err != nil {
		log.Errorf("LTC: Get remaining blocks height list failed: %v", err)
		return
	}
	if len(remaingHeights) == 0 {
		log.Infof("LTC: No more blocks to synchronize with the whole daemon")
		return
	}
	log.Infof("LTC: Start sync for %d blocks. Minimum height: %d, Maximum height: %d", len(remaingHeights), remaingHeights[0], remaingHeights[len(remaingHeights)-1])

	// reindexing := newIndexes || int64(len(remaingHeights)) > pgb.LtcBestBlock.Height/20
	conflictCheck := false
	// if reindexing {
	// 	conflictCheck = false
	// 	log.Info("LTC: Large bulk load: Removing indexes")
	// 	if err = pgb.DeindexMutilchainWholeTable(mutilchain.TYPELTC); err != nil &&
	// 		!strings.Contains(err.Error(), "does not exist") &&
	// 		!strings.Contains(err.Error(), "不存在") {
	// 		log.Errorf("LTC: Deindex for multichain whole table: %v", err)
	// 		return
	// 	}
	// }

	var totalTxs, totalVins, totalVouts int
	var firstBlock, lastBlock int64
	countBlock := 0
	// iterate heights and spawn workers
	for idx, height := range remaingHeights {
		// get block (wrap with mutex if needed)
		// getBlockMu.Lock()
		block, _, err := ltcrpcutils.GetBlock(height, pgb.LtcClient)
		// getBlockMu.Unlock()
		if err != nil {
			log.Errorf("LTC: GetBlock failed (%d): %v", height, err)
			return
		}

		// store block (wrap with mutex if needed)
		// storeBlockMu.Lock()
		retryCount := 0
		completed := false
		var numVins, numVouts int64
		for retryCount < 50 && !completed {
			numVins, numVouts, err = pgb.StoreLTCWholeBlock(pgb.LtcClient, block.MsgBlock(), conflictCheck, false)
			if err != nil {
				log.Errorf("LTC StoreBlock failed (height %d): %v. Retrying...", height, err)
				retryCount++
			} else {
				completed = true
				break
			}
		}
		// if not completed
		if !completed {
			log.Errorf("LTC: Retry 50 times but can't complete. (height %d)", height)
			return
		}
		totalTxs += len(block.Transactions())
		totalVins += int(numVins)
		totalVouts += int(numVouts)

		countBlock++
		if idx == 0 {
			firstBlock = height
		}
		display := false
		if countBlock == 200 || idx == len(remaingHeights)-1 {
			lastBlock = height
			display = true
		}

		if display {
			log.Infof("LTC: Processed data for blocks from %d to %d. Txs: %d. Vins: %d, Vouts: %d", firstBlock, lastBlock, totalTxs, totalVins, totalVouts)
			firstBlock = height + 1
			countBlock = 0
			totalTxs = 0
			totalVins = 0
			totalVouts = 0
		}
	}
	// reindex if needed
	// if reindexing {
	// 	// Check and remove duplicate rows if any before recreating index
	// 	err = pgb.MultichainCheckAndRemoveDupplicate(mutilchain.TYPELTC)
	// 	if err != nil {
	// 		log.Errorf("LTC: Check and remove dupplicate rows on all table failed: %v", err)
	// 		return
	// 	}
	// 	if err := pgb.IndexMutilchainWholeTable(mutilchain.TYPELTC); err != nil {
	// 		log.Errorf("LTC: Re-index failed: %v", err)
	// 		return
	// 	}
	// }

	log.Infof("LTC: Finish sync for %d blocks. Minimum height: %d, Maximum height: %d",
		len(remaingHeights), remaingHeights[0], remaingHeights[len(remaingHeights)-1])
}

func (pgb *ChainDB) SyncLTCAtomicSwap() error {
	// Get list of unsynchronized ltc blocks atomic swap transaction
	var ltcSyncHeights []int64
	rows, err := pgb.db.QueryContext(pgb.ctx, mutilchainquery.MakeSelectBlocksUnsynchoronized(mutilchain.TYPELTC))
	if err != nil {
		log.Errorf("Get list of unsynchronized ltc blocks failed %v", err)
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ltcHeight int64
		if err = rows.Scan(&ltcHeight); err != nil {
			log.Errorf("Scan litecoin blocks unsync failed %v", err)
			return err
		}
		ltcSyncHeights = append(ltcSyncHeights, ltcHeight)
	}
	if err = rows.Err(); err != nil {
		log.Errorf("Scan litecoin blocks unsync failed %v", err)
		return err
	}
	for _, syncHeight := range ltcSyncHeights {
		err = pgb.SyncLTCAtomicSwapData(syncHeight)
		if err != nil {
			log.Errorf("Scan litecoin blocks unsync failed %v", err)
			return err
		}
	}
	return nil
}

func (pgb *ChainDB) SyncLTCAtomicSwapData(height int64) error {
	log.Debugf("Start Sync LTC swap data with height: %d", height)
	blockhash, err := ltcrpcutils.WithTimeout(func() (*ltcchainhash.Hash, error) {
		return pgb.LtcClient.GetBlockHash(height)
	})
	if err != nil {
		return err
	}

	msgBlock, err := ltcrpcutils.WithTimeout(func() (*wire.MsgBlock, error) {
		return pgb.LtcClient.GetBlock(blockhash)
	})
	if err != nil {
		return err
	}
	// delete 24h before dump data from LTC swaps table
	_, err = DeleteDumpMultichainSwapData(pgb.db, mutilchain.TYPELTC)
	if err != nil {
		return err
	}
	// Check all regular tree txns except coinbase.
	for _, tx := range msgBlock.Transactions[1:] {
		swapRes, err := ltctxhelper.MsgTxAtomicSwapsInfo(tx, nil, pgb.ltcChainParams)
		if err != nil {
			return err
		}
		if swapRes == nil || swapRes.Found == "" {
			continue
		}
		for _, red := range swapRes.Redemptions {
			contractTx, err := pgb.GetLTCTransactionByHash(red.ContractTx)
			if err != nil {
				continue
			}
			red.Value = contractTx.MsgTx().TxOut[red.ContractVout].Value
			err = InsertLtcSwap(pgb.db, height, red)
			if err != nil {
				log.Errorf("InsertLTCSwap err: %v", err)
				continue
			}
		}
		for _, ref := range swapRes.Refunds {
			contractTx, err := pgb.GetLTCTransactionByHash(ref.ContractTx)
			if err != nil {
				continue
			}
			ref.Value = contractTx.MsgTx().TxOut[ref.ContractVout].Value
			err = InsertLtcSwap(pgb.db, height, ref)
			log.Errorf("InsertLTCSwap err: %v", err)
			continue
		}
	}
	// update block synced status
	var blockId int64
	err = pgb.db.QueryRow(mutilchainquery.MakeUpsertBlockSimpleInfo(mutilchain.TYPELTC), blockhash.String(),
		height, msgBlock.Header.Timestamp.Unix(), true).Scan(&blockId)
	if err != nil {
		log.Errorf("Update LTC block synced status failed: %v", err)
		return err
	}
	log.Debugf("Finish Sync LTC swap data with height: %d", height)
	return nil
}

func CalcLTCBlockSubsidy(height int32, params *chaincfg.Params) ltcutil.Amount {
	const initialSubsidy = 50 * ltcutil.SatoshiPerBitcoin // 50 BTC in satoshis
	interval := int32(params.SubsidyReductionInterval)    // 210,000 on Bitcoin
	halvings := uint(height / interval)
	if halvings >= 64 {
		return 0
	}
	return ltcutil.Amount(initialSubsidy >> halvings)
}

func SumLTCSubsidy(startHeight, endHeight int32, params *chaincfg.Params, excludeGenesis bool) ltcutil.Amount {
	var total ltcutil.Amount
	for h := startHeight; h <= endHeight; h++ {
		if excludeGenesis && h == 0 {
			continue
		}
		total += CalcLTCBlockSubsidy(h, params)
	}
	return total
}
