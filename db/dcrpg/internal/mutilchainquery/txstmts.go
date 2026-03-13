package mutilchainquery

import (
	"fmt"

	"github.com/decred/dcrdata/v8/mutilchain"
)

const (
	// Insert
	insertTxRow0 = `INSERT INTO %stransactions (
		block_hash, block_height, block_time, time,
		tx_type, version, tx_hash, block_index, 
		lock_time, size, spent, sent, fees, 
		num_vin, num_vout)
	VALUES (
		$1, $2, $3, $4, 
		$5, $6, $7, $8, $9,
		$10, $11, $12, $13, $14, $15) `
	insertXmrTxRow0 = `INSERT INTO xmrtransactions (
		block_hash, block_height, block_time, time,
		tx_type, version, tx_hash, block_index, is_ringct, rct_type,
		tx_public_key, prunable_size, size, spent, sent, fees, 
		num_vin, num_vout, coinbase)
	VALUES (
		$1, $2, $3, $4, 
		$5, $6, $7, $8, $9,
		$10, $11, $12, $13, $14, $15,
		$16, $17, $18, $19)`
	insertTxRow           = insertTxRow0 + ` RETURNING id;`
	insertTxRowChecked    = insertTxRow0 + ` ON CONFLICT (tx_hash) DO NOTHING RETURNING id;`
	insertXmrTxRow        = insertXmrTxRow0 + ` RETURNING id;`
	insertXmrTxRowChecked = insertXmrTxRow0 + ` ON CONFLICT (tx_hash) DO NOTHING RETURNING id;`

	CreateXmrTransactionTable = `CREATE TABLE IF NOT EXISTS xmrtransactions (
		id SERIAL8 PRIMARY KEY,
		/*block_db_id INT4,*/
		block_hash TEXT,
		block_height INT8,
		block_time INT8,
		time INT8,
		tx_type INT4,
		version INT4,
		tx_hash TEXT,
		block_index INT4,
		is_ringct BOOLEAN DEFAULT FALSE,
		rct_type INT,
		tx_public_key TEXT,
		prunable_size INT,
		size INT4,
		spent INT8,
		sent INT8,
		fees INT8,
		num_vin INT4,
		coinbase BOOLEAN DEFAULT FALSE,
		num_vout INT4
	);`

	CreateTransactionTable = `CREATE TABLE IF NOT EXISTS %stransactions (
		id SERIAL8 PRIMARY KEY,
		block_hash TEXT,
		block_height INT8,
		block_time INT8,
		time INT8,
		tx_type INT4,
		version INT4,
		tx_hash TEXT,
		block_index INT4,
		lock_time INT4,
		size INT4,
		spent INT8,
		sent INT8,
		fees INT8,
		num_vin INT4,
		num_vout INT4
	);`

	SelectTotalTransaction    = `SELECT count(*) FROM %stransactions;`
	SelectTxByHash            = `SELECT id, block_hash, block_index FROM %stransactions WHERE tx_hash = $1;`
	SelectTxsByBlockHash      = `SELECT id, tx_hash, block_index FROM %stransactions WHERE block_hash = $1;`
	SelectXMRTxsByBlockHeight = `SELECT tx_hash, fees, size FROM xmrtransactions WHERE block_height = $1;`

	SelectFullTxByHash = `SELECT id, block_hash, block_height, block_time, 
		time, tx_type, version, tx_hash, block_index, lock_time, 
		size, spent, sent, fees, num_vin, num_vout 
		FROM %stransactions WHERE tx_hash = $1;`

	SelectRegularTxByHash = `SELECT id, block_hash, block_index FROM %stransactions WHERE tx_hash = $1;`

	SelectTxsBlocks = `SELECT block_height, block_hash, block_index
		FROM %stransactions
		WHERE tx_hash = $1 ORDER BY block_height DESC;`

	SelectTxHashsWithMinHeight = `SELECT tx_hash FROM %stransactions WHERE block_height > $1;`

	DeleteTxsWithMinBlockHeight = `DELETE FROM %stransactions WHERE block_height > $1`

	IndexTransactionTableOnHashes = `CREATE INDEX uix_%stx_hash_blhash
		 ON %stransactions(tx_hash, block_hash)
		 ;` // STORING (block_hash, block_index, tree)
	DeindexTransactionTableOnHashes = `DROP INDEX uix_%stx_hash_blhash;`

	IndexTransactionTableOnBlockHeight   = `CREATE INDEX uix_%stx_block_height ON %stransactions(block_height);`
	DeindexTransactionTableOnBlockHeight = `DROP INDEX uix_%stx_block_height CASCADE;`

	IndexTransactionTableOnTxHash   = `CREATE UNIQUE INDEX uix_%stx_txhash ON %stransactions(tx_hash);`
	DeindexTransactionTableOnTxHash = `DROP INDEX uix_%stx_txhash CASCADE;`

	//SelectTxByPrevOut = `SELECT * FROM transactions WHERE vins @> json_build_array(json_build_object('prevtxhash',$1)::jsonb)::jsonb;`
	//SelectTxByPrevOut = `SELECT * FROM transactions WHERE vins #>> '{"prevtxhash"}' = '$1';`

	//SelectTxsByPrevOutTx = `SELECT * FROM transactions WHERE vins @> json_build_array(json_build_object('prevtxhash',$1::TEXT)::jsonb)::jsonb;`
	// '[{"prevtxhash":$1}]'

	// RetrieveVoutValues = `WITH voutsOnly AS (
	// 		SELECT unnest((vouts)) FROM transactions WHERE id = $1
	// 	) SELECT v.* FROM voutsOnly v;`
	// RetrieveVoutValues = `SELECT vo.value
	// 	FROM  transactions txs, unnest(txs.vouts) vo
	// 	WHERE txs.id = $1;`
	// RetrieveVoutValue = `SELECT vouts[$2].value FROM transactions WHERE id = $1;`
	SelectFeesPerBlockAboveHeight = `
	SELECT block_height, SUM(fees) AS fees
	FROM %stransactions
	WHERE block_height > $1
	GROUP BY block_height
	ORDER BY block_height;`
	DeleteTxsOfOlderThan20Blocks = `DELETE FROM %stransactions WHERE block_height < $1;`
	Select24hAvgAndSumTxFee      = `SELECT COALESCE(SUM(fees)::bigint, 0) AS sum_fees_last_24h, COALESCE(ROUND(AVG(fees))::bigint, 0) AS avg_fees_last_24h
	FROM %stransactions WHERE time >= EXTRACT(EPOCH FROM now()) - 24*3600;`
	CheckAndRemoveDupplicateTxsRow = `WITH duplicates AS (
  		SELECT id, row_number() OVER (PARTITION BY tx_hash ORDER BY id) AS rn
  		FROM public.%stransactions
  		WHERE tx_hash IS NOT NULL)
		DELETE FROM public.%stransactions t
		USING duplicates d
		WHERE t.id = d.id
  		AND d.rn > 1;`

	SelectXmrUseRingCtTxsRate = `SELECT 
  		100.0 * SUM(CASE WHEN is_ringct THEN 1 ELSE 0 END) / COUNT(*) AS ringct_ratio
		FROM xmrtransactions;`
)

func MakeSelectFeesPerBlockAboveHeight(chainType string) string {
	return fmt.Sprintf(SelectFeesPerBlockAboveHeight, chainType)
}

func MakeDeleteTxsOfOlderThan20Blocks(chainType string) string {
	return fmt.Sprintf(DeleteTxsOfOlderThan20Blocks, chainType)
}

func MakeSelectTotalTransaction(chainType string) string {
	return fmt.Sprintf(SelectTotalTransaction, chainType)
}

func MakeSelectTxsBlocks(chainType string) string {
	return fmt.Sprintf(SelectTxsBlocks, chainType)
}

func MakeSelectFullTxByHash(chainType string) string {
	return fmt.Sprintf(SelectFullTxByHash, chainType)
}
func MakeIndexTransactionTableOnTxHash(chainType string) string {
	return fmt.Sprintf(IndexTransactionTableOnTxHash, chainType, chainType)
}

func MakeDeindexTransactionTableOnTxHash(chainType string) string {
	return fmt.Sprintf(DeindexTransactionTableOnTxHash, chainType)
}

func MakeIndexTransactionTableOnHashes(chainType string) string {
	return fmt.Sprintf(IndexTransactionTableOnHashes, chainType, chainType)
}

func MakeDeindexTransactionTableOnHashes(chainType string) string {
	return fmt.Sprintf(DeindexTransactionTableOnHashes, chainType)
}

func MakeIndexTransactionTableOnBlockHeight(chainType string) string {
	return fmt.Sprintf(IndexTransactionTableOnBlockHeight, chainType, chainType)
}

func MakeDeindexTransactionTableOnBlockHeight(chainType string) string {
	return fmt.Sprintf(DeindexTransactionTableOnBlockHeight, chainType)
}

// func makeTxInsertStatement(voutDbIDs, vinDbIDs []uint64, vouts []*dbtypes.Vout, checked bool) string {
// 	voutDbIDsBIGINT := makeARRAYOfBIGINTs(voutDbIDs)
// 	vinDbIDsBIGINT := makeARRAYOfBIGINTs(vinDbIDs)
// 	voutCompositeARRAY := makeARRAYOfVouts(vouts)
// 	var insert string
// 	if checked {
// 		insert = insertTxRowChecked
// 	} else {
// 		insert = insertTxRow
// 	}
// 	return fmt.Sprintf(insert, voutDbIDsBIGINT, voutCompositeARRAY, vinDbIDsBIGINT)
// }

func MakeTxInsertStatement(checked bool, chainType string) string {
	if checked {
		if chainType == mutilchain.TYPEXMR {
			return insertXmrTxRowChecked
		} else {
			return fmt.Sprintf(insertTxRowChecked, chainType)
		}
	}
	if chainType == mutilchain.TYPEXMR {
		return insertXmrTxRow
	} else {
		return fmt.Sprintf(insertTxRow, chainType)
	}
}

func CreateTransactionTableFunc(chainType string) string {
	if chainType == mutilchain.TYPEXMR {
		return CreateXmrTransactionTable
	}
	return fmt.Sprintf(CreateTransactionTable, chainType)
}

func CreateSelectTxHashsWithMinHeightQuery(chainType string) string {
	return fmt.Sprintf(SelectTxHashsWithMinHeight, chainType)
}

func CreateDeleteTxsWithMinBlockHeightQuery(chainType string) string {
	return fmt.Sprintf(DeleteTxsWithMinBlockHeight, chainType)
}

func CreateSelect24hAvgAndSumTxFee(chainType string) string {
	return fmt.Sprintf(Select24hAvgAndSumTxFee, chainType)
}

func CreateCheckAndRemoveDupplicateTxsRowQuery(chainType string) string {
	return fmt.Sprintf(CheckAndRemoveDupplicateTxsRow, chainType, chainType)
}
