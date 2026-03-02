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

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrdata/v8/mutilchain"
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
	// CollectHash fetches the block, header, connection count, and blockchain
	// info in one pass — no need to call GetBlock/GetBlockHeaderVerbose
	// separately here, as that would duplicate the RPCs inside CollectBlockInfo.
	blockData, msgBlock, err := p.collector.CollectHash(hash)
	if err != nil {
		return nil, nil, fmt.Errorf("blockdata.CollectHash(hash) failed: %v", err.Error())
	}
	log.Infof("Block height %v connected. Collecting data...", blockData.Header.Height)
	return msgBlock, blockData, nil
}

// ConnectBlock is a synchronous version of BlockConnectedHandler that collects
// and stores data for a block. ConnectBlock satisfies
// notification.BlockHandler, and is registered as a handler in main.go.
func (p *chainMonitor) ConnectBlock(header *mutilchain.BtcBlockHeader) error {
	hash := header.Hash

	// Collect block data outside the lock — this is read-only RPC fetching
	// and does not need reorg protection.
	msgBlock, blockData, err := p.collect(&hash)
	if err != nil {
		return err
	}

	// Only hold the lock during the store phase to prevent simultaneous
	// reorg and block connect operations.
	p.reorgLock.Lock()
	defer p.reorgLock.Unlock()

	// Store block data with each saver, with a per-saver timeout.
	for _, s := range p.dataSavers {
		if s != nil {
			tStart := time.Now()
			if err0 := p.runSaverWithTimeout(func() error {
				return s.BTCStore(blockData, msgBlock)
			}); err0 != nil {
				log.Errorf("(%v).Store failed: %v", reflect.TypeOf(s), err0)
				err = err0
			}
			log.Tracef("(*chainMonitor).ConnectBlock: Completed %s.Store in %v.",
				reflect.TypeOf(s), time.Since(tStart))
		}
	}
	return err
}

// saverTimeout is the maximum time allowed for a single saver to complete.
const saverTimeout = 10 * time.Minute

// runSaverWithTimeout runs a saver function with a timeout. If the saver does
// not complete within saverTimeout, an error is returned. The context is also
// checked for cancellation.
func (p *chainMonitor) runSaverWithTimeout(fn func() error) error {
	done := make(chan error, 1)
	go func() {
		done <- fn()
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(saverTimeout):
		return fmt.Errorf("BTC saver timed out after %v", saverTimeout)
	case <-p.ctx.Done():
		return p.ctx.Err()
	}
}
