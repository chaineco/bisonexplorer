// Copyright (c) 2018-2021, The Decred developers
// Copyright (c) 2017, Jonathan Chappelow
// See LICENSE for details.

package blockdatabtc

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrdata/v8/mutilchain"
	"github.com/decred/dcrdata/v8/mutilchain/btcrpcutils"
)

// for getblock, ticketfeeinfo, estimatestakediff, etc.
type chainMonitor struct {
	ctx             context.Context
	collector       *Collector
	dataSavers      []BlockDataSaver
	reorgDataSavers []BlockDataSaver
	reorgLock       sync.Mutex
}

// NewChainMonitor creates a new chainMonitor.
func NewChainMonitor(ctx context.Context, collector *Collector, savers []BlockDataSaver,
	reorgSavers []BlockDataSaver) *chainMonitor {

	return &chainMonitor{
		ctx:             ctx,
		collector:       collector,
		dataSavers:      savers,
		reorgDataSavers: reorgSavers,
	}
}

func (p *chainMonitor) collect(hash *chainhash.Hash) (*wire.MsgBlock, *BlockData, error) {
	// getblock RPC
	msgBlock, err := btcrpcutils.WithTimeout(func() (*wire.MsgBlock, error) {
		return p.collector.btcdChainSvr.GetBlock(hash)
	})
	blockHeader, blockHeaderErr := btcrpcutils.WithTimeout(func() (*btcjson.GetBlockHeaderVerboseResult, error) {
		return p.collector.btcdChainSvr.GetBlockHeaderVerbose(hash)
	})
	if err != nil || blockHeaderErr != nil {
		return nil, nil, fmt.Errorf("failed to get block %v", hash)
	}
	height := int64(blockHeader.Height)
	log.Infof("Block height %v connected. Collecting data...", height)

	// Get node's best block height to see if the block for which we are
	// collecting data is the best block.
	chainHeight, err := btcrpcutils.WithTimeout(func() (int64, error) {
		return p.collector.btcdChainSvr.GetBlockCount()
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get chain height: %v", err)
	}

	// If new block height not equal to chain height, then we are behind
	// on data collection, so specify the hash of the notified, skipping
	// stake diff estimates and other stuff for web ui that is only
	// relevant for the best block.
	var blockData *BlockData
	if chainHeight != height {
		log.Debugf("Collecting data for block %v (%d), behind tip %d.",
			hash, height, chainHeight)
		blockData, _, err = p.collector.CollectHash(hash)
		if err != nil {
			return nil, nil, fmt.Errorf("blockdata.CollectHash(hash) failed: %v", err.Error())
		}
	} else {
		blockData, _, err = p.collector.Collect()
		if err != nil {
			return nil, nil, fmt.Errorf("blockdata.Collect() failed: %v", err.Error())
		}
	}

	return msgBlock, blockData, nil
}

// ConnectBlock is a synchronous version of BlockConnectedHandler that collects
// and stores data for a block. ConnectBlock satisfies
// notification.BlockHandler, and is registered as a handler in main.go.
func (p *chainMonitor) ConnectBlock(header *mutilchain.BtcBlockHeader) error {
	// Do not handle reorg and block connects simultaneously.
	hash := header.Hash
	p.reorgLock.Lock()
	defer p.reorgLock.Unlock()
	// Collect block data.
	msgBlock, blockData, err := p.collect(&hash)
	if err != nil {
		return err
	}
	// Store block data with each saver.
	for _, s := range p.dataSavers {
		if s != nil {
			tStart := time.Now()
			// Save data to wherever the saver wants to put it.
			if err0 := s.BTCStore(blockData, msgBlock); err0 != nil {
				log.Errorf("(%v).Store failed: %v", reflect.TypeOf(s), err0)
				err = err0
			}
			log.Tracef("(*chainMonitor).ConnectBlock: Completed %s.Store in %v.",
				reflect.TypeOf(s), time.Since(tStart))
		}
	}
	return err
}
