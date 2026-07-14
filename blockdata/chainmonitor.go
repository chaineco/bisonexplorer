// Copyright (c) 2018-2021, The Decred developers
// Copyright (c) 2017, Jonathan Chappelow
// See LICENSE for details.

package blockdata

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/wire"

	"github.com/decred/dcrdata/v8/txhelpers"
)

const (
	// collectMaxRetries is how many extra times block collection is retried
	// when it fails with a transient RPC/connection error, giving the dcrd RPC
	// client time to auto-reconnect its websocket (DisableAutoReconnect is
	// false) so a brief node disconnect does not cause a block to be skipped.
	collectMaxRetries = 5
	// collectRetryBase is the base backoff between collection retries; the wait
	// grows linearly with the attempt number.
	collectRetryBase = 3 * time.Second
)

// isTransientRPCError reports whether err is the kind of temporary
// connection/cancellation failure that is expected to clear once the dcrd RPC
// websocket reconnects, and is therefore worth retrying. Permanent errors
// (e.g. a block that genuinely does not exist) return false so we fail fast.
func isTransientRPCError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	// Note: errors from collectOnce are wrapped with %v (not %w), so the
	// errors.Is checks above only catch unwrapped errors; the string list below
	// must therefore also include the context error messages.
	msg := strings.ToLower(err.Error())
	for _, s := range []string{
		"request was canceled",
		"context canceled",
		"deadline exceeded",
		"connection reset",
		"connection refused",
		"broken pipe",
		"websocket",
		"disconnected",
		"eof",
		"i/o timeout",
		"no route to host",
		"use of closed network connection",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

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

// collect gathers block data, retrying on transient RPC/connection failures so
// that a temporary dcrd disconnect (which the RPC client auto-reconnects from)
// does not cause the block to be skipped and its data lost.
func (p *chainMonitor) collect(hash *chainhash.Hash) (*wire.MsgBlock, *BlockData, error) {
	var lastErr error
	for attempt := 0; attempt <= collectMaxRetries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(attempt) * collectRetryBase
			log.Warnf("blockdata collect for %v failed (attempt %d/%d): %v; "+
				"waiting %v for RPC to reconnect, then retrying",
				hash, attempt, collectMaxRetries, lastErr, delay)
			select {
			case <-p.ctx.Done():
				return nil, nil, p.ctx.Err()
			case <-time.After(delay):
			}
		}

		msgBlock, blockData, err := p.collectOnce(hash)
		if err == nil {
			if attempt > 0 {
				log.Infof("blockdata collect for %v succeeded after %d retr%s",
					hash, attempt, map[bool]string{true: "y", false: "ies"}[attempt == 1])
			}
			return msgBlock, blockData, nil
		}

		lastErr = err
		// Only retry errors that are expected to clear on reconnect. Fail fast
		// on anything else (e.g. a block that genuinely cannot be found).
		if !isTransientRPCError(err) {
			return nil, nil, err
		}
	}
	return nil, nil, lastErr
}

func (p *chainMonitor) collectOnce(hash *chainhash.Hash) (*wire.MsgBlock, *BlockData, error) {
	// getblock RPC
	ctx, cancel := context.WithTimeout(context.TODO(), 2*time.Minute)
	defer cancel()
	msgBlock, err := p.collector.dcrdChainSvr.GetBlock(ctx, hash)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get block %v: %v", hash, err)
	}
	height := int64(msgBlock.Header.Height)
	log.Infof("Block height %v connected. Collecting data...", height)

	// Get node's best block height to see if the block for which we are
	// collecting data is the best block.
	chainHeight, err := p.collector.dcrdChainSvr.GetBlockCount(ctx)
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
func (p *chainMonitor) ConnectBlock(header *wire.BlockHeader) error {
	// Do not handle reorg and block connects simultaneously.
	hash := header.BlockHash()
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
			if err0 := s.Store(blockData, msgBlock); err0 != nil {
				log.Errorf("(%v).Store failed: %v", reflect.TypeOf(s), err0)
				err = err0
			}
			log.Tracef("(*chainMonitor).ConnectBlock: Completed %s.Store in %v.",
				reflect.TypeOf(s), time.Since(tStart))
		}
	}
	return err
}

// ReorgHandler processes a chain reorg. A reorg is handled in blockdata by
// simply collecting data for the new best block, and storing it in the
// *reorgDataSavers*.
func (p *chainMonitor) ReorgHandler(reorg *txhelpers.ReorgData) error {
	if reorg == nil {
		return fmt.Errorf("nil reorg data received")
	}

	newHeight := reorg.NewChainHeight
	newHash := reorg.NewChainHead

	// Do not handle reorg and block connects simultaneously.
	p.reorgLock.Lock()
	defer p.reorgLock.Unlock()
	log.Infof("Reorganize signaled to blockdata. "+
		"Collecting data for NEW head block %v at height %d.",
		newHash, newHeight)

	// Collect data for the new best block.
	msgBlock, blockData, err := p.collect(&newHash)
	if err != nil {
		reorg.WG.Done()
		return fmt.Errorf("ReorgHandler: Failed to collect data for block %v: %v", newHash, err)
	}

	// Store block data with each REORG saver.
	for _, s := range p.reorgDataSavers {
		if s != nil {
			// Save data to wherever the saver wants to put it.
			if err := s.Store(blockData, msgBlock); err != nil {
				return fmt.Errorf("(%v).Store failed: %v", reflect.TypeOf(s), err)
			}
		}
	}
	return nil
}
