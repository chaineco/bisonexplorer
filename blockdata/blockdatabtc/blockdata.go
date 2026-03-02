// Copyright (c) 2020-2021, The Decred developers
// Copyright (c) 2017, Jonathan Chappelow
// See LICENSE for details.

package blockdatabtc

import (
	"fmt"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	apitypes "github.com/decred/dcrdata/v8/api/types"
	"github.com/decred/dcrdata/v8/db/dbtypes"
	"github.com/decred/dcrdata/v8/mutilchain"
	"github.com/decred/dcrdata/v8/mutilchain/btcrpcutils"
	"github.com/decred/dcrdata/v8/stakedb"
)

// BlockData contains all the data collected by a Collector and stored
// by a BlockDataSaver. TODO: consider if pointers are desirable here.
type BlockData struct {
	Header         btcjson.GetBlockHeaderVerboseResult
	Connections    int32
	ExtraInfo      apitypes.BlockExplorerExtraInfo
	BlockchainInfo *btcjson.GetBlockChainInfoResult
}

// ToBlockSummary returns an apitypes.BlockDataBasic object from the blockdata
func (b *BlockData) ToBlockSummary() apitypes.BlockDataBasic {
	t := dbtypes.NewTimeDefFromUNIX(b.Header.Time)
	return apitypes.BlockDataBasic{
		Height:     uint32(b.Header.Height),
		Hash:       b.Header.Hash,
		Difficulty: b.Header.Difficulty,
		Time:       apitypes.TimeAPI{S: t},
	}
}

// ToBlockExplorerSummary returns a BlockExplorerBasic
func (b *BlockData) ToBlockExplorerSummary() apitypes.BlockExplorerBasic {
	extra := b.ExtraInfo
	t := dbtypes.NewTimeDefFromUNIX(b.Header.Time)
	return apitypes.BlockExplorerBasic{
		Height:                 uint32(b.Header.Height),
		BlockExplorerExtraInfo: extra,
		Time:                   t,
	}
}

// NodeClient is the RPC client functionality required by Collector.
type NodeClient interface {
	GetBlockCount() (int64, error)
	GetBlock(blockHash *chainhash.Hash) (*wire.MsgBlock, error)
	GetBlockHeaderVerbose(hash *chainhash.Hash) (*btcjson.GetBlockHeaderVerboseResult, error)
	GetBlockChainInfo() (*btcjson.GetBlockChainInfoResult, error)
	GetConnectionCount() (int64, error)
}

// Collector models a structure for the source of the blockdata
type Collector struct {
	mtx          sync.Mutex
	btcdChainSvr NodeClient
	netParams    *chaincfg.Params
	stakeDB      *stakedb.StakeDatabase
}

// NewCollector creates a new Collector.
func NewCollector(btcdChainSvr NodeClient, params *chaincfg.Params) *Collector {
	return &Collector{
		btcdChainSvr: btcdChainSvr,
		netParams:    params,
	}
}

// CollectAPITypes uses CollectBlockInfo to collect block data, then organizes
// it into the BlockDataBasic and StakeInfoExtended and dcrdataapi types.
func (t *Collector) CollectAPITypes(hash *chainhash.Hash) *apitypes.BlockDataBasic {
	blockDataBasic, _, _, _, err := t.CollectBlockInfo(hash)
	if err != nil {
		return nil
	}

	return blockDataBasic
}

// CollectBlockInfo uses the chain server and the stake DB to collect most of
// the block data required by Collect() that is specific to the block with the
// given hash.
func (t *Collector) CollectBlockInfo(hash *chainhash.Hash) (*apitypes.BlockDataBasic, *btcjson.GetBlockHeaderVerboseResult,
	*apitypes.BlockExplorerExtraInfo, *wire.MsgBlock, error) {
	// Retrieve block from dcrd.
	blockHeader, err := btcrpcutils.WithTimeout(func() (*btcjson.GetBlockHeaderVerboseResult, error) {
		return t.btcdChainSvr.GetBlockHeaderVerbose(hash)
	})
	msgBlock, blockErr := btcrpcutils.WithTimeout(func() (*wire.MsgBlock, error) {
		return t.btcdChainSvr.GetBlock(hash)
	})
	if err != nil || blockErr != nil {
		return nil, nil, nil, nil, fmt.Errorf("Retrieve block info error")
	}
	txLen := len(msgBlock.Transactions)
	// Coin supply and block subsidy. If either RPC fails, do not immediately
	// return. Attempt acquisition of other data for this block.
	// coinSupply := txOutResult.TotalAmount
	// Output
	blockdata := &apitypes.BlockDataBasic{
		Height:     uint32(blockHeader.Height),
		Size:       uint32(msgBlock.SerializeSize()),
		Hash:       hash.String(),
		Difficulty: blockHeader.Difficulty,
		Time:       apitypes.TimeAPI{S: dbtypes.NewTimeDef(time.Unix(blockHeader.Time, 0))},
	}
	extrainfo := &apitypes.BlockExplorerExtraInfo{
		TxLen:           txLen,
		NextBlockReward: mutilchain.GetNextBlockReward(mutilchain.TYPEBTC, t.netParams.SubsidyReductionInterval, blockHeader.Height),
		BlockReward:     mutilchain.GetCurrentBlockReward(mutilchain.TYPEBTC, t.netParams.SubsidyReductionInterval, blockHeader.Height),
	}
	return blockdata, blockHeader, extrainfo, msgBlock, nil
}

// CollectHash collects chain data at the block with the specified hash.
func (t *Collector) CollectHash(hash *chainhash.Hash) (*BlockData, *wire.MsgBlock, error) {
	// In case of a very fast block, make sure previous call to collect is not
	// still running, or dcrd may be mad.
	t.mtx.Lock()
	defer t.mtx.Unlock()

	// Time this function
	defer func(start time.Time) {
		log.Debugf("Collector.CollectHash() completed in %v", time.Since(start))
	}(time.Now())

	// Info specific to the block hash
	_, blockHeaderVerbose, extra, msgBlock, err := t.CollectBlockInfo(hash)
	if err != nil {
		return nil, nil, err
	}

	// Number of peer connection to chain server
	numConn, err := btcrpcutils.WithTimeout(func() (int64, error) {
		return t.btcdChainSvr.GetConnectionCount()
	})
	if err != nil {
		log.Warn("Unable to get connection count: ", err)
	}

	// Blockchain info (e.g. syncheight, verificationprogress, chainwork,
	// bestblockhash, initialblockdownload, maxblocksize, deployments, etc.).
	chainInfo, err := btcrpcutils.WithTimeout(func() (*btcjson.GetBlockChainInfoResult, error) {
		return t.btcdChainSvr.GetBlockChainInfo()
	})
	if err != nil {
		log.Warn("Unable to get blockchain info: ", err)
	}
	// GetBlockChainInfo is only valid for for chain tip.
	if chainInfo != nil && chainInfo.BestBlockHash != hash.String() {
		chainInfo = nil
	}

	// Output
	blockdata := &BlockData{
		Header:         *blockHeaderVerbose,
		Connections:    int32(numConn),
		ExtraInfo:      *extra,
		BlockchainInfo: chainInfo,
	}

	return blockdata, msgBlock, err
}

// Collect collects chain data at the current best block.
func (t *Collector) Collect() (*BlockData, *wire.MsgBlock, error) {
	// In case of a very fast block, make sure previous call to collect is not
	// still running, or dcrd may be mad.
	t.mtx.Lock()
	defer t.mtx.Unlock()

	// Time this function.
	defer func(start time.Time) {
		log.Debugf("Collector.Collect() completed in %v", time.Since(start))
	}(time.Now())

	// Pull and store relevant data about the blockchain (e.g. syncheight,
	// verificationprogress, chainwork, bestblockhash, initialblockdownload,
	// maxblocksize, deployments, etc.).
	blockchainInfo, err := btcrpcutils.WithTimeout(func() (*btcjson.GetBlockChainInfoResult, error) {
		return t.btcdChainSvr.GetBlockChainInfo()
	})
	if err != nil {
		return nil, nil, fmt.Errorf("unable to get blockchain info: %v", err)
	}

	hash, err := chainhash.NewHashFromStr(blockchainInfo.BestBlockHash)
	if err != nil {
		return nil, nil,
			fmt.Errorf("invalid best block hash from getblockchaininfo: %v", err)
	}
	// Info specific to the block hash
	_, blockHeaderVerbose, extra, msgBlock, err := t.CollectBlockInfo(hash)
	if err != nil {
		return nil, nil, err
	}
	// Number of peer connection to chain server
	numConn, err := btcrpcutils.WithTimeout(func() (int64, error) {
		return t.btcdChainSvr.GetConnectionCount()
	})
	if err != nil {
		log.Warn("Unable to get connection count: ", err)
	}
	// Output
	blockdata := &BlockData{
		Header:         *blockHeaderVerbose,
		Connections:    int32(numConn),
		ExtraInfo:      *extra,
		BlockchainInfo: blockchainInfo,
	}

	return blockdata, msgBlock, err
}
