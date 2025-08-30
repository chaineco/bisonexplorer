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

	btcchaincfg "github.com/btcsuite/btcd/chaincfg"
	btcClient "github.com/btcsuite/btcd/rpcclient"
	btcwire "github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrdata/db/dcrpg/v8/internal"
	apitypes "github.com/decred/dcrdata/v8/api/types"
	"github.com/decred/dcrdata/v8/db/dbtypes"
	"github.com/decred/dcrdata/v8/mutilchain"
	"github.com/decred/dcrdata/v8/mutilchain/btcrpcutils"
	"github.com/decred/dcrdata/v8/mutilchain/ltcrpcutils"
	"github.com/decred/dcrdata/v8/txhelpers"
	"github.com/ltcsuite/ltcd/chaincfg"
	ltcClient "github.com/ltcsuite/ltcd/rpcclient"
	"github.com/ltcsuite/ltcd/wire"
)

// SyncChainDBAsync is like SyncChainDB except it also takes a result channel on
// which the caller should wait to receive the result. As such, this method
// should be called as a goroutine or it will hang on send if the channel is
// unbuffered.
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
	_, nodeHeight, err := client.GetBestBlock()
	if err != nil {
		return -1, fmt.Errorf("GetBestBlock BTC failed: %v", err)
	}
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
		if _, nodeHeight, err = client.GetBestBlock(); err != nil {
			return ib, fmt.Errorf("BTC: GetBestBlock failed: %v", err)
		}
	}

	speedReport()

	if reindexing || newIndexes {
		if err = db.IndexAllMutilchain(nil, mutilchain.TYPEBTC); err != nil {
			return int64(nodeHeight), fmt.Errorf("BTC: IndexAllMutilchain failed: %v", err)
		}
		if !updateAllAddresses {
			err = db.IndexMutilchainAddressTable(nil, mutilchain.TYPEBTC)
		}
	}

	if updateAllAddresses {
		// Remove existing indexes not on funding txns
		_ = db.DeindexMutilchainAddressTable(mutilchain.TYPEBTC) // ignore errors for non-existent indexes
		log.Infof("BTC: Populating spending tx info in address table...")
		numAddresses, err := db.UpdateMutilchainSpendingInfoInAllAddresses(mutilchain.TYPEBTC)
		if err != nil {
			log.Errorf("BTC: UpdateSpendingInfoInAllAddresses for BTC FAILED: %v", err)
		}
		log.Infof("BTC: Updated %d rows of address table", numAddresses)
		if err = db.IndexMutilchainAddressTable(nil, mutilchain.TYPEBTC); err != nil {
			log.Errorf("BTC: IndexBTCAddressTable FAILED: %v", err)
		}
	}
	db.MutilchainEnableDuplicateCheckOnInsert(true, mutilchain.TYPEBTC)
	log.Infof("BTC: Sync finished at height %d. Delta: %d blocks, %d transactions, %d ins, %d outs",
		nodeHeight, int64(nodeHeight)-startHeight+1, totalTxs, totalVins, totalVouts)
	return int64(nodeHeight), err
}

func (pgb *ChainDB) IsLTC20BlocksSyncing() bool {
	return pgb.LTC20BlocksSyncing
}

func (pgb *ChainDB) IsBTC20BlocksSyncing() bool {
	return pgb.BTC20BlocksSyncing
}

func (pgb *ChainDB) SyncLast20BTCBlocks(nodeHeight int32) error {
	if pgb.BTC20BlocksSyncing {
		return fmt.Errorf("BTC: There is another sync task running")
	}
	pgb.BTC20BlocksSyncing = true
	//preprocessing, check from DB
	// Total and rate statistics
	var totalTxs, totalVins, totalVouts int64
	var lastTxs, lastVins, lastVouts int64
	startHeight := nodeHeight - 25
	//Delete all blocks data and blocks related data older than start block
	//Delete vins, vouts
	err := DeleteVinsOfOlderThan20Blocks(pgb.ctx, pgb.db, mutilchain.TYPEBTC, int64(startHeight))
	if err != nil {
		pgb.BTC20BlocksSyncing = false
		return err
	}
	err = DeleteVoutsOfOlderThan20Blocks(pgb.ctx, pgb.db, mutilchain.TYPEBTC, int64(startHeight))
	if err != nil {
		pgb.BTC20BlocksSyncing = false
		return err
	}
	//delete txs
	err = DeleteTxsOfOlderThan20Blocks(pgb.ctx, pgb.db, mutilchain.TYPEBTC, int64(startHeight))
	if err != nil {
		pgb.BTC20BlocksSyncing = false
		return err
	}
	//delete blocks
	// err = DeleteOlderThan20Blocks(pgb.ctx, pgb.db, mutilchain.TYPEBTC, int64(startHeight))
	// if err != nil {
	// 	pgb.BTC20BlocksSyncing = false
	// 	return err
	// }
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
		log.Infof("BTC: Avg. speed: %d tx/s, %d vout/s", totalTxPerSec, totalVoutPerSec)
	}
	speedReport := func() { o.Do(speedReporter) }
	defer speedReport()
	lastBlock := startHeight
	// Start rebuilding
	for ib := startHeight; ib <= nodeHeight; ib++ {
		//check exist on DB
		exist, err := CheckBlockExistOnDB(pgb.ctx, pgb.db, mutilchain.TYPEBTC, int64(ib))
		if err != nil || exist {
			continue
		}
		select {
		case <-ticker.C:
			blocksPerSec := float64(ib-lastBlock) / tickTime.Seconds()
			txPerSec := float64(totalTxs-lastTxs) / tickTime.Seconds()
			vinsPerSec := float64(totalVins-lastVins) / tickTime.Seconds()
			voutPerSec := float64(totalVouts-lastVouts) / tickTime.Seconds()
			log.Infof("BTC: (%3d blk/s,%5d tx/s,%5d vin/sec,%5d vout/s)", int64(blocksPerSec),
				int64(txPerSec), int64(vinsPerSec), int64(voutPerSec))
			lastBlock, lastTxs = ib, totalTxs
			lastVins, lastVouts = totalVins, totalVouts
		default:
		}

		block, blockHash, err := btcrpcutils.GetBlock(int64(ib), pgb.BtcClient)
		if err != nil {
			pgb.BTC20BlocksSyncing = false
			return fmt.Errorf("BTC: GetBlock failed (%s): %v", blockHash, err)
		}
		var numVins, numVouts int64
		if numVins, numVouts, err = pgb.StoreBTCBlockInfo(pgb.BtcClient, block.MsgBlock(), int64(ib)); err != nil {
			pgb.BTC20BlocksSyncing = false
			return fmt.Errorf("BTC StoreBlock failed: %v", err)
		}
		totalVins += numVins
		totalVouts += numVouts
		numRTx := int64(len(block.Transactions()))
		totalTxs += numRTx
		// update height, the end condition for the loop
		if _, nodeHeight, err = pgb.BtcClient.GetBestBlock(); err != nil {
			pgb.BTC20BlocksSyncing = false
			return fmt.Errorf("BTC: GetBestBlock failed: %v", err)
		}
	}

	speedReport()

	log.Debugf("BTC: Sync last 20 Blocks of BTC finished at height %d. Delta: %d blocks, %d transactions, %d ins, %d outs",
		nodeHeight, int64(nodeHeight)-int64(startHeight)+1, totalTxs, totalVins, totalVouts)
	pgb.BTC20BlocksSyncing = false
	return err
}

func (pgb *ChainDB) SyncLast20LTCBlocks(nodeHeight int32) error {
	if pgb.LTC20BlocksSyncing {
		return fmt.Errorf("LTC: There is another sync task running")
	}
	pgb.LTC20BlocksSyncing = true
	// Total and rate statistics
	var totalTxs, totalVins, totalVouts int64
	var lastTxs, lastVins, lastVouts int64
	startHeight := nodeHeight - 25
	//Delete all blocks data and blocks related data older than start block
	//Delete vins, vouts
	err := DeleteVinsOfOlderThan20Blocks(pgb.ctx, pgb.db, mutilchain.TYPELTC, int64(startHeight))
	if err != nil {
		pgb.LTC20BlocksSyncing = false
		return err
	}
	err = DeleteVoutsOfOlderThan20Blocks(pgb.ctx, pgb.db, mutilchain.TYPELTC, int64(startHeight))
	if err != nil {
		pgb.LTC20BlocksSyncing = false
		return err
	}
	//delete txs
	err = DeleteTxsOfOlderThan20Blocks(pgb.ctx, pgb.db, mutilchain.TYPELTC, int64(startHeight))
	if err != nil {
		pgb.LTC20BlocksSyncing = false
		return err
	}
	//delete blocks
	// err = DeleteOlderThan20Blocks(pgb.ctx, pgb.db, mutilchain.TYPELTC, int64(startHeight))
	// if err != nil {
	// 	pgb.LTC20BlocksSyncing = false
	// 	return err
	// }
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
		log.Infof("LTC: Avg. speed: %d tx/s, %d vout/s", totalTxPerSec, totalVoutPerSec)
	}
	speedReport := func() { o.Do(speedReporter) }
	defer speedReport()
	lastBlock := startHeight
	// Start rebuilding
	for ib := startHeight; ib <= nodeHeight; ib++ {
		//check exist on DB
		exist, err := CheckBlockExistOnDB(pgb.ctx, pgb.db, mutilchain.TYPELTC, int64(ib))
		if err != nil || exist {
			continue
		}
		select {
		case <-ticker.C:
			blocksPerSec := float64(ib-lastBlock) / tickTime.Seconds()
			txPerSec := float64(totalTxs-lastTxs) / tickTime.Seconds()
			vinsPerSec := float64(totalVins-lastVins) / tickTime.Seconds()
			voutPerSec := float64(totalVouts-lastVouts) / tickTime.Seconds()
			log.Infof("LTC: (%3d blk/s,%5d tx/s,%5d vin/sec,%5d vout/s)", int64(blocksPerSec),
				int64(txPerSec), int64(vinsPerSec), int64(voutPerSec))
			lastBlock, lastTxs = ib, totalTxs
			lastVins, lastVouts = totalVins, totalVouts
		default:
		}

		block, blockHash, err := ltcrpcutils.GetBlock(int64(ib), pgb.LtcClient)
		if err != nil {
			pgb.LTC20BlocksSyncing = false
			return fmt.Errorf("LTC: GetBlock failed (%s): %v", blockHash, err)
		}
		var numVins, numVouts int64
		if numVins, numVouts, err = pgb.StoreLTCBlockInfo(pgb.LtcClient, block.MsgBlock(), int64(ib)); err != nil {
			pgb.LTC20BlocksSyncing = false
			return fmt.Errorf("LTC StoreBlock failed: %v", err)
		}
		totalVins += numVins
		totalVouts += numVouts
		numRTx := int64(len(block.Transactions()))
		totalTxs += numRTx
		// update height, the end condition for the loop
		if _, nodeHeight, err = pgb.LtcClient.GetBestBlock(); err != nil {
			pgb.LTC20BlocksSyncing = false
			return fmt.Errorf("LTC: GetBestBlock failed: %v", err)
		}
	}

	speedReport()

	log.Debugf("LTC: Sync last 20 Blocks of LTC finished at height %d. Delta: %d blocks, %d transactions, %d ins, %d outs",
		nodeHeight, int64(nodeHeight)-int64(startHeight)+1, totalTxs, totalVins, totalVouts)
	pgb.LTC20BlocksSyncing = false
	return err
}

func (db *ChainDB) SyncLTCChainDB(client *ltcClient.Client, quit chan struct{},
	newIndexes, updateAllAddresses bool) (int64, error) {
	// Get chain servers's best block
	_, nodeHeight, err := client.GetBestBlock()
	if err != nil {
		return -1, fmt.Errorf("GetBestBlock LTC failed: %v", err)
	}
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
		if _, nodeHeight, err = client.GetBestBlock(); err != nil {
			return ib, fmt.Errorf("GetBestBlock failed: %v", err)
		}
	}

	speedReport()

	if reindexing || newIndexes {
		if err = db.IndexAllMutilchain(nil, mutilchain.TYPELTC); err != nil {
			return int64(nodeHeight), fmt.Errorf("IndexAllMutilchain failed: %v", err)
		}
		if !updateAllAddresses {
			err = db.IndexMutilchainAddressTable(nil, mutilchain.TYPELTC)
		}
	}

	if updateAllAddresses {
		// Remove existing indexes not on funding txns
		_ = db.DeindexMutilchainAddressTable(mutilchain.TYPELTC) // ignore errors for non-existent indexes
		log.Infof("Populating spending tx info in address table...")
		numAddresses, err := db.UpdateMutilchainSpendingInfoInAllAddresses(mutilchain.TYPELTC)
		if err != nil {
			log.Errorf("UpdateSpendingInfoInAllAddresses for LTC FAILED: %v", err)
		}
		log.Infof("Updated %d rows of address table", numAddresses)
		if err = db.IndexMutilchainAddressTable(nil, mutilchain.TYPELTC); err != nil {
			log.Errorf("IndexLTCAddressTable FAILED: %v", err)
		}
	}
	db.MutilchainEnableDuplicateCheckOnInsert(true, mutilchain.TYPELTC)
	log.Infof("Sync finished at height %d. Delta: %d blocks, %d transactions, %d ins, %d outs",
		nodeHeight, int64(nodeHeight)-startHeight+1, totalTxs, totalVins, totalVouts)

	return int64(nodeHeight), err
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

	// regular transactions
	resChanReg := make(chan storeTxnsResult)
	go func() {
		resChanReg <- pgb.storeBTCTxns(client, dbBlock, msgBlock,
			pgb.btcChainParams, &dbBlock.TxDbIDs, updateAddressesSpendingInfo, true)
	}()
	errReg := <-resChanReg
	numVins = errReg.numVins
	numVouts = errReg.numVouts
	dbBlock.NumVins = uint32(numVins)
	dbBlock.NumVouts = uint32(numVouts)
	dbBlock.Fees = uint64(errReg.fees)
	dbBlock.TotalSent = uint64(errReg.totalSent)
	// Store the block now that it has all it's transaction PK IDs
	var blockDbID uint64
	blockDbID, err = InsertMutilchainBlock(pgb.db, dbBlock, isValid, pgb.btcDupChecks, mutilchain.TYPEBTC)
	if err != nil {
		log.Error("BTC: InsertBlock:", err)
		return
	}
	pgb.btcLastBlock[msgBlock.BlockHash()] = blockDbID

	pgb.BtcBestBlock = &MutilchainBestBlock{
		Height: int64(dbBlock.Height),
		Hash:   dbBlock.Hash,
	}
	err = InsertMutilchainBlockPrevNext(pgb.db, blockDbID, dbBlock.Hash,
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
		err = UpdateMutilchainLastBlock(pgb.db, lastBlockDbID, false, mutilchain.TYPEBTC)
		if err != nil {
			log.Error("BTC: UpdateLastBlock:", err)
			return
		}
		err = UpdateMutilchainBlockNext(pgb.db, lastBlockDbID, dbBlock.Hash, mutilchain.TYPEBTC)
		if err != nil {
			log.Error("UpdateBlockNext:", err)
			return
		}
	}
	return
}

// StoreBTCBlockInfo. Store only blockinfo. For get summary info of block (Not use when sync blockchain data)
func (pgb *ChainDB) StoreBTCBlockInfo(client *btcClient.Client, msgBlock *btcwire.MsgBlock, height int64) (numVins int64, numVouts int64, err error) {
	log.Infof("BTC: Start sync block info. Height: %d", height)
	// Convert the wire.MsgBlock to a dbtypes.Block
	dbBlock := dbtypes.MsgBTCBlockToDBBlock(client, msgBlock, pgb.btcChainParams)
	// regular transactions
	resChanReg := make(chan storeTxnsResult)
	go func() {
		resChanReg <- pgb.storeBTCTxns(client, dbBlock, msgBlock,
			pgb.btcChainParams, &dbBlock.TxDbIDs, false, false)
	}()
	errReg := <-resChanReg
	dbBlock.NumVins = uint32(errReg.numVins)
	dbBlock.NumVouts = uint32(errReg.numVouts)
	dbBlock.Fees = uint64(errReg.fees)
	numVins = errReg.numVins
	numVouts = errReg.numVouts
	dbBlock.TotalSent = uint64(errReg.totalSent)
	// Store the block now that it has all it's transaction PK IDs
	_, err = InsertMutilchainBlock(pgb.db, dbBlock, true, pgb.btcDupChecks, mutilchain.TYPEBTC)
	if err != nil {
		log.Error("BTC: InsertBlock:", err)
		return
	}
	return
}

// StoreLTCBlockInfo. Store only blockinfo. For get summary info of block (Not use when sync blockchain data)
func (pgb *ChainDB) StoreLTCBlockInfo(client *ltcClient.Client, msgBlock *wire.MsgBlock, height int64) (numVins int64, numVouts int64, err error) {
	log.Infof("LTC: Start sync block info. Height: %d", height)
	// Convert the wire.MsgBlock to a dbtypes.Block
	dbBlock := dbtypes.MsgLTCBlockToDBBlock(client, msgBlock, pgb.ltcChainParams)
	// regular transactions
	resChanReg := make(chan storeTxnsResult)
	go func() {
		resChanReg <- pgb.storeLTCTxns(client, dbBlock, msgBlock,
			pgb.ltcChainParams, &dbBlock.TxDbIDs, false, false)
	}()

	errReg := <-resChanReg
	dbBlock.NumVins = uint32(errReg.numVins)
	dbBlock.NumVouts = uint32(errReg.numVouts)
	dbBlock.Fees = uint64(errReg.fees)
	numVins = errReg.numVins
	numVouts = errReg.numVouts
	dbBlock.TotalSent = uint64(errReg.totalSent)
	// Store the block now that it has all it's transaction PK IDs
	_, err = InsertMutilchainBlock(pgb.db, dbBlock, true, pgb.ltcDupChecks, mutilchain.TYPELTC)
	if err != nil {
		log.Error("LTC: InsertBlock:", err)
		return
	}
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

	// regular transactions
	resChanReg := make(chan storeTxnsResult)
	go func() {
		resChanReg <- pgb.storeLTCTxns(client, dbBlock, msgBlock,
			pgb.ltcChainParams, &dbBlock.TxDbIDs, updateAddressesSpendingInfo, true)
	}()

	errReg := <-resChanReg
	numVins = errReg.numVins
	numVouts = errReg.numVouts
	dbBlock.NumVins = uint32(numVins)
	dbBlock.NumVouts = uint32(numVouts)
	dbBlock.Fees = uint64(errReg.fees)
	dbBlock.TotalSent = uint64(errReg.totalSent)
	// Store the block now that it has all it's transaction PK IDs
	var blockDbID uint64
	blockDbID, err = InsertMutilchainBlock(pgb.db, dbBlock, isValid, pgb.ltcDupChecks, mutilchain.TYPELTC)
	if err != nil {
		log.Error("InsertBlock:", err)
		return
	}
	pgb.ltcLastBlock[msgBlock.BlockHash()] = blockDbID

	// pgb.LtcBestBlock = &MutilchainBestBlock{
	// 	Height: int64(dbBlock.Height),
	// 	Hash:   dbBlock.Hash,
	// }

	err = InsertMutilchainBlockPrevNext(pgb.db, blockDbID, dbBlock.Hash,
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
		err = UpdateMutilchainLastBlock(pgb.db, lastBlockDbID, false, mutilchain.TYPELTC)
		if err != nil {
			log.Error("UpdateLastBlock:", err)
			return
		}
		err = UpdateMutilchainBlockNext(pgb.db, lastBlockDbID, dbBlock.Hash, mutilchain.TYPELTC)
		if err != nil {
			log.Error("UpdateBlockNext:", err)
			return
		}
	}
	return
}

func (pgb *ChainDB) storeLTCTxns(client *ltcClient.Client, block *dbtypes.Block, msgBlock *wire.MsgBlock,
	chainParams *chaincfg.Params, TxDbIDs *[]uint64,
	updateAddressesSpendingInfo, onlyTxInsert bool) storeTxnsResult {
	dbTransactions, dbTxVouts, dbTxVins := dbtypes.ExtractLTCBlockTransactions(client, block,
		msgBlock, chainParams)
	var txRes storeTxnsResult
	dbAddressRows := make([][]dbtypes.MutilchainAddressRow, len(dbTransactions))
	var totalAddressRows int
	var err error
	for it, dbtx := range dbTransactions {
		dbtx.VoutDbIds, dbAddressRows[it], err = InsertMutilchainVouts(pgb.db, dbTxVouts[it], pgb.ltcDupChecks, mutilchain.TYPELTC)
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

		dbtx.VinDbIds, err = InsertMutilchainVins(pgb.db, dbTxVins[it], mutilchain.TYPELTC, pgb.ltcDupChecks)
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

	// Get the tx PK IDs for storage in the blocks table
	*TxDbIDs, err = InsertMutilchainTxns(pgb.db, dbTransactions, pgb.ltcDupChecks, mutilchain.TYPELTC)
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
	_, err = InsertMutilchainAddressOuts(pgb.db, dbAddressRowsFlat, mutilchain.TYPELTC, pgb.ltcDupChecks)
	if err != nil {
		log.Error("InsertAddressOuts:", err)
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
			// Get the tx hash and vout index (previous output) from vins table
			// vinDbID, txHash, txIndex, _, err := RetrieveFundingOutpointByTxIn(
			// 	pgb.db, vin.TxID, vin.TxIndex)
			vinDbID := dbTransactions[it].VinDbIds[iv]
			// skip coinbase inputs
			if bytes.Equal(zeroHashStringBytes, []byte(vin.PrevTxHash)) {
				continue
			}

			var numAddressRowsSet int64
			numAddressRowsSet, err = SetMutilchainSpendingForFundingOP(pgb.db,
				vin.PrevTxHash, vin.PrevTxIndex, // funding
				txDbID, vin.TxID, vin.TxIndex, vinDbID, mutilchain.TYPELTC) // spending
			if err != nil {
				log.Errorf("SetSpendingForFundingOP: %v", err)
			}
			txRes.numAddresses += numAddressRowsSet
		}
	}

	return txRes
}

func (pgb *ChainDB) storeBTCTxns(client *btcClient.Client, block *dbtypes.Block, msgBlock *btcwire.MsgBlock,
	chainParams *btcchaincfg.Params, TxDbIDs *[]uint64,
	updateAddressesSpendingInfo, onlyTxInsert bool) storeTxnsResult {
	dbTransactions, dbTxVouts, dbTxVins := dbtypes.ExtractBTCBlockTransactions(client, block,
		msgBlock, chainParams)
	var txRes storeTxnsResult
	dbAddressRows := make([][]dbtypes.MutilchainAddressRow, len(dbTransactions))
	var totalAddressRows int
	var err error
	for it, dbtx := range dbTransactions {
		dbtx.VoutDbIds, dbAddressRows[it], err = InsertMutilchainVouts(pgb.db, dbTxVouts[it], pgb.btcDupChecks, mutilchain.TYPEBTC)
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
		dbtx.VinDbIds, err = InsertMutilchainVins(pgb.db, dbTxVins[it], mutilchain.TYPEBTC, pgb.btcDupChecks)
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
	*TxDbIDs, err = InsertMutilchainTxns(pgb.db, dbTransactions, pgb.btcDupChecks, mutilchain.TYPEBTC)
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
	_, err = InsertMutilchainAddressOuts(pgb.db, dbAddressRowsFlat, mutilchain.TYPEBTC, pgb.btcDupChecks)
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
			numAddressRowsSet, err = SetMutilchainSpendingForFundingOP(pgb.db,
				vin.PrevTxHash, vin.PrevTxIndex, // funding
				txDbID, vin.TxID, vin.TxIndex, vinDbID, mutilchain.TYPEBTC) // spending
			if err != nil {
				log.Errorf("BTC: SetSpendingForFundingOP: %v", err)
			}
			txRes.numAddresses += numAddressRowsSet
		}
	}

	return txRes
}

func (pgb *ChainDB) Sync24BlocksAsync() {
	//delete all invalid row
	numRow, delErr := DeleteInvalid24hBlocksRow(pgb.db)
	if delErr != nil {
		log.Errorf("failed to delete invalid block from DB: %v", delErr)
		return
	}

	if numRow > 0 {
		log.Debugf("Sync24BlocksAsync: Deleted %d rows on 24hblocks table", numRow)
	}
	chainList := []string{mutilchain.TYPEDCR}
	chainList = append(chainList, dbtypes.MutilchainList...)
	//Get valid blockchain
	for _, chain := range chainList {
		pgb.Sync24hMetricsByChainType(chain)
	}
}

func (pgb *ChainDB) Sync24hMetricsByChainType(chain string) {
	if pgb.ChainDisabledMap[chain] {
		return
	}
	if chain == mutilchain.TYPEDCR {
		log.Debugf("Start syncing for 24hblocks info. ChainType: %s", mutilchain.TYPEDCR)
		pgb.SyncDecred24hBlocks()
		log.Debugf("Finish syncing for 24hblocks info. ChainType: %s", mutilchain.TYPEDCR)
		return
	}
	bbheight, _ := pgb.GetMutilchainBestBlock(chain)
	if bbheight == 0 {
		return
	}
	pgb.SyncMutilchain24hBlocks(bbheight, chain)
}

func (pgb *ChainDB) SyncDecred24hBlocks() {
	blockList, err := Retrieve24hBlockData(pgb.ctx, pgb.db)
	if err != nil {
		log.Errorf("Sync Decred blocks in 24h failed: %v", err)
		return
	}
	dbTx, err := pgb.db.BeginTx(pgb.ctx, nil)
	if err != nil {
		log.Errorf("failed to start new DB transaction: %v", err)
		return
	}
	//prepare query
	stmt, err := dbTx.Prepare(internal.Insert24hBlocksRow)
	if err != nil {
		dbTx.Rollback()
		log.Errorf("insert block info to 24hblocks table failed: %v", err)
		return
	}
	for _, block := range blockList {
		var exist bool
		//check exist on DB
		err := pgb.db.QueryRowContext(pgb.ctx, internal.CheckExist24Blocks, mutilchain.TYPEDCR, block.BlockHeight).Scan(&exist)
		if err != nil || exist {
			continue
		}
		var txnum, spent, sent, numvin, numvout int64
		pgb.db.QueryRowContext(pgb.ctx, internal.Select24hBlockSummary, block.BlockHeight).Scan(&txnum, &spent, &sent, &numvin, &numvout)
		//handler for fees
		block.Fees, _ = pgb.GetDecredBlockFees(block.BlockHash)
		//insert to db
		var id uint64
		err = stmt.QueryRow(mutilchain.TYPEDCR, block.BlockHash, block.BlockHeight, block.BlockTime,
			spent, sent, block.Fees, txnum, numvin, numvout).Scan(&id)
		if err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			_ = stmt.Close() // try, but we want the QueryRow error back
			if errRoll := dbTx.Rollback(); errRoll != nil {
				log.Errorf("Rollback failed: %v", errRoll)
			}
			return
		}
	}
	stmt.Close()
	dbTx.Commit()
}

func (pgb *ChainDB) GetDecredBlockFees(blockHash string) (int64, error) {
	data := pgb.GetBlockVerboseByHash(blockHash, true)
	if data == nil {
		return 0, fmt.Errorf("Unable to get block for block hash: %s", blockHash)
	}
	totalFees := int64(0)
	//Get fees from stake txs
	for i := range data.RawTx {
		stx := &data.RawTx[i]
		msgTx, err := txhelpers.MsgTxFromHex(stx.Hex)
		if err != nil {
			continue
		}
		fees, _ := txhelpers.TxFeeRate(msgTx)
		if int64(fees) < 0 {
			continue
		}
		totalFees += int64(fees)
	}
	return totalFees, nil
}

func (pgb *ChainDB) SyncMutilchain24hBlocks(height int64, chainType string) {
	log.Infof("Start syncing for 24hblocks info. ChainType: %s", chainType)
	dbTx, err := pgb.db.BeginTx(pgb.ctx, nil)
	if err != nil {
		log.Errorf("failed to start new DB transaction: %v", err)
		return
	}
	//prepare query
	stmt, err := dbTx.Prepare(internal.Insert24hBlocksRow)
	if err != nil {
		log.Errorf("%s: Prepare insert block info to 24hblocks table failed: %v", chainType, err)
		_ = dbTx.Rollback()
		return
	}
	for {
		var exist bool
		//check exist on DB
		err := pgb.db.QueryRowContext(pgb.ctx, internal.CheckExist24Blocks, chainType, height).Scan(&exist)
		if err != nil {
			log.Errorf("%s: Check block exist in 24hblocks table failed: %v", chainType, err)
			_ = stmt.Close()
			_ = dbTx.Rollback()
			return
		}

		if exist {
			height--
			continue
		}

		//Get block hash
		bHash, hashErr := pgb.GetDaemonMutilchainBlockHash(height, chainType)
		if hashErr != nil {
			log.Errorf("%s: Get block hash from height failed: %v", chainType, err)
			_ = stmt.Close()
			_ = dbTx.Rollback()
			return
		}

		var blockData *apitypes.Block24hData
		var isBreak bool
		switch chainType {
		case mutilchain.TYPELTC:
			blockData, isBreak = pgb.GetLTCBlockData(bHash, height)
		case mutilchain.TYPEBTC:
			blockData, isBreak = pgb.GetBTCBlockData(bHash, height)
		}

		if isBreak {
			break
		}

		log.Debugf("%s: Insert to 24h blocks metric: Height: %d, TxNum: %d", chainType, blockData.BlockHeight, blockData.NumTx)

		//insert to db
		var id uint64
		err = stmt.QueryRow(chainType, blockData.BlockHash, blockData.BlockHeight, blockData.BlockTime,
			blockData.Spent, blockData.Sent, blockData.Fees, blockData.NumTx, blockData.NumVin, blockData.NumVout).Scan(&id)
		if err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			_ = stmt.Close() // try, but we want the QueryRow error back
			if errRoll := dbTx.Rollback(); errRoll != nil {
				log.Errorf("Rollback failed: %v", errRoll)
			}
			return
		}
		height--
	}
	stmt.Close()
	dbTx.Commit()
	log.Infof("Finish syncing for 24hblocks info. ChainType: %s", chainType)
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
