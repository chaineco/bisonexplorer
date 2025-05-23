// Copyright (c) 2018-2021, The Decred developers
// Copyright (c) 2017, The dcrdata developers
// See LICENSE for details.

package dcrpg

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/decred/dcrd/blockchain/stake/v5"
	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrd/txscript/v4/stdaddr"
	"github.com/decred/dcrdata/db/dcrpg/v8/internal"
	"github.com/decred/dcrdata/db/dcrpg/v8/internal/mutilchainquery"
	"github.com/decred/dcrdata/v8/db/dbtypes"
	"github.com/decred/dcrdata/v8/mutilchain"
	"github.com/decred/dcrdata/v8/rpcutils"
	"github.com/decred/dcrdata/v8/txhelpers"
	"github.com/decred/dcrdata/v8/txhelpers/btctxhelper"
	"github.com/decred/dcrdata/v8/txhelpers/ltctxhelper"
)

const (
	quickStatsTarget         = 250
	deepStatsTarget          = 600
	rescanLogBlockChunk      = 500
	ltcRescanLogBlockChunk   = 250
	btcRescanLogBlockChunk   = 250
	initialLoadSyncStatusMsg = "Syncing stake and chain DBs..."
	addressesSyncStatusMsg   = "Syncing addresses table with spending info..."
)

func (pgb *ChainDB) SyncMonthlyPrice(ctx context.Context) error {
	//check lastest on monthly_table, if is same month, ignore
	var lastestMonth time.Time
	err := pgb.db.QueryRow(internal.SelectLastMonth).Scan(&lastestMonth)
	now := time.Now()
	if err != sql.ErrNoRows && (err != nil || lastestMonth.After(now) || (lastestMonth.Year() == now.Year() && lastestMonth.Month() == now.Month())) {
		log.Infof("No need to sync monthly price data")
		return nil
	}
	//check table exist and create new
	createTableErr := pgb.CheckCreateMonthlyPriceTable()
	if createTableErr != nil {
		log.Errorf("Check exist and create monthly_price table failed: %v", createTableErr)
		return createTableErr
	}

	//Get lastest monthly on monthly_table
	monthPriceMap := pgb.GetBitDegreeAllPriceMap()

	pgb.CheckAndInsertToMonthlyPriceTable(monthPriceMap)
	return nil
}

func (pgb *ChainDB) SyncCoinAgesData() error {
	// get max height of coin_age table
	var maxCoinAgeHeight int64
	err := pgb.db.QueryRowContext(pgb.ctx, internal.SelectCoinAgeMaxHeight).Scan(&maxCoinAgeHeight)
	if err != nil {
		log.Errorf("Get max height of coin age failed: %v", err)
		return err
	}
	// get max height of coin_age_bands table
	var maxUtxoHistoryHeight int64
	err = pgb.db.QueryRowContext(pgb.ctx, internal.SelectUtxoHistoryMaxHeight).Scan(&maxUtxoHistoryHeight)
	if err != nil {
		log.Errorf("Get max height of utxo_history failed: %v", err)
		return err
	}
	// get max height of coin_age_bands table
	var maxCoinAgeBandsHeight int64
	err = pgb.db.QueryRowContext(pgb.ctx, internal.SelectCoinAgeBandsMaxHeight).Scan(&maxCoinAgeBandsHeight)
	if err != nil {
		log.Errorf("Get max height of coin_age_bands failed: %v", err)
		return err
	}
	log.Info("Start sync coin_age table from height: ", maxCoinAgeHeight)
	// sync coin age data
	err = pgb.syncCoinAgeTable(maxCoinAgeHeight)
	if err != nil {
		log.Errorf("Sync coin age table data failed: %v", err)
		return err
	}

	log.Info("Start sync utxo_history table from height: %d", maxUtxoHistoryHeight)
	// sync utxo history data
	err = pgb.syncUtxoHistoryTable(maxUtxoHistoryHeight)
	if err != nil {
		log.Errorf("Sync utxo_history table data failed: %v", err)
		return err
	}

	log.Info("Start sync coin_age_bands table from height: ", maxCoinAgeBandsHeight)
	// sync coin_age_bands table
	go pgb.syncCoinAgeBandsTable(maxCoinAgeBandsHeight)
	return nil
}

func (pgb *ChainDB) syncCoinAgeBandsTable(lastHeight int64) error {
	maxHeight := pgb.bestBlock.height
	batchSize := int64(50)
	for fromHeight := lastHeight + 1; fromHeight <= maxHeight; fromHeight += batchSize {
		toHeight := fromHeight + batchSize - 1
		if toHeight > maxHeight {
			toHeight = maxHeight
		}
		err := pgb.SyncCoinAgeBandsWithHeightRange(fromHeight, toHeight)
		if err != nil {
			log.Errorf("Sync coin_age_bands table failed with height from: %d to %d. Error: %v", fromHeight, toHeight, err)
			return err
		}
	}
	return nil
}

func (pgb *ChainDB) SyncCoinAgeBandsWithHeightRange(fromHeight, toHeight int64) error {
	log.Infof("Start sync coin_age_bands table from: %d to %d", fromHeight, toHeight)
	rows, err := pgb.db.QueryContext(pgb.ctx, internal.SelectCoinAgeBandsFromUtxoHistory, fromHeight, toHeight)
	if err != nil {
		return err
	}
	defer closeRows(rows)
	for rows.Next() {
		var totalValue int64
		var height int64
		var ageBand string
		var timestamp time.Time
		if err := rows.Scan(&height, &timestamp, &ageBand, &totalValue); err != nil {
			return err
		}
		result, err := pgb.db.Exec(internal.InsertCoinAgeBandsRow,
			height, timestamp, ageBand, totalValue)
		if err != nil {
			return fmt.Errorf("insert new rows to coin_age_bands table failed: %w", err)
		}
		_, err = result.RowsAffected()
		if err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	log.Infof("Finish sync coin_age_bands table from: %d to %d", fromHeight, toHeight)
	return nil
}

func (pgb *ChainDB) syncUtxoHistoryTable(lastHeight int64) error {
	maxHeight := pgb.bestBlock.height
	for height := lastHeight + 1; height <= maxHeight; height++ {
		err := pgb.SyncUtxoHistoryHeight(height)
		if err != nil {
			log.Errorf("Sync utxo history on height %d failed: %v", height, err)
			return err
		}
	}
	return nil
}

func (pgb *ChainDB) SyncUtxoHistoryHeight(height int64) error {
	fmt.Printf("🔄 Processing block %d...\n", height)
	// Add new utxo_history row
	result, err := pgb.db.Exec(internal.InsertUtxoHistoryRow, height)
	if err != nil {
		return fmt.Errorf("Insert to utxo history failed with height: %d. Error: %v", height, err)
	}
	_, err = result.RowsAffected()
	if err != nil {
		return err
	}

	_, err = pgb.db.Exec(internal.UpdateSpentUtxo, height)
	if err != nil {
		return err
	}
	return nil
}

func (pgb *ChainDB) syncCoinAgeTable(maxHeight int64) error {
	rows, err := pgb.db.QueryContext(pgb.ctx, internal.SelectCoinDaysDestroyed, maxHeight)
	if err != nil {
		return err
	}
	defer closeRows(rows)
	for rows.Next() {
		var cdd, avgAgeDays float64
		var height int64
		var timestamp time.Time
		if err := rows.Scan(&height, &timestamp, &cdd, &avgAgeDays); err != nil {
			return err
		}
		cddAmount := dcrutil.Amount(int64(math.Floor(cdd)))
		result, err := pgb.db.Exec(internal.InsertCoinAgeRow,
			height, timestamp, cddAmount, avgAgeDays)
		if err != nil {
			return fmt.Errorf("insert new rows to coin_age table failed: %w", err)
		}

		_, err = result.RowsAffected()
		if err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return nil
}

func (pgb *ChainDB) SyncCoinAgeTableWithHeight(height int64) error {
	rows, err := pgb.db.QueryContext(pgb.ctx, internal.SelectCoinDaysDestroyedWithHeight, height)
	if err != nil {
		return err
	}
	defer closeRows(rows)
	for rows.Next() {
		var cdd, avgAgeDays float64
		var height int64
		var timestamp time.Time
		if err := rows.Scan(&height, &timestamp, &cdd, &avgAgeDays); err != nil {
			return err
		}
		cddAmount := dcrutil.Amount(int64(math.Floor(cdd)))
		result, err := pgb.db.Exec(internal.InsertCoinAgeRow,
			height, timestamp, cddAmount, avgAgeDays)
		if err != nil {
			return fmt.Errorf("insert new rows to coin_age table failed: %w", err)
		}
		_, err = result.RowsAffected()
		if err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return nil
}

func (pgb *ChainDB) SyncTSpendVotesData() error {
	// Get all tspend transactions
	tspendTxns, err := pgb.GetAllTSpendTxns()
	if err != nil {
		log.Errorf("Get all tspend transactions failed: %v", err)
		return err
	}
	dbtx, err := pgb.db.Begin()
	if err != nil {
		return fmt.Errorf("unable to begin database transaction: %w", err)
	}
	// Prepare tspend votes insert statement.
	tspendVotesInsert := internal.MakeTSpendVotesInsertStatement(false)
	tspendVotesStmt, err := dbtx.Prepare(tspendVotesInsert)
	if err != nil {
		log.Errorf("TSpend Votes INSERT prepare: %v", err)
		_ = dbtx.Rollback() // try, but we want the Prepare error back
		return err
	}
	tip, err := pgb.GetTip()
	tipHeight := int64(tip.Height)
	if err != nil {
		log.Errorf("Failed to get the chain tip from the database.: %v", err)
		return nil
	}
	for _, tspendTx := range tspendTxns {
		log.Infof("Begin syncing for tx: %s", tspendTx.TxID)
		// get Treasury Spend Votes
		hash, err := chainhash.NewHashFromStr(tspendTx.TxID)
		if err != nil {
			return err
		}
		tspendVoteResult, err := pgb.Client.GetTreasurySpendVotes(pgb.ctx, nil, []*chainhash.Hash{hash})
		if err != nil {
			return err
		}
		maxVotes := int64(uint64(pgb.chainParams.TicketsPerBlock) * pgb.chainParams.TreasuryVoteInterval * pgb.chainParams.TreasuryVoteIntervalMultiplier)
		quorumCount := maxVotes * int64(pgb.chainParams.TreasuryVoteQuorumMultiplier) / int64(pgb.chainParams.TreasuryVoteQuorumDivisor)
		var rowID uint64
		// get votes tx list from start height and end height
		for _, vote := range tspendVoteResult.Votes {
			var maxRemainingBlocks int64
			voteStarted := tipHeight >= vote.VoteStart
			if !voteStarted {
				maxRemainingBlocks = vote.VoteEnd - vote.VoteStart
			} else if tspendTx.BlockHeight != 0 && vote.VoteEnd > tspendTx.BlockHeight {
				maxRemainingBlocks = vote.VoteEnd - tspendTx.BlockHeight
			} else if vote.VoteEnd > tipHeight {
				maxRemainingBlocks = vote.VoteEnd - tipHeight
			}
			maxRemainingVotes := maxRemainingBlocks * int64(pgb.chainParams.TicketsPerBlock)
			totalVotes := vote.YesVotes + vote.NoVotes
			requiredYesVotes := (totalVotes + maxRemainingVotes) * int64(pgb.chainParams.TreasuryVoteRequiredMultiplier) / int64(pgb.chainParams.TreasuryVoteRequiredDivisor)
			approved := vote.YesVotes >= requiredYesVotes && totalVotes >= quorumCount
			var voteEnd = vote.VoteEnd
			if approved {
				if voteEnd > tspendTx.BlockHeight {
					voteEnd = tspendTx.BlockHeight
				}
			}
			voteHashs, _ := RetrieveVotesHashWithHeightRange(pgb.ctx, pgb.db, vote.VoteStart, voteEnd)
			if err != nil && err != sql.ErrNoRows {
				return err
			}
			if err == sql.ErrNoRows {
				continue
			}
			for _, voteHash := range voteHashs {
				// get msgTx from hash
				voteTx, err := pgb.GetTransactionByHash(voteHash.TxHash)
				if err != nil {
					continue
				}
				_, _, _, _, tspendVotes, err := txhelpers.SSGenVoteChoices(voteTx, pgb.chainParams)
				if err != nil {
					return err
				}
				// handle for tspend votes
				for _, tspendChoices := range tspendVotes {
					err = tspendVotesStmt.QueryRow(voteHash.Id, tspendChoices.TSpend.String(), tspendChoices.Choice).Scan(&rowID)
					if err != nil {
						return err
					}
				}
			}
		}
		log.Infof("Finish syncing for tx: %s", tspendTx.TxID)
	}
	_ = tspendVotesStmt.Close()
	return dbtx.Commit()
}

func (pgb *ChainDB) SyncAddressSummary() error {
	if pgb.AddressSummarySyncing {
		return nil
	}
	pgb.AddressSummarySyncing = true
	err := pgb.CheckCreateAddressSummaryTable()
	if err != nil {
		log.Errorf("Check exist and create address summary table failed: %v", err)
		pgb.AddressSummarySyncing = false
		return err
	}

	//check synchronized month
	var summaryRows = make([]*dbtypes.AddressSummaryRow, 0)
	sumRows, _ := pgb.db.QueryContext(pgb.ctx, internal.SelectAddressSummaryRows)
	for sumRows.Next() {
		var addrSum dbtypes.AddressSummaryRow
		err := sumRows.Scan(&addrSum.Id, &addrSum.Time, &addrSum.SpentValue, &addrSum.ReceivedValue, &addrSum.MonthDebitRowIndex, &addrSum.MonthCreditRowIndex, &addrSum.Saved)
		if err == nil {
			summaryRows = append(summaryRows, &addrSum)
		}
	}

	projectFundAddress, addErr := dbtypes.DevSubsidyAddress(pgb.chainParams)
	if addErr != nil {
		log.Warnf("ChainDB.NewChainDB: %v", addErr)
	}
	_, decodeErr := stdaddr.DecodeAddress(projectFundAddress, pgb.chainParams)
	if decodeErr != nil {
		pgb.AddressSummarySyncing = false
		return decodeErr
	}

	//get firt address item to get next time
	frows, _ := pgb.db.QueryContext(pgb.ctx, internal.SelectAddressCreditsLimitNByAddressFromOldest, projectFundAddress, 1, 0)
	firstRows := pgb.ConvertToAddressObj(frows)
	if len(firstRows) < 1 {
		pgb.AddressSummarySyncing = false
		return nil
	}
	startTime := firstRows[0].TxBlockTime.T
	now := time.Now()

	//Get all data of legacy address
	//get data by month
	monthCreditCountRows, err := pgb.db.QueryContext(pgb.ctx, internal.SelectAddressCreditRowCountByMonth, projectFundAddress)
	if err != nil && err != sql.ErrNoRows {
		pgb.AddressSummarySyncing = false
		return err
	}
	monthDeditCountRows, err := pgb.db.QueryContext(pgb.ctx, internal.SelectAddressDebitRowCountByMonth, projectFundAddress)
	if err != nil && err != sql.ErrNoRows {
		pgb.AddressSummarySyncing = false
		return err
	}
	addressCreditCountRows := pgb.ConvertMonthRowsCount(monthCreditCountRows)
	addressDebitCountRows := pgb.ConvertMonthRowsCount(monthDeditCountRows)
	stopFlg := false
	for !stopFlg {
		if now.Month() == startTime.Month() && now.Year() == startTime.Year() {
			stopFlg = true
		}
		exist, isSavedFlg, summaryRow := pgb.CheckSavedSummary(summaryRows, startTime)
		if exist && isSavedFlg {
			startTime = startTime.AddDate(0, 1, 0)
			continue
		}
		//get start of month
		startDay := startTime.AddDate(0, 0, -startTime.Day()+1)
		startDay = time.Date(startDay.Year(), startDay.Month(), startDay.Day(), 0, 0, 0, 0, time.Local)
		//get end of month
		endDay := startTime.AddDate(0, 1, -startTime.Day())
		endDay = time.Date(endDay.Year(), endDay.Month(), endDay.Day(), 23, 59, 59, 999999999, time.Local)
		//get data by month
		rows, err := pgb.db.QueryContext(pgb.ctx, internal.SelectAddressCreditsLimitNByAddressByPeriod, projectFundAddress, startDay, endDay)
		if err != nil && err != sql.ErrNoRows {
			pgb.AddressSummarySyncing = false
			return err
		}
		addressRows := pgb.ConvertToAddressObj(rows)
		received := uint64(0)
		spent := uint64(0)
		for _, addressRow := range addressRows {
			if addressRow.IsFunding {
				received += addressRow.Value
			} else {
				spent += addressRow.Value
			}
		}
		//next month
		var nextMonth = startTime.AddDate(0, 1, 0)
		if spent == 0 && received == 0 {
			startTime = nextMonth
			continue
		}
		var thisMonth = false
		creditIndex := int64(-1)
		debitIndex := int64(-1)
		if now.Month() == startTime.Month() && now.Year() == startTime.Year() {
			thisMonth = true
		} else {
			creditIndex = pgb.GetStartRevertIndexAddressRow(addressCreditCountRows, startTime)
			debitIndex = pgb.GetStartRevertIndexAddressRow(addressDebitCountRows, startTime)
		}
		//set revert index
		if summaryRow == nil {
			//insert new row
			newSumRow := dbtypes.AddressSummaryRow{
				Time:                dbtypes.NewTimeDef(startDay),
				SpentValue:          spent,
				ReceivedValue:       received,
				Saved:               !thisMonth,
				MonthCreditRowIndex: creditIndex,
				MonthDebitRowIndex:  debitIndex,
			}
			var id uint64
			pgb.db.QueryRow(internal.InsertAddressSummaryRow, newSumRow.Time, newSumRow.SpentValue, newSumRow.ReceivedValue, newSumRow.Saved, newSumRow.MonthDebitRowIndex, newSumRow.MonthCreditRowIndex).Scan(&id)
			startTime = nextMonth
			continue
		}
		if received == summaryRow.ReceivedValue && spent == summaryRow.SpentValue {
			startTime = nextMonth
			continue
		}
		summaryRow.SpentValue = spent
		summaryRow.ReceivedValue = received
		summaryRow.Saved = !thisMonth
		summaryRow.MonthCreditRowIndex = creditIndex
		summaryRow.MonthDebitRowIndex = debitIndex
		//update row
		pgb.db.Exec(internal.UpdateAddressSummaryByTotalAndSpent, spent, received, !thisMonth, debitIndex, creditIndex, summaryRow.Id)
		startTime = nextMonth
	}
	pgb.AddressSummarySyncing = false
	return nil
}

func (pgb *ChainDB) SyncTreasurySummary() error {
	if pgb.TreasurySummarySyncing {
		return nil
	}
	pgb.TreasurySummarySyncing = true
	err := pgb.CheckCreateTreasurySummaryTable()
	if err != nil {
		log.Errorf("Check exist and create treasury summary table failed: %v", err)
		pgb.TreasurySummarySyncing = false
		return err
	}

	//check synchronized month
	var summaryRows = make([]*dbtypes.AddressSummaryRow, 0)
	sumRows, _ := pgb.db.QueryContext(pgb.ctx, internal.SelectTreasurySummaryRows)
	for sumRows.Next() {
		var addrSum dbtypes.AddressSummaryRow
		var rowId, spentValue, receivedValue, taddValue int64
		err := sumRows.Scan(&rowId, &addrSum.Time, &spentValue, &receivedValue, &taddValue, &addrSum.MonthCreditRowIndex, &addrSum.MonthDebitRowIndex, &addrSum.Saved)
		addrSum.Id = uint64(rowId)
		addrSum.SpentValue = uint64(spentValue)
		addrSum.ReceivedValue = uint64(receivedValue)
		addrSum.TAdd = uint64(taddValue)
		if err == nil {
			summaryRows = append(summaryRows, &addrSum)
		}
	}

	//get firt address item to get next time
	var startTimeDef dbtypes.TimeDef
	firstErr := pgb.db.QueryRowContext(pgb.ctx, internal.SelectTreasuryFirstRowFromOldest).Scan(&startTimeDef)
	if firstErr != nil {
		pgb.TreasurySummarySyncing = false
		return nil
	}
	startTime := startTimeDef.T
	now := time.Now()

	//Get all data of treasury
	//get data by month
	monthCreditCountRows, err := pgb.db.QueryContext(pgb.ctx, internal.SelectTreasuryTBaseRowCountByMonth)
	if err != nil && err != sql.ErrNoRows {
		pgb.TreasurySummarySyncing = false
		return err
	}
	monthDeditCountRows, err := pgb.db.QueryContext(pgb.ctx, internal.SelectTreasurySpendRowCountByMonth)
	if err != nil && err != sql.ErrNoRows {
		pgb.TreasurySummarySyncing = false
		return err
	}
	creditCountRows := pgb.ConvertMonthRowsCount(monthCreditCountRows)
	debitCountRows := pgb.ConvertMonthRowsCount(monthDeditCountRows)
	stopFlg := false
	_, tipHeight := pgb.BestBlock()
	maturityHeight := tipHeight - int64(pgb.chainParams.CoinbaseMaturity)
	for !stopFlg {
		if now.Month() == startTime.Month() && now.Year() == startTime.Year() {
			stopFlg = true
		}
		exist, isSavedFlg, summaryRow := pgb.CheckSavedSummary(summaryRows, startTime)
		if exist && isSavedFlg {
			startTime = startTime.AddDate(0, 1, 0)
			continue
		}
		//get start of month
		startDay := startTime.AddDate(0, 0, -startTime.Day()+1)
		startDay = time.Date(startDay.Year(), startDay.Month(), startDay.Day(), 0, 0, 0, 0, time.Local)
		//get end of month
		endDay := startTime.AddDate(0, 1, -startTime.Day())
		endDay = time.Date(endDay.Year(), endDay.Month(), endDay.Day(), 23, 59, 59, 999999999, time.Local)
		//get data by month
		rows, err := pgb.db.QueryContext(pgb.ctx, internal.SelectTreasuryRowsByPeriod, startDay, endDay, maturityHeight)
		if err != nil && err != sql.ErrNoRows {
			pgb.TreasurySummarySyncing = false
			return err
		}
		treasuryRows := pgb.ConvertToTreasuryList(rows)
		received := uint64(0)
		spent := uint64(0)
		tadd := uint64(0)
		for _, treasuryRow := range treasuryRows {
			if treasuryRow.Type == int(stake.TxTypeTreasuryBase) {
				received += uint64(treasuryRow.Amount)
			} else if treasuryRow.Type == int(stake.TxTypeTAdd) {
				tadd += uint64(treasuryRow.Amount)
			} else if treasuryRow.Type == int(stake.TxTypeTSpend) {
				spent += uint64(treasuryRow.Amount)
			}
		}
		received += tadd
		//next month
		var nextMonth = startTime.AddDate(0, 1, 0)
		if spent == 0 && received == 0 {
			startTime = nextMonth
			continue
		}
		var thisMonth = false
		creditIndex := int64(-1)
		debitIndex := int64(-1)
		if now.Month() == startTime.Month() && now.Year() == startTime.Year() {
			thisMonth = true
		} else {
			creditIndex = pgb.GetStartRevertIndexAddressRow(creditCountRows, startTime)
			debitIndex = pgb.GetStartRevertIndexAddressRow(debitCountRows, startTime)
		}
		//set revert index
		if summaryRow == nil {
			//insert new row
			newSumRow := dbtypes.AddressSummaryRow{
				Time:                dbtypes.NewTimeDef(startDay),
				SpentValue:          spent,
				ReceivedValue:       received,
				TAdd:                tadd,
				Saved:               !thisMonth,
				MonthCreditRowIndex: creditIndex,
				MonthDebitRowIndex:  debitIndex,
			}
			var id uint64
			pgb.db.QueryRow(internal.InsertTreasurySummaryRow, newSumRow.Time, int64(newSumRow.SpentValue), int64(newSumRow.ReceivedValue), int64(newSumRow.TAdd), newSumRow.Saved, newSumRow.MonthCreditRowIndex, newSumRow.MonthDebitRowIndex).Scan(&id)
			startTime = nextMonth
			continue
		}
		if received == summaryRow.ReceivedValue && spent == summaryRow.SpentValue {
			startTime = nextMonth
			continue
		}
		summaryRow.SpentValue = spent
		summaryRow.ReceivedValue = received
		summaryRow.TAdd = tadd
		summaryRow.Saved = !thisMonth
		summaryRow.MonthCreditRowIndex = creditIndex
		summaryRow.MonthDebitRowIndex = debitIndex
		//update row
		pgb.db.Exec(internal.UpdateTreasurySummaryByTotalAndSpent, int64(spent), int64(received), int64(tadd), !thisMonth, debitIndex, creditIndex, int64(summaryRow.Id))
		startTime = nextMonth
	}
	pgb.TreasurySummarySyncing = false
	return nil
}

func (pgb *ChainDB) GetStartRevertIndexAddressRow(rowCountMonths []*dbtypes.AddressesMonthRowsCount, currentMonth time.Time) int64 {
	count := int64(0)
	for _, monthRow := range rowCountMonths {
		count += int64(monthRow.Count)
		// if is month
		if monthRow.Month.T.Month() == currentMonth.Month() && monthRow.Month.T.Year() == currentMonth.Year() {
			return count
		}
	}
	return -1
}

func (pgb *ChainDB) CheckSavedSummary(summaryRows []*dbtypes.AddressSummaryRow, compareTime time.Time) (hasData bool, isSaved bool, sumRow *dbtypes.AddressSummaryRow) {
	for _, summaryRow := range summaryRows {
		if summaryRow.Time.T.Month() == compareTime.Month() && summaryRow.Time.T.Year() == compareTime.Year() {
			return true, summaryRow.Saved, summaryRow
		}
	}
	return false, false, nil
}

func (pgb *ChainDB) ConvertMonthRowsCount(rows *sql.Rows) []*dbtypes.AddressesMonthRowsCount {
	addressMonthCountRows := make([]*dbtypes.AddressesMonthRowsCount, 0)
	for rows.Next() {
		var rowRecord dbtypes.AddressesMonthRowsCount

		err := rows.Scan(&rowRecord.Month, &rowRecord.Count)

		if err != nil {
			return addressMonthCountRows
		}

		addressMonthCountRows = append(addressMonthCountRows, &rowRecord)
	}
	return addressMonthCountRows
}

func (pgb *ChainDB) ConvertToTreasuryList(rows *sql.Rows) []*dbtypes.TreasuryTx {
	txns := make([]*dbtypes.TreasuryTx, 0)
	var err error
	for rows.Next() {
		var tx dbtypes.TreasuryTx
		var mainchain bool
		err = rows.Scan(&tx.TxID, &tx.Type, &tx.Amount, &tx.BlockHash, &tx.BlockHeight, &tx.BlockTime, &mainchain)
		if err != nil {
			return txns
		}
		txns = append(txns, &tx)
	}
	return txns
}

func (pgb *ChainDB) ConvertToAddressObj(rows *sql.Rows) []*dbtypes.AddressRow {
	addressRows := make([]*dbtypes.AddressRow, 0)
	for rows.Next() {
		var id uint64
		var addr dbtypes.AddressRow
		var matchingTxHash sql.NullString
		var txVinIndex, vinDbID sql.NullInt64

		err := rows.Scan(&id, &addr.Address, &matchingTxHash, &addr.TxHash, &addr.TxType,
			&addr.ValidMainChain, &txVinIndex, &addr.TxBlockTime, &vinDbID,
			&addr.Value, &addr.IsFunding)

		if err != nil {
			return addressRows
		}

		if matchingTxHash.Valid {
			addr.MatchingTxHash = matchingTxHash.String
		}
		if txVinIndex.Valid {
			addr.TxVinVoutIndex = uint32(txVinIndex.Int64)
		}
		if vinDbID.Valid {
			addr.VinVoutDbID = uint64(vinDbID.Int64)
		}

		addressRows = append(addressRows, &addr)
	}
	return addressRows
}

// SyncChainDB stores in the DB all blocks on the main chain available from the
// RPC client. The table indexes may be force-dropped and recreated by setting
// newIndexes to true. The quit channel is used to break the sync loop. For
// example, closing the channel on SIGINT.
func (pgb *ChainDB) SyncChainDB(ctx context.Context, client rpcutils.BlockFetcher,
	updateAllAddresses, newIndexes bool, updateExplorer chan *chainhash.Hash,
	barLoad chan *dbtypes.ProgressBarLoad) (int64, error) {
	// Note that we are doing a batch blockchain sync.
	pgb.InBatchSync = true
	defer func() { pgb.InBatchSync = false }()

	// Get the chain servers's best block.
	_, nodeHeight, err := client.GetBestBlock(ctx)
	if err != nil {
		return -1, fmt.Errorf("GetBestBlock failed: %w", err)
	}

	// Retrieve the best block in the database from the meta table.
	lastBlock, err := pgb.HeightDB()
	if err != nil {
		return -1, fmt.Errorf("RetrieveBestBlockHeight: %w", err)
	}
	if lastBlock == -1 {
		log.Info("Tables are empty, starting fresh.")
	}

	// Remove indexes/constraints before an initial sync or when explicitly
	// requested to reindex and update spending information in the addresses
	// table.
	reindexing := newIndexes || lastBlock == -1

	// See if initial sync (initial block download) was previously completed.
	ibdComplete, err := IBDComplete(pgb.db)
	if err != nil {
		return lastBlock, fmt.Errorf("IBDComplete failed: %w", err)
	}

	// Check and report heights of the DBs. dbHeight is the lowest of the
	// heights, and may be -1 with an empty DB.
	stakeDBHeight := int64(pgb.stakeDB.Height())
	if lastBlock < -1 {
		panic("invalid starting height")
	}

	log.Info("Current best block (dcrd):       ", nodeHeight)
	log.Info("Current best block (primary db): ", lastBlock)
	log.Info("Current best block (stakedb):    ", stakeDBHeight)

	// Attempt to rewind stake database, if needed, forcing it to the lowest DB
	// height (or 0 if the lowest DB height is -1).
	if stakeDBHeight > lastBlock && stakeDBHeight > 0 {
		if lastBlock < 0 || stakeDBHeight > 2*lastBlock {
			return -1, fmt.Errorf("delete stake db (ffldb_stake) and try again")
		}
		log.Infof("Rewinding stake node from %d to %d", stakeDBHeight, lastBlock)
		// Rewind best node in ticket DB to larger of lowest DB height or zero.
		stakeDBHeight, err = pgb.RewindStakeDB(ctx, lastBlock)
		if err != nil {
			return lastBlock, fmt.Errorf("RewindStakeDB failed: %w", err)
		}
	}

	// When IBD is not yet completed, force reindexing and update of full
	// spending info in addresses table after block sync.
	if !ibdComplete {
		if lastBlock > -1 {
			log.Warnf("Detected that initial sync was previously started but not completed!")
		}
		if !reindexing {
			reindexing = true
			log.Warnf("Forcing table reindexing.")
		}
		if !updateAllAddresses {
			updateAllAddresses = true
			log.Warnf("Forcing full update of spending information in addresses table.")
		}
	}

	if pgb.utxoCache.Size() == 0 { // entries at any height implies it's warmed by previous sync
		log.Infof("Collecting all UTXO data prior to height %d...", lastBlock+1)
		utxos, err := RetrieveUTXOs(ctx, pgb.db)
		if err != nil {
			return -1, fmt.Errorf("RetrieveUTXOs: %w", err)
		}

		log.Infof("Pre-warming UTXO cache with %d UTXOs...", len(utxos))
		pgb.InitUtxoCache(utxos)
		log.Infof("UTXO cache is ready.")
	}

	if reindexing {
		// Remove any existing indexes.
		log.Info("Large bulk load: Removing indexes and disabling duplicate checks.")
		err = pgb.DeindexAll()
		if err != nil && !strings.Contains(err.Error(), "does not exist") {
			return lastBlock, err
		}

		// Create the temporary index on addresses(tx_vin_vout_row_id) that
		// prevents the stake disapproval updates to a block to cause massive
		// slowdown during initial sync without an index on tx_vin_vout_row_id.
		log.Infof("Creating temporary index on addresses(tx_vin_vout_row_id).")
		_, err = pgb.db.Exec(`CREATE INDEX IF NOT EXISTS idx_addresses_vinvout_id_tmp ` +
			`ON addresses(tx_vin_vout_row_id)`)
		if err != nil {
			return lastBlock, err
		}

		// Disable duplicate checks on insert queries since the unique indexes
		// that enforce the constraints will not exist.
		pgb.EnableDuplicateCheckOnInsert(false)
	} else {
		// When the unique indexes exist, inserts should check for conflicts
		// with the tables' constraints.
		pgb.EnableDuplicateCheckOnInsert(true)
	}

	// When reindexing or adding a large amount of data, ANALYZE tables.
	requireAnalyze := reindexing || nodeHeight-lastBlock > 10000

	// If reindexing or batch table data updates are required, set the
	// ibd_complete flag to false if it is not already false.
	if ibdComplete && (reindexing || updateAllAddresses) {
		// Set meta.ibd_complete = FALSE.
		if err = SetIBDComplete(pgb.db, false); err != nil {
			return nodeHeight, fmt.Errorf("failed to set meta.ibd_complete: %w", err)
		}
	}

	stages := 1 // without indexing and spend updates, just block import
	if reindexing {
		stages += 3 // duplicate data removal, indexing, deep analyze
	} else if requireAnalyze {
		stages++ // not reindexing, just quick analyzing because far behind
	}
	if updateAllAddresses {
		stages++ // addresses table spending info update
	}

	// Safely send sync status updates on barLoad channel, and set the channel
	// to nil if the buffer is full.
	sendProgressUpdate := func(p *dbtypes.ProgressBarLoad) {
		if barLoad == nil {
			return
		}
		select {
		case barLoad <- p:
		default:
			log.Debugf("(*ChainDB).SyncChainDB: barLoad chan full. Halting sync progress updates.")
			barLoad = nil
		}
	}

	// Safely send new block hash on updateExplorer channel, and set the channel
	// to nil if the buffer is full.
	sendPageData := func(hash *chainhash.Hash) {
		if updateExplorer == nil {
			return
		}
		select {
		case updateExplorer <- hash:
		default:
			log.Debugf("(*ChainDB).SyncChainDB: updateExplorer chan full. Halting explorer updates.")
			updateExplorer = nil
		}
	}

	// Add the various updates that should run on successful sync.
	sendProgressUpdate(&dbtypes.ProgressBarLoad{
		Msg:   initialLoadSyncStatusMsg,
		BarID: dbtypes.InitialDBLoad,
	})
	// Addresses table sync should only run if bulk update is enabled.
	if updateAllAddresses {
		sendProgressUpdate(&dbtypes.ProgressBarLoad{
			Msg:   addressesSyncStatusMsg,
			BarID: dbtypes.AddressesTableSync,
		})
	}

	// Total and rate statistics
	var totalTxs, totalVins, totalVouts, totalAddresses int64
	var lastTxs, lastVins, lastVouts int64
	tickTime := 20 * time.Second
	ticker := time.NewTicker(tickTime)
	startTime := time.Now()
	startHeight := lastBlock + 1
	o := sync.Once{}
	speedReporter := func() {
		ticker.Stop()
		timeElapsed := time.Since(startTime)
		secsElapsed := timeElapsed.Seconds()
		if int64(secsElapsed) == 0 {
			return
		}
		totalVoutPerSec := totalVouts / int64(secsElapsed)
		totalTxPerSec := totalTxs / int64(secsElapsed)
		if totalTxs == 0 {
			return
		}
		log.Infof("Avg. speed: %d tx/s, %d vout/s", totalTxPerSec, totalVoutPerSec)
		syncedBlocks := nodeHeight - startHeight + 1
		log.Infof("Block import elapsed: %.2f minutes, %d blocks (%.2f blocks/s)",
			timeElapsed.Minutes(), syncedBlocks, float64(syncedBlocks)/timeElapsed.Seconds())
	}
	speedReport := func() { o.Do(speedReporter) }
	defer speedReport()

	lastProgressUpdateTime := startTime

	stage := 1
	log.Infof("Beginning SYNC STAGE %d of %d (block data import).", stage, stages)

	importBlocks := func(start int64) (int64, error) {
		for ib := start; ib <= nodeHeight; ib++ {
			// Check for quit signal.
			select {
			case <-ctx.Done():
				return ib - 1, fmt.Errorf("sync cancelled at height %d", ib)
			default:
			}

			// Progress logging
			if (ib-1)%rescanLogBlockChunk == 0 || ib == startHeight {
				if ib == 0 {
					log.Infof("Scanning genesis block into chain db.")
				} else {
					_, nodeHeight, err = client.GetBestBlock(ctx)
					if err != nil {
						return ib, fmt.Errorf("GetBestBlock failed: %w", err)
					}
					endRangeBlock := rescanLogBlockChunk * (1 + (ib-1)/rescanLogBlockChunk)
					if endRangeBlock > nodeHeight {
						endRangeBlock = nodeHeight
					}
					log.Infof("Processing blocks %d to %d...", ib, endRangeBlock)

					if barLoad != nil {
						timeTakenPerBlock := time.Since(lastProgressUpdateTime).Seconds() /
							float64(endRangeBlock-ib)
						sendProgressUpdate(&dbtypes.ProgressBarLoad{
							From:      ib,
							To:        nodeHeight,
							Timestamp: int64(timeTakenPerBlock * float64(nodeHeight-endRangeBlock)),
							Msg:       initialLoadSyncStatusMsg,
							BarID:     dbtypes.InitialDBLoad,
						})
						lastProgressUpdateTime = time.Now()
					}
				}
			}

			// Speed report
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

			// Get the block, making it available to stakedb, which will signal on
			// the above channel when it is done connecting it.
			block, blockHash, err := rpcutils.GetBlock(ib, client)
			if err != nil {
				log.Errorf("UpdateToBlock (%d) failed: %v", ib, err)
				return ib - 1, fmt.Errorf("UpdateToBlock (%d) failed: %w", ib, err)
			}

			// Advance stakedb height, which should always be less than or equal to
			// PSQL height. stakedb always has genesis, as enforced by the rewinding
			// code in this function.
			if ib > stakeDBHeight {
				behind := ib - stakeDBHeight
				if behind != 1 {
					panic(fmt.Sprintf("About to connect the wrong block: %d, %d\n"+
						"The stake database is corrupted. "+
						"Restart with --purge-n-blocks=%d to recover.",
						ib, stakeDBHeight, 2*behind))
				}
				if err = pgb.stakeDB.ConnectBlock(block); err != nil {
					return ib - 1, pgb.supplementUnknownTicketError(err)
				}
			}
			stakeDBHeight = int64(pgb.stakeDB.Height()) // i

			// Get the chainwork
			chainWork, err := rpcutils.GetChainWork(client, blockHash)
			if err != nil {
				return ib - 1, fmt.Errorf("GetChainWork failed (%s): %w", blockHash, err)
			}

			// Store data from this block in the database.
			isValid, isMainchain := true, true
			// updateExisting is ignored if dupCheck=false, but set it to true since
			// SyncChainDB is processing main chain blocks.
			updateExisting := true
			numVins, numVouts, numAddresses, err := pgb.StoreBlock(block.MsgBlock(),
				isValid, isMainchain, updateExisting, !updateAllAddresses, chainWork)
			if err != nil {
				return ib - 1, fmt.Errorf("StoreBlock failed: %w", err)
			}
			totalVins += numVins
			totalVouts += numVouts
			totalAddresses += numAddresses

			// Total transactions is the sum of regular and stake transactions.
			totalTxs += int64(len(block.STransactions()) + len(block.Transactions()))

			// Update explorer pages at intervals of 20 blocks if the update channel
			// is active (non-nil and not closed).
			if !updateAllAddresses && ib%20 == 0 {
				log.Tracef("Updating the explorer with information for block %v", ib)
				sendPageData(blockHash)
			}

			// Update node height, the end condition for the loop.
			if ib == nodeHeight {
				_, nodeHeight, err = client.GetBestBlock(ctx)
				if err != nil {
					return ib, fmt.Errorf("GetBestBlock failed: %w", err)
				}
			}
		}
		return nodeHeight, nil
	}

	// Start syncing blocks.
	endHeight, err := importBlocks(startHeight)
	if err != nil {
		return endHeight, err
	} // else endHeight == nodeHeight

	// Final speed report
	speedReport()

	// Signal the final height to any heightClients.
	pgb.SignalHeight(uint32(nodeHeight))

	// Signal the end of the initial load sync.
	sendProgressUpdate(&dbtypes.ProgressBarLoad{
		From:  nodeHeight,
		To:    nodeHeight,
		Msg:   initialLoadSyncStatusMsg,
		BarID: dbtypes.InitialDBLoad,
	})

	// Index and analyze tables.
	var analyzed bool
	if reindexing {
		stage++
		log.Infof("Beginning SYNC STAGE %d of %d (duplicate row removal).", stage, stages)

		// Drop the temporary index on addresses(tx_vin_vout_row_id).
		log.Infof("Dropping temporary index on addresses(tx_vin_vout_row_id).")
		_, err = pgb.db.Exec(`DROP INDEX IF EXISTS idx_addresses_vinvout_id_tmp;`)
		if err != nil {
			return nodeHeight, err
		}

		// To build indexes, there must NOT be duplicate rows in terms of the
		// constraints defined by the unique indexes. Duplicate transactions,
		// vins, and vouts can end up in the tables when identical transactions
		// are included in multiple blocks. This happens when a block is
		// invalidated and the transactions are subsequently re-mined in another
		// block. Remove these before indexing.
		log.Infof("Finding and removing duplicate table rows before indexing...")
		if err = pgb.DeleteDuplicates(barLoad); err != nil {
			return 0, err
		}

		stage++
		log.Infof("Beginning SYNC STAGE %d of %d (table indexing and analyzing).", stage, stages)

		// Create all indexes except those on addresses.matching_tx_hash and all
		// tickets indexes.
		if err = pgb.IndexAll(barLoad); err != nil {
			return nodeHeight, fmt.Errorf("IndexAll failed: %w", err)
		}

		if !updateAllAddresses {
			// The addresses.matching_tx_hash index is not included in IndexAll.
			if err = IndexAddressTableOnMatchingTxHash(pgb.db); err != nil {
				return nodeHeight, fmt.Errorf("IndexAddressTableOnMatchingTxHash failed: %w", err)
			}
		}

		// Tickets table indexes are not included in IndexAll. (move it into
		// IndexAll since tickets are always updated on the fly and not part of
		// updateAllAddresses?)
		if err = pgb.IndexTicketsTable(barLoad); err != nil {
			return nodeHeight, fmt.Errorf("IndexTicketsTable failed: %w", err)
		}

		// Deep ANALYZE all tables.
		stage++
		log.Infof("Beginning SYNC STAGE %d of %d (deep database ANALYZE).", stage, stages)
		if err = AnalyzeAllTables(pgb.db, deepStatsTarget); err != nil {
			return nodeHeight, fmt.Errorf("failed to ANALYZE tables: %w", err)
		}
		analyzed = true
	}

	select {
	case <-ctx.Done():
		return nodeHeight, fmt.Errorf("sync cancelled")
	default:
	}

	// Batch update addresses table with spending info.
	if updateAllAddresses {
		stage++
		log.Infof("Beginning SYNC STAGE %d of %d (setting spending info in addresses table). "+
			"This will take a while.", stage, stages)

		// Analyze vouts and transactions tables first.
		if !analyzed {
			log.Infof("Performing an ANALYZE(%d) on vouts table...", deepStatsTarget)
			if err = AnalyzeTable(pgb.db, "vouts", deepStatsTarget); err != nil {
				return nodeHeight, fmt.Errorf("failed to ANALYZE vouts table: %w", err)
			}
			log.Infof("Performing an ANALYZE(%d) on transactions table...", deepStatsTarget)
			if err = AnalyzeTable(pgb.db, "transactions", deepStatsTarget); err != nil {
				return nodeHeight, fmt.Errorf("failed to ANALYZE transactions table: %w", err)
			}
		}

		// Drop the index on addresses.matching_tx_hash if it exists.
		_ = DeindexAddressTableOnMatchingTxHash(pgb.db) // ignore error if the index is absent

		numAddresses, err := pgb.UpdateSpendingInfoInAllAddresses(barLoad)
		if err != nil {
			return nodeHeight, fmt.Errorf("UpdateSpendingInfoInAllAddresses FAILED: %w", err)
		}
		log.Infof("Updated %d rows of addresses table.", numAddresses)

		log.Info("Indexing addresses table on matching_tx_hash...")
		if err = IndexAddressTableOnMatchingTxHash(pgb.db); err != nil {
			return nodeHeight, fmt.Errorf("IndexAddressTableOnMatchingTxHash failed: %w", err)
		}

		// Deep ANALYZE the newly indexed addresses table.
		log.Infof("Performing an ANALYZE(%d) on addresses table...", deepStatsTarget)
		if err = AnalyzeTable(pgb.db, "addresses", deepStatsTarget); err != nil {
			return nodeHeight, fmt.Errorf("failed to ANALYZE addresses table: %w", err)
		}
	}

	select {
	case <-ctx.Done():
		return nodeHeight, fmt.Errorf("sync cancelled")
	default:
	}

	// Quickly ANALYZE all tables if not already done after indexing.
	if !analyzed && requireAnalyze {
		stage++
		log.Infof("Beginning SYNC STAGE %d of %d (quick database ANALYZE).", stage, stages)
		if err = AnalyzeAllTables(pgb.db, quickStatsTarget); err != nil {
			return nodeHeight, fmt.Errorf("failed to ANALYZE tables: %w", err)
		}
	}

	// Set meta.ibd_complete = TRUE.
	if err = SetIBDComplete(pgb.db, true); err != nil {
		return nodeHeight, fmt.Errorf("failed to set meta.ibd_complete: %w", err)
	}

	select {
	case <-ctx.Done():
		return nodeHeight, fmt.Errorf("sync cancelled")
	default:
	}

	// After sync and indexing, must use upsert statement, which checks for
	// duplicate entries and updates instead of throwing and error and panicing.
	pgb.EnableDuplicateCheckOnInsert(true)

	// Catch up after indexing and other updates.
	_, nodeHeight, err = client.GetBestBlock(ctx)
	if err != nil {
		return nodeHeight, fmt.Errorf("GetBestBlock failed: %w", err)
	}
	if nodeHeight > endHeight {
		log.Infof("Catching up with network at block height %d from %d...", nodeHeight, endHeight)
		if endHeight, err = importBlocks(endHeight); err != nil {
			return endHeight, err
		}
	}

	// Caller should pre-fetch treasury balance when ready.

	if barLoad != nil {
		barID := dbtypes.InitialDBLoad
		if updateAllAddresses {
			barID = dbtypes.AddressesTableSync
		}
		sendProgressUpdate(&dbtypes.ProgressBarLoad{
			BarID:    barID,
			Subtitle: "sync complete",
		})
	}

	log.Infof("SYNC COMPLETED at height %d. Delta:\n\t\t\t% 10d blocks\n\t\t\t% 10d transactions\n\t\t\t% 10d ins\n\t\t\t% 10d outs\n\t\t\t% 10d addresses",
		nodeHeight, nodeHeight-startHeight+1, totalTxs, totalVins, totalVouts, totalAddresses)

	return nodeHeight, err
}

func parseUnknownTicketError(err error) (hash *chainhash.Hash) {
	// Look for the dreaded ticket database error.
	re := regexp.MustCompile(`unknown ticket (\w*) spent in block`)
	matches := re.FindStringSubmatch(err.Error())
	var unknownTicket string
	if len(matches) <= 1 {
		// Unable to parse the error as unknown ticket message.
		return
	}
	unknownTicket = matches[1]
	ticketHash, err1 := chainhash.NewHashFromStr(unknownTicket)
	if err1 != nil {
		return
	}
	return ticketHash
}

// supplementUnknownTicketError checks the passed error for the "unknown ticket
// [hash] spent in block" message, and supplements matching errors with the
// block height of the ticket and switches to help recovery.
func (pgb *ChainDB) supplementUnknownTicketError(err error) error {
	ticketHash := parseUnknownTicketError(err)
	if ticketHash == nil {
		return err
	}
	txraw, err1 := pgb.Client.GetRawTransactionVerbose(context.TODO(), ticketHash)
	if err1 != nil {
		return err
	}
	badTxBlock := txraw.BlockHeight
	sDBHeight := int64(pgb.stakeDB.Height())
	numToPurge := sDBHeight - badTxBlock + 1
	return fmt.Errorf("%v\n\t**** Unknown ticket was mined in block %d. "+
		"Try \"--purge-n-blocks=%d to recover. ****",
		err, badTxBlock, numToPurge)
}

func (pgb *ChainDB) SyncDecredAtomicSwap() error {
	// Get list of unsynchronized blocks atomic swap transaction
	var syncHeights []int64
	rows, err := pgb.db.QueryContext(pgb.ctx, internal.SelectBlocksUnsynchoronized)
	if err != nil {
		log.Errorf("Get list of unsynchronized decred blocks failed %v", err)
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var height int64
		if err = rows.Scan(&height); err != nil {
			log.Errorf("Scan decred blocks unsync failed %v", err)
			return err
		}
		syncHeights = append(syncHeights, height)
	}
	if err = rows.Err(); err != nil {
		log.Errorf("Scan decred blocks unsync failed %v", err)
		return err
	}
	for _, syncHeight := range syncHeights {
		err = pgb.SyncDecredAtomicSwapData(syncHeight)
		if err != nil {
			log.Errorf("Scan decred blocks unsync failed %v", err)
			return err
		}
	}
	return nil
}

func (pgb *ChainDB) SyncDecredAtomicSwapData(height int64) error {
	log.Infof("Start Sync DCR swap data with height: %d", height)
	blockhash, err := pgb.Client.GetBlockHash(pgb.ctx, height)
	if err != nil {
		return err
	}

	msgBlock, err := pgb.Client.GetBlock(pgb.ctx, blockhash)
	if err != nil {
		return err
	}
	// Check all regular tree txns except coinbase.
	for _, tx := range msgBlock.Transactions[1:] {
		// This will only identify the redeem and refund txns, unlike the use of
		// TxAtomicSwapsInfo in API and explorer calls.
		swapTxns, err := txhelpers.MsgTxAtomicSwapsInfo(tx, nil, pgb.chainParams)
		if err != nil {
			log.Warnf("MsgTxAtomicSwapsInfo: %v", err)
			continue
		}
		if swapTxns == nil || swapTxns.Found == "" {
			continue
		}
		for _, red := range swapTxns.Redemptions {
			err = InsertSwap(pgb.db, pgb.ctx, pgb.Client, height, red, false)
			if err != nil {
				log.Errorf("InsertSwap: %v", err)
			}
		}
		for _, ref := range swapTxns.Refunds {
			err = InsertSwap(pgb.db, pgb.ctx, pgb.Client, height, ref, true)
			if err != nil {
				log.Errorf("InsertSwap: %v", err)
			}
		}
	}
	// update block synced status
	var blockId int64
	err = pgb.db.QueryRow(internal.UpdateBlockAtomicSwapSynced, height).Scan(&blockId)
	if err != nil {
		log.Errorf("Update DCR block synced status failed: %v", err)
		return err
	}
	log.Infof("Finish Sync DCR swap data with height: %d", height)
	return nil
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
	log.Infof("Start Sync BTC swap data with height: %d", height)
	blockhash, err := pgb.BtcClient.GetBlockHash(height)
	if err != nil {
		return err
	}

	msgBlock, err := pgb.BtcClient.GetBlock(blockhash)
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
	log.Infof("Finish Sync BTC swap data with height: %d", height)
	return nil
}

func (pgb *ChainDB) SyncLTCAtomicSwapData(height int64) error {
	log.Infof("Start Sync LTC swap data with height: %d", height)
	blockhash, err := pgb.LtcClient.GetBlockHash(height)
	if err != nil {
		return err
	}

	msgBlock, err := pgb.LtcClient.GetBlock(blockhash)
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
	log.Infof("Finish Sync LTC swap data with height: %d", height)
	return nil
}
