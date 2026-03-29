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

	"github.com/btcsuite/btcd/btcutil"
	btcchaincfg "github.com/btcsuite/btcd/chaincfg"
	btcchainhash "github.com/btcsuite/btcd/chaincfg/chainhash"
	btcClient "github.com/btcsuite/btcd/rpcclient"
	btcwire "github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrdata/db/dcrpg/v8/internal/mutilchainquery"
	apitypes "github.com/decred/dcrdata/v8/api/types"
	"github.com/decred/dcrdata/v8/db/dbtypes"
	"github.com/decred/dcrdata/v8/mutilchain"
	"github.com/decred/dcrdata/v8/mutilchain/btcrpcutils"
	"github.com/decred/dcrdata/v8/txhelpers"
	"github.com/decred/dcrdata/v8/txhelpers/btctxhelper"
)

func (db *ChainDB) SyncBTCChainDBAsync(res chan dbtypes.SyncResult,
	client *btcClient.Client, quit chan struct{}, updateAllAddresses, newIndexes bool) {
	if db == nil {
		res <- dbtypes.SyncResult{
			Height: -1,
			Error:  fmt.Errorf("ChainDB BTC (psql) disabled"),
		}
		return
	}
	height, err := db.SyncBTCChainDB(client, quit, newIndexes, updateAllAddresses)
	res <- dbtypes.SyncResult{
		Height: height,
		Error:  err,
	}
}

func (db *ChainDB) SyncBTCChainDB(client *btcClient.Client, quit chan struct{},
	newIndexes, updateAllAddresses bool) (int64, error) {
	// Get chain servers's best block
	type btcBestBlockResult struct {
		hash   *btcchainhash.Hash
		height int32
	}
	bestBlockRes, err := btcrpcutils.WithTimeout(func() (*btcBestBlockResult, error) {
		hash, height, err := btcrpcutils.GetBestBlock(client)
		if err != nil {
			return nil, err
		}
		return &btcBestBlockResult{hash, int32(height)}, nil
	})
	if err != nil {
		return -1, fmt.Errorf("GetBestBlock BTC failed: %v", err)
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

	startingHeight, err := db.MutilchainHeightDB(mutilchain.TYPEBTC)
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
		log.Info("BTC Large bulk load: Removing indexes and disabling duplicate checks.")
		err = db.DeindexAllMutilchain(mutilchain.TYPEBTC)
		if err != nil && !strings.Contains(err.Error(), "does not exist") && !strings.Contains(err.Error(), "不存在") {
			return lastBlock, err
		}
		db.MutilchainEnableDuplicateCheckOnInsert(false, mutilchain.TYPEBTC)
	} else {
		db.MutilchainEnableDuplicateCheckOnInsert(true, mutilchain.TYPEBTC)
	}

	// Start rebuilding
	startHeight := lastBlock + 1
	for ib := startHeight; ib <= int64(nodeHeight); ib++ {
		// check for quit signal
		select {
		case <-quit:
			log.Infof("BTC: Rescan cancelled at height %d.", ib)
			return ib - 1, nil
		default:
		}

		if (ib-1)%btcRescanLogBlockChunk == 0 || ib == startHeight {
			if ib == 0 {
				log.Infof("BTC: Scanning genesis block.")
			} else {
				endRangeBlock := btcRescanLogBlockChunk * (1 + (ib-1)/btcRescanLogBlockChunk)
				if endRangeBlock > int64(nodeHeight) {
					endRangeBlock = int64(nodeHeight)
				}
				log.Infof("BTC: Processing blocks %d to %d...", ib, endRangeBlock)
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

		block, blockHash, err := btcrpcutils.GetBlock(ib, client)
		if err != nil {
			return ib - 1, fmt.Errorf("BTC: GetBlock failed (%s): %v", blockHash, err)
		}
		var numVins, numVouts int64
		if numVins, numVouts, err = db.StoreBTCBlock(client, block.MsgBlock(), true, !updateAllAddresses); err != nil {
			return ib - 1, fmt.Errorf("BTC: StoreBlock failed: %v", err)
		}
		totalVins += numVins
		totalVouts += numVouts

		numRTx := int64(len(block.Transactions()))
		totalTxs += numRTx
		// totalRTxs += numRTx
		// totalSTxs += numSTx

		// update height, the end condition for the loop
		bestBlockUpd, err := btcrpcutils.WithTimeout(func() (*btcBestBlockResult, error) {
			hash, height, err := btcrpcutils.GetBestBlock(client)
			if err != nil {
				return nil, err
			}
			return &btcBestBlockResult{hash, int32(height)}, nil
		})
		if err != nil {
			return ib, fmt.Errorf("BTC: GetBestBlock failed: %v", err)
		}
		nodeHeight = bestBlockUpd.height
	}

	speedReport()

	if reindexing || newIndexes {
		if err = db.IndexAllMutilchain(mutilchain.TYPEBTC); err != nil {
			return int64(nodeHeight), fmt.Errorf("BTC: IndexAllMutilchain failed: %v", err)
		}
		if !updateAllAddresses {
			err = db.IndexMutilchainAddressesTable(mutilchain.TYPEBTC)
		}
	}

	if updateAllAddresses {
		// Remove existing indexes not on funding txns
		_ = db.DeindexMutilchainAddressesTable(mutilchain.TYPEBTC) // ignore errors for non-existent indexes
		log.Infof("BTC: Populating spending tx info in address table...")
		numAddresses, err := db.UpdateMutilchainSpendingInfoInAllAddresses(mutilchain.TYPEBTC)
		if err != nil {
			log.Errorf("BTC: UpdateSpendingInfoInAllAddresses for BTC FAILED: %v", err)
		}
		log.Infof("BTC: Updated %d rows of address table", numAddresses)
		if err = db.IndexMutilchainAddressesTable(mutilchain.TYPEBTC); err != nil {
			log.Errorf("BTC: IndexBTCAddressTable FAILED: %v", err)
		}
	}
	db.MutilchainEnableDuplicateCheckOnInsert(true, mutilchain.TYPEBTC)
	log.Infof("BTC: Sync finished at height %d. Delta: %d blocks, %d transactions, %d ins, %d outs",
		nodeHeight, int64(nodeHeight)-startHeight+1, totalTxs, totalVins, totalVouts)
	return int64(nodeHeight), err
}

func (pgb *ChainDB) SyncLast20BTCBlocks(nodeHeight int32) error {
	if !pgb.btc20BlocksSyncMtx.TryLock() {
		log.Debugf("BTC last 20 blocks sync already running, skipping")
		return nil
	}
	defer pgb.btc20BlocksSyncMtx.Unlock()
	//preprocessing, check from DB
	// Total and rate statistics
	var totalTxs, totalVins, totalVouts int64
	startHeight := nodeHeight - 25
	//Delete all blocks data and blocks related data older than start block
	//Delete vins, vouts
	err := DeleteVinsOfOlderThan20Blocks(pgb.ctx, pgb.db, mutilchain.TYPEBTC, int64(startHeight))
	if err != nil {
		return err
	}
	err = DeleteVoutsOfOlderThan20Blocks(pgb.ctx, pgb.db, mutilchain.TYPEBTC, int64(startHeight))
	if err != nil {
		return err
	}
	// Start rebuilding
	for ib := startHeight; ib <= nodeHeight; ib++ {
		block, blockHash, err := btcrpcutils.GetBlock(int64(ib), pgb.BtcClient)
		if err != nil {
			return fmt.Errorf("BTC: GetBlock failed (%s): %v", blockHash, err)
		}
		var numVins, numVouts int64
		//check exist on DB
		exist, err := CheckBlockExistOnDB(pgb.ctx, pgb.db, mutilchain.TYPEBTC, int64(ib))
		if err != nil {
			return fmt.Errorf("BTC: Check exist block (%d) on db failed: %v", ib, err)
		}
		if exist {
			// sync and update for block
			dbBlockInfo, err := RetrieveBlockInfo(pgb.ctx, pgb.db, mutilchain.TYPEBTC, int64(ib))
			if err != nil {
				return fmt.Errorf("BTC: Get block detail (%d) on db failed: %v", ib, err)
			}
			// if have summary info, ignore
			if dbBlockInfo.TxCount > 0 || dbBlockInfo.Inputs > 0 || dbBlockInfo.Outputs > 0 {
				continue
			}
			// if don't have any info, update summary info
			if numVins, numVouts, err = pgb.UpdateStoreBTCBlockInfo(pgb.BtcClient, block.MsgBlock(), int64(ib), false); err != nil {
				return fmt.Errorf("BTC UpdateStoreBlock failed: %v", err)
			}
		} else {
			if numVins, numVouts, err = pgb.StoreBTCBlockInfo(pgb.BtcClient, block.MsgBlock(), int64(ib), false); err != nil {
				return fmt.Errorf("BTC StoreBlock failed: %v", err)
			}
		}
		totalVins += numVins
		totalVouts += numVouts
		numRTx := int64(len(block.Transactions()))
		totalTxs += numRTx
		// update height, the end condition for the loop
		type btcBestBlockResult struct {
			hash   *btcchainhash.Hash
			height int32
		}
		bestBlockUpd, err := btcrpcutils.WithTimeout(func() (*btcBestBlockResult, error) {
			hash, height, err := btcrpcutils.GetBestBlock(pgb.BtcClient)
			if err != nil {
				return nil, err
			}
			return &btcBestBlockResult{hash, int32(height)}, nil
		})
		if err != nil {
			return fmt.Errorf("BTC: GetBestBlock failed: %v", err)
		}
		nodeHeight = bestBlockUpd.height
	}

	log.Debugf("BTC: Sync last 20 Blocks of BTC finished at height %d. Delta: %d blocks, %d transactions, %d ins, %d outs",
		nodeHeight, int64(nodeHeight)-int64(startHeight)+1, totalTxs, totalVins, totalVouts)
	return err
}

// StoreBlock processes the input wire.MsgBlock, and saves to the data tables.
// The number of vins, and vouts stored are also returned.
func (pgb *ChainDB) StoreBTCBlock(client *btcClient.Client, msgBlock *btcwire.MsgBlock,
	isValid, updateAddressesSpendingInfo bool) (numVins int64, numVouts int64, err error) {
	// Convert the wire.MsgBlock to a dbtypes.Block
	dbBlock := dbtypes.MsgBTCBlockToDBBlock(client, msgBlock, pgb.btcChainParams)
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

	txRes := pgb.storeBTCTxns(dbtx, client, dbBlock, msgBlock,
		pgb.btcChainParams, &dbBlock.TxDbIDs, updateAddressesSpendingInfo, true, true)

	if txRes.err != nil {
		err = txRes.err
		log.Errorf("BTC: storeBTCTxns failed: %v", err)
		return
	}
	numVins = txRes.numVins
	numVouts = txRes.numVouts
	dbBlock.NumVins = uint32(numVins)
	dbBlock.NumVouts = uint32(numVouts)
	dbBlock.Fees = uint64(txRes.fees)
	dbBlock.TotalSent = uint64(txRes.totalSent)
	// Store the block now that it has all it's transaction PK IDs
	var blockDbID uint64
	blockDbID, err = InsertMutilchainBlock(dbtx, dbBlock, isValid, pgb.btcDupChecks, mutilchain.TYPEBTC)
	if err != nil {
		log.Error("BTC: InsertBlock:", err)
		return
	}
	pgb.btcLastBlock[msgBlock.BlockHash()] = blockDbID

	pgb.BtcBestBlock = &MutilchainBestBlock{
		Height: int64(dbBlock.Height),
		Hash:   dbBlock.Hash,
	}
	err = InsertMutilchainBlockPrevNext(dbtx, blockDbID, dbBlock.Hash,
		dbBlock.PreviousHash, "", mutilchain.TYPEBTC)
	if err != nil && err != sql.ErrNoRows {
		log.Error("BTC: InsertBlockPrevNext:", err)
		return
	}

	pgb.BtcBestBlock.Mtx.Lock()
	pgb.BtcBestBlock.Height = int64(dbBlock.Height)
	pgb.BtcBestBlock.Hash = dbBlock.Hash
	pgb.BtcBestBlock.Mtx.Unlock()

	// Update last block in db with this block's hash as it's next. Also update
	// isValid flag in last block if votes in this block invalidated it.
	lastBlockHash := msgBlock.Header.PrevBlock
	lastBlockDbID, ok := pgb.btcLastBlock[lastBlockHash]
	if ok {
		log.Infof("BTC: Setting last block %s. Height: %d", lastBlockHash, dbBlock.Height)
		err = UpdateMutilchainLastBlock(dbtx, lastBlockDbID, false, mutilchain.TYPEBTC)
		if err != nil {
			log.Error("BTC: UpdateLastBlock:", err)
			return
		}
		err = UpdateMutilchainBlockNext(dbtx, lastBlockDbID, dbBlock.Hash, mutilchain.TYPEBTC)
		if err != nil {
			log.Error("UpdateBlockNext:", err)
			return
		}
	}
	// Commit the tx
	if cerr := dbtx.Commit(); cerr != nil {
		err = fmt.Errorf("BTC: commit tx: %v", cerr)
		log.Error(err)
		return
	}
	committed = true
	return
}

// StoreBTCBlockInfo. Store only blockinfo. For get summary info of block (Not use when sync blockchain data)
func (pgb *ChainDB) StoreBTCBlockInfo(client *btcClient.Client, msgBlock *btcwire.MsgBlock, height int64, allSync bool) (numVins int64, numVouts int64, err error) {
	log.Infof("BTC: Start sync block info. Height: %d", height)
	// Convert the wire.MsgBlock to a dbtypes.Block
	dbBlock := dbtypes.MsgBTCBlockToDBBlock(client, msgBlock, pgb.btcChainParams)
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
	resChanReg := make(chan storeTxnsResult)
	go func() {
		resChanReg <- pgb.getBTCTxnsInfo(client, dbBlock, msgBlock,
			pgb.btcChainParams)
	}()
	errReg := <-resChanReg
	dbBlock.NumVins = uint32(errReg.numVins)
	dbBlock.NumVouts = uint32(errReg.numVouts)
	dbBlock.Fees = uint64(errReg.fees)
	numVins = errReg.numVins
	numVouts = errReg.numVouts
	dbBlock.TotalSent = uint64(errReg.totalSent)
	// Store the block now that it has all it's transaction PK IDs
	_, err = InsertMutilchainBlock(dbtx, dbBlock, true, pgb.btcDupChecks, mutilchain.TYPEBTC)
	if err != nil {
		log.Error("BTC: InsertBlock:", err)
		return
	}
	// Commit the tx
	if cerr := dbtx.Commit(); cerr != nil {
		err = fmt.Errorf("BTC: commit tx: %v", cerr)
		log.Error(err)
		return
	}
	committed = true
	log.Infof("BTC: Finish sync block info. Height: %d", height)
	return
}

func (pgb *ChainDB) UpdateStoreBTCBlockInfo(client *btcClient.Client, msgBlock *btcwire.MsgBlock, height int64, allSync bool) (numVins int64, numVouts int64, err error) {
	log.Infof("BTC: Start update sync block info. Height: %d", height)
	// Convert the wire.MsgBlock to a dbtypes.Block
	dbBlock := dbtypes.MsgBTCBlockToDBBlock(client, msgBlock, pgb.btcChainParams)
	// regular transactions
	resChanReg := make(chan storeTxnsResult)
	go func() {
		resChanReg <- pgb.getBTCTxnsInfo(client, dbBlock, msgBlock,
			pgb.btcChainParams)
	}()

	errReg := <-resChanReg
	dbBlock.NumVins = uint32(errReg.numVins)
	dbBlock.NumVouts = uint32(errReg.numVouts)
	dbBlock.Fees = uint64(errReg.fees)
	numVins = errReg.numVins
	numVouts = errReg.numVouts
	dbBlock.TotalSent = uint64(errReg.totalSent)
	// Store the block now that it has all it's transaction PK IDs
	_, err = UpdateMutilchainBlock(pgb.db, dbBlock, true, mutilchain.TYPEBTC)
	if err != nil {
		log.Errorf("BTC: UpdateBlock failed: %v", err)
		return
	}
	log.Infof("BTC: Finish update sync block info. Height: %d", height)
	return
}

func (pgb *ChainDB) StoreBTCWholeBlock(client *btcClient.Client, msgBlock *btcwire.MsgBlock, conflictCheck, updateAddressSpendInfo bool) (numVins int64, numVouts int64, err error) {
	// Convert the wire.MsgBlock to a dbtypes.Block
	dbBlock := dbtypes.MsgBTCBlockToDBBlock(client, msgBlock, pgb.btcChainParams)
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

	txRes := pgb.storeBTCWholeTxns(dbtx, client, dbBlock, msgBlock, conflictCheck, updateAddressSpendInfo,
		pgb.btcChainParams)
	if txRes.err != nil {
		err = txRes.err
		log.Errorf("BTC: storeBTCWholeTxns failed: %v", err)
		return
	}

	numVins = txRes.numVins
	numVouts = txRes.numVouts
	dbBlock.NumVins = uint32(numVins)
	dbBlock.NumVouts = uint32(numVouts)
	dbBlock.Fees = uint64(txRes.fees)
	dbBlock.TotalSent = uint64(txRes.totalSent)
	// Store the block now that it has all it's transaction PK IDs
	_, err = InsertMutilchainWholeBlock(dbtx, dbBlock, true, conflictCheck, mutilchain.TYPEBTC)
	if err != nil {
		log.Error("BTC: InsertBlock:", err)
		return
	}

	// update synced flag for block
	// log.Infof("BTC: Set synced flag for height: %d", dbBlock.Height)
	err = UpdateMutilchainSyncedStatus(dbtx, uint64(dbBlock.Height), mutilchain.TYPEBTC)
	if err != nil {
		log.Error("BTC: UpdateLastBlock:", err)
		return
	}
	// Commit the tx
	if cerr := dbtx.Commit(); cerr != nil {
		err = fmt.Errorf("BTC: commit tx: %v", cerr)
		log.Error(err)
		return
	}
	committed = true
	return
}

func (pgb *ChainDB) storeBTCTxns(sqlTx *sql.Tx, client *btcClient.Client, block *dbtypes.Block, msgBlock *btcwire.MsgBlock,
	chainParams *btcchaincfg.Params, TxDbIDs *[]uint64,
	updateAddressesSpendingInfo, onlyTxInsert, allSync bool) storeTxnsResult {
	dbTransactions, dbTxVouts, dbTxVins := dbtypes.ExtractBTCBlockTransactions(client, block,
		msgBlock, chainParams)
	var txRes storeTxnsResult
	dbAddressRows := make([][]dbtypes.MutilchainAddressRow, len(dbTransactions))
	var totalAddressRows int
	var err error
	for it, dbtx := range dbTransactions {
		dbtx.VoutDbIds, dbAddressRows[it], err = InsertMutilchainVouts(sqlTx, dbTxVouts[it], pgb.btcDupChecks, mutilchain.TYPEBTC)
		if err != nil && err != sql.ErrNoRows {
			log.Error("BTC: InsertVouts:", err)
			txRes.err = err
			return txRes
		}
		totalAddressRows += len(dbAddressRows[it])
		txRes.numVouts += int64(len(dbtx.VoutDbIds))
		if err == sql.ErrNoRows || len(dbTxVouts[it]) != len(dbtx.VoutDbIds) {
			log.Warnf("BTC: Incomplete Vout insert.")
		}
		dbtx.VinDbIds, err = InsertMutilchainVins(sqlTx, dbTxVins[it], mutilchain.TYPEBTC, pgb.btcDupChecks)
		if err != nil && err != sql.ErrNoRows {
			log.Error("BTC: InsertVins:", err)
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
		*TxDbIDs, err = InsertMutilchainTxns(sqlTx, dbTransactions, pgb.btcDupChecks, mutilchain.TYPEBTC)
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
		_, err = InsertMutilchainAddressOuts(sqlTx, dbAddressRowsFlat, mutilchain.TYPEBTC, pgb.btcDupChecks)
		if err != nil {
			log.Error("BTC: InsertAddressOuts:", err)
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
					txDbID, vin.TxID, vin.TxIndex, vinDbID, mutilchain.TYPEBTC) // spending
				if err != nil {
					log.Errorf("BTC: SetSpendingForFundingOP: %v", err)
				}
				txRes.numAddresses += numAddressRowsSet
			}
		}
	}
	return txRes
}

func (pgb *ChainDB) getBTCTxnsInfo(client *btcClient.Client, block *dbtypes.Block, msgBlock *btcwire.MsgBlock,
	chainParams *btcchaincfg.Params) storeTxnsResult {
	dbTransactions := dbtypes.ExtractBTCBlockTransactionsSimpleInfo(client, block,
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

func (pgb *ChainDB) storeBTCWholeTxns(sqlTx *sql.Tx, client *btcClient.Client, block *dbtypes.Block, msgBlock *btcwire.MsgBlock, conflictCheck, addressSpendingUpdateInfo bool,
	chainParams *btcchaincfg.Params) storeTxnsResult {
	dbTransactions, dbTxVouts, dbTxVins := dbtypes.ExtractBTCBlockTransactions(client, block,
		msgBlock, chainParams)
	var txRes storeTxnsResult
	dbAddressRows := make([][]dbtypes.MutilchainAddressRow, len(dbTransactions))
	var totalAddressRows int
	var err error
	for it, dbtx := range dbTransactions {
		dbtx.VoutDbIds, dbAddressRows[it], err = InsertMutilchainWholeVouts(sqlTx, dbTxVouts[it], conflictCheck, mutilchain.TYPEBTC)
		if err != nil && err != sql.ErrNoRows {
			log.Error("BTC: InsertVouts:", err)
			txRes.err = err
			return txRes
		}
		totalAddressRows += len(dbAddressRows[it])
		txRes.numVouts += int64(len(dbtx.VoutDbIds))
		if err == sql.ErrNoRows || len(dbTxVouts[it]) != len(dbtx.VoutDbIds) {
			log.Warnf("BTC: Incomplete Vout insert.")
		}
		dbtx.VinDbIds, err = InsertMutilchainWholeVins(sqlTx, dbTxVins[it], mutilchain.TYPEBTC, conflictCheck)
		if err != nil && err != sql.ErrNoRows {
			log.Error("BTC: InsertVins:", err)
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
	TxDbIDs, err := InsertMutilchainTxns(sqlTx, dbTransactions, conflictCheck, mutilchain.TYPEBTC)
	if err != nil && err != sql.ErrNoRows {
		log.Error("InsertTxns:", err)
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
	_, err = InsertMutilchainAddressOuts(sqlTx, dbAddressRowsFlat, mutilchain.TYPEBTC, conflictCheck)
	if err != nil {
		log.Error("BTC: InsertAddressOuts:", err)
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
				txDbID, vin.TxID, vin.TxIndex, vinDbID, mutilchain.TYPEBTC) // spending
			if err != nil {
				log.Errorf("BTC: SetSpendingForFundingOP: %v", err)
			}
			txRes.numAddresses += numAddressRowsSet
		}
	}
	// set address synced flag
	txRes.addressesSynced = true
	return txRes
}

func (pgb *ChainDB) GetBTCBlockData(hash string, height int64) (*apitypes.Block24hData, bool) {
	yeserDayTimeInt := time.Now().Add(-24 * time.Hour).Unix()
	//Get block verbose
	blockData := pgb.GetBTCBlockVerboseTxByHash(hash)
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
		msgTx, err := txhelpers.MsgBTCTxFromHex(tx.Hex, int32(tx.Version))
		if err != nil {
			log.Errorf("BTC: Unknown transaction %s: %v", tx.Txid, err)
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
				txInResult, txinErr := btcrpcutils.GetRawTransactionByTxidStr(pgb.BtcClient, txin.PreviousOutPoint.Hash.String())
				if txinErr == nil {
					unitAmount = dbtypes.GetBTCValueInFromRawTransction(txInResult, txin)
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

func (pgb *ChainDB) SyncBTCWholeChain(newIndexes bool) {
	pgb.btcWholeSyncMtx.Lock()
	defer pgb.btcWholeSyncMtx.Unlock()

	// Get remaining heights
	btcBestBlockHeight := pgb.BtcBestBlock.Height
	rows, err := pgb.db.QueryContext(pgb.ctx, mutilchainquery.CreateSelectRemainingNotSyncedHeights(mutilchain.TYPEBTC), btcBestBlockHeight)
	if err != nil {
		log.Errorf("BTC: Query remaining syncing blocks height list failed: %v", err)
		return
	}
	remaingHeights, err := getRemainingHeightsFromSqlRows(rows)
	if err != nil {
		log.Errorf("BTC: Get remaining blocks height list failed: %v", err)
		return
	}
	if len(remaingHeights) == 0 {
		log.Infof("BTC: No more blocks to synchronize with the whole daemon")
		return
	}
	log.Infof("BTC: Start sync for %d blocks. Minimum height: %d, Maximum height: %d", len(remaingHeights), remaingHeights[0], remaingHeights[len(remaingHeights)-1])

	// reindexing := newIndexes || int64(len(remaingHeights)) > pgb.BtcBestBlock.Height/20
	checkConflict := false
	// if reindexing {
	// 	checkConflict = false
	// 	log.Info("BTC: Large bulk load: Removing indexes")
	// TODO: Reopen
	// if err = pgb.DeindexMutilchainWholeTable(mutilchain.TYPEBTC); err != nil &&
	// 	!strings.Contains(err.Error(), "does not exist") &&
	// 	!strings.Contains(err.Error(), "不存在") {
	// 	log.Errorf("BTC: Deindex for multichain whole table: %v", err)
	// 	return
	// }
	// }
	var totalTxs, totalVins, totalVouts int
	var firstBlock, lastBlock int64
	countBlock := 0
	for idx, height := range remaingHeights {
		// get block (if GetBlock is NOT thread-safe, guard with getBlockMu)
		// getBlockMu.Lock()
		block, _, err := btcrpcutils.GetBlock(height, pgb.BtcClient)
		// getBlockMu.Unlock()
		if err != nil {
			log.Errorf("BTC: GetBlock failed (%d): %v", height, err)
			return
		}
		// store block (if StoreBTCWholeBlock is NOT thread-safe, guard with storeBlockMu)
		// storeBlockMu.Lock()
		retryCount := 0
		completed := false
		var numVins, numVouts int64
		for retryCount < 50 && !completed {
			numVins, numVouts, err = pgb.StoreBTCWholeBlock(pgb.BtcClient, block.MsgBlock(), checkConflict, false)
			if err != nil {
				log.Errorf("BTC StoreBlock failed (height %d): %v. Retrying...", height, err)
				retryCount++
			} else {
				completed = true
				break
			}
		}
		// if not completed
		if !completed {
			log.Errorf("BTC: Retry 50 times but can't complete. (height %d)", height)
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
			log.Infof("BTC: Processed data for blocks from %d to %d. Txs: %d. Vins: %d, Vouts: %d", firstBlock, lastBlock, totalTxs, totalVins, totalVouts)
			firstBlock = height + 1
			countBlock = 0
			totalTxs = 0
			totalVins = 0
			totalVouts = 0
		}
	}
	// rebuild index
	// if reindexing {
	// TODO: Reopen
	// Check and remove duplicate rows if any before recreating index
	// err = pgb.MultichainCheckAndRemoveDupplicate(mutilchain.TYPEBTC)
	// if err != nil {
	// 	log.Errorf("BTC: Check and remove dupplicate rows on all table failed: %v", err)
	// 	return
	// }
	// if err := pgb.IndexMutilchainWholeTable(mutilchain.TYPEBTC); err != nil {
	// 	log.Errorf("BTC: Re-index failed: %v", err)
	// 	return
	// }
	// }

	log.Infof("BTC: Finish sync for %d blocks. Minimum height: %d, Maximum height: %d",
		len(remaingHeights), remaingHeights[0], remaingHeights[len(remaingHeights)-1])
}

func (pgb *ChainDB) SyncOneBTCWholeBlock(client *btcClient.Client, msgBlock *btcwire.MsgBlock) (err error) {
	pgb.btcWholeSyncMtx.Lock()
	defer pgb.btcWholeSyncMtx.Unlock()
	_, _, err = pgb.StoreBTCWholeBlock(client, msgBlock, true, true)
	return err
}

func (pgb *ChainDB) SyncBTCAtomicSwap() error {
	// Get list of unsynchronized btc blocks atomic swap transaction
	var btcSyncHeights []int64
	rows, err := pgb.db.QueryContext(pgb.ctx, mutilchainquery.MakeSelectBlocksUnsynchoronized(mutilchain.TYPEBTC))
	if err != nil {
		log.Errorf("Get list of unsynchronized btc blocks failed %v", err)
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var btcHeight int64
		if err = rows.Scan(&btcHeight); err != nil {
			log.Errorf("Scan bitcoin blocks unsync failed %v", err)
			return err
		}
		btcSyncHeights = append(btcSyncHeights, btcHeight)
	}
	if err = rows.Err(); err != nil {
		log.Errorf("Scan bitcoin blocks unsync failed %v", err)
		return err
	}
	for _, syncHeight := range btcSyncHeights {
		err = pgb.SyncBTCAtomicSwapData(syncHeight)
		if err != nil {
			log.Errorf("Scan bitcoin blocks unsync failed %v", err)
			return err
		}
	}
	return nil
}

func (pgb *ChainDB) SyncBTCAtomicSwapData(height int64) error {
	log.Debugf("Start Sync BTC swap data with height: %d", height)
	blockhash, err := btcrpcutils.WithTimeout(func() (*btcchainhash.Hash, error) {
		return pgb.BtcClient.GetBlockHash(height)
	})
	if err != nil {
		return err
	}

	msgBlock, err := btcrpcutils.WithTimeout(func() (*btcwire.MsgBlock, error) {
		return pgb.BtcClient.GetBlock(blockhash)
	})
	if err != nil {
		return err
	}
	// delete 24h before dump data from BTC swaps table
	_, err = DeleteDumpMultichainSwapData(pgb.db, mutilchain.TYPEBTC)
	if err != nil {
		return err
	}
	// Check all regular tree txns except coinbase.
	for _, tx := range msgBlock.Transactions[1:] {
		swapRes, err := btctxhelper.MsgTxAtomicSwapsInfo(tx, nil, pgb.btcChainParams)
		if err != nil {
			return err
		}
		if swapRes == nil || swapRes.Found == "" {
			continue
		}
		for _, red := range swapRes.Redemptions {
			contractTx, err := pgb.GetBTCTransactionByHash(red.ContractTx)
			if err != nil {
				continue
			}
			red.Value = contractTx.MsgTx().TxOut[red.ContractVout].Value
			err = InsertBtcSwap(pgb.db, height, red)
			if err != nil {
				log.Errorf("InsertBTCSwap err: %v", err)
				continue
			}
		}
		for _, ref := range swapRes.Refunds {
			contractTx, err := pgb.GetBTCTransactionByHash(ref.ContractTx)
			if err != nil {
				continue
			}
			ref.Value = contractTx.MsgTx().TxOut[ref.ContractVout].Value
			err = InsertBtcSwap(pgb.db, height, ref)
			log.Errorf("InsertBTCSwap err: %v", err)
			continue
		}
	}
	// update block synced status
	var blockId int64
	err = pgb.db.QueryRow(mutilchainquery.MakeUpsertBlockSimpleInfo(mutilchain.TYPEBTC), blockhash.String(),
		height, msgBlock.Header.Timestamp.Unix(), true).Scan(&blockId)
	if err != nil {
		log.Errorf("Update BTC block synced status failed: %v", err)
		return err
	}
	log.Debugf("Finish Sync BTC swap data with height: %d", height)
	return nil
}

func CalcBTCBlockSubsidy(height int32, params *btcchaincfg.Params) btcutil.Amount {
	const initialSubsidy = 50 * btcutil.SatoshiPerBitcoin // 50 BTC in satoshis
	interval := int32(params.SubsidyReductionInterval)    // 210,000 on Bitcoin
	halvings := uint(height / interval)
	if halvings >= 64 {
		return 0
	}
	return btcutil.Amount(initialSubsidy >> halvings)
}

func SumBTCSubsidy(startHeight, endHeight int32, params *btcchaincfg.Params, excludeGenesis bool) btcutil.Amount {
	var total btcutil.Amount
	for h := startHeight; h <= endHeight; h++ {
		if excludeGenesis && h == 0 {
			continue
		}
		total += CalcBTCBlockSubsidy(h, params)
	}
	return total
}
