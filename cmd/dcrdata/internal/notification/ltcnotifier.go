package notification

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/decred/dcrdata/v8/mutilchain"
	"github.com/ltcsuite/ltcd/btcjson"
	"github.com/ltcsuite/ltcd/chaincfg/chainhash"
	"github.com/ltcsuite/ltcd/rpcclient"
)

// LtcTxHandler is a function that will be called for new mempool transactions.
type LtcTxHandler func(*btcjson.TxRawResult) error

// LtcBlockHandler is a function that will be called when a new block is detected.
type LtcBlockHandler func(*mutilchain.LtcBlockHeader) error

// LtcBlockHandlerLite is a simpler trigger using only builtin types.
type LtcBlockHandlerLite func(uint32, string) error

// LTCNotifier handles block notifications from a litecoind node via HTTP polling.
type LTCNotifier struct {
	client          *rpcclient.Client
	anyQ            chan interface{}
	tx              [][]LtcTxHandler
	block           [][]LtcBlockHandler
	pollInterval    time.Duration
	lastKnownHeight int64
	previous        struct {
		hash   chainhash.Hash
		height uint32
	}
}

// NewLtcNotifier is the constructor for a LTCNotifier.
func NewLtcNotifier() *LTCNotifier {
	return &LTCNotifier{
		anyQ:         make(chan interface{}, 1024),
		tx:           make([][]LtcTxHandler, 0),
		block:        make([][]LtcBlockHandler, 0),
		pollInterval: 10 * time.Second,
	}
}

// SetClient sets the RPC client for polling.
func (notifier *LTCNotifier) SetClient(client *rpcclient.Client) {
	notifier.client = client
}

// Listen starts polling for new blocks. Must be called after SetClient and
// after all handlers are registered.
func (notifier *LTCNotifier) Listen(ctx context.Context) *ContextualError {
	if notifier.client == nil {
		return newContextualError("client not set", fmt.Errorf("call SetClient first"))
	}

	height, err := notifier.client.GetBlockCount()
	if err != nil {
		return newContextualError("failed to get initial block count", err)
	}
	notifier.lastKnownHeight = height

	log.Infof("LTC: Starting block polling, interval %v, height: %d", notifier.pollInterval, height)

	go notifier.superQueue(ctx)
	go notifier.pollBlocks(ctx)
	return nil
}

func (notifier *LTCNotifier) SetPreviousBlock(prevHash chainhash.Hash, prevHeight uint32) {
	notifier.previous.hash = prevHash
	notifier.previous.height = prevHeight
}

// pollBlocks polls for new blocks periodically.
func (notifier *LTCNotifier) pollBlocks(ctx context.Context) {
	ticker := time.NewTicker(notifier.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Infof("LTC: Block polling stopped")
			return
		case <-ticker.C:
			notifier.checkForNewBlocks()
		}
	}
}

// checkForNewBlocks checks if there are new blocks and queues them.
func (notifier *LTCNotifier) checkForNewBlocks() {
	if notifier.client == nil {
		return
	}

	currentHeight, err := notifier.client.GetBlockCount()
	if err != nil {
		log.Errorf("LTC: Failed to get block count: %v", err)
		return
	}

	lastHeight := notifier.lastKnownHeight

	for height := lastHeight + 1; height <= currentHeight; height++ {
		hash, err := notifier.client.GetBlockHash(height)
		if err != nil {
			log.Errorf("LTC: Failed to get block hash for height %d: %v", height, err)
			return
		}

		blockHeader := &mutilchain.LtcBlockHeader{
			Hash:   *hash,
			Height: int32(height),
			Time:   time.Now(),
		}

		log.Infof("LTC: New block at height %d: %v", height, hash)
		notifier.anyQ <- blockHeader
	}

	if currentHeight > lastHeight {
		notifier.lastKnownHeight = currentHeight
	}
}

// superQueue processes notifications from the queue.
func (notifier *LTCNotifier) superQueue(ctx context.Context) {
out:
	for {
		select {
		case rawMsg := <-notifier.anyQ:
			switch msg := rawMsg.(type) {
			case *mutilchain.LtcBlockHeader:
				log.Infof("LTCSuperQueue: Processing new block %v. Height: %d", msg.Hash, msg.Height)
				notifier.processBlock(msg)
			case *btcjson.TxRawResult:
				notifier.processTx(msg)
			default:
				log.Warn("unknown message type in superQueue LTC: %T", rawMsg)
			}
		case <-ctx.Done():
			break out
		}
	}
}

// RegisterTxHandlerGroup adds a group of tx handlers.
func (notifier *LTCNotifier) RegisterTxHandlerGroup(handlers ...LtcTxHandler) {
	notifier.tx = append(notifier.tx, handlers)
}

// RegisterBlockHandlerGroup adds a group of block handlers.
func (notifier *LTCNotifier) RegisterBlockHandlerGroup(handlers ...LtcBlockHandler) {
	notifier.block = append(notifier.block, handlers)
}

// RegisterBlockHandlerLiteGroup adds a group of block handlers using builtin types.
func (notifier *LTCNotifier) RegisterBlockHandlerLiteGroup(handlers ...LtcBlockHandlerLite) {
	translations := make([]LtcBlockHandler, 0, len(handlers))
	notifier.RegisterBlockHandlerGroup(translations...)
}

// processBlock calls the BlockHandler groups one at a time in the order
// that they were registered.
func (notifier *LTCNotifier) processBlock(bh *mutilchain.LtcBlockHeader) {
	start := time.Now()
	for _, handlers := range notifier.block {
		wg := new(sync.WaitGroup)
		for _, h := range handlers {
			wg.Add(1)
			go func(h LtcBlockHandler) {
				tStart := time.Now()
				defer wg.Done()
				defer log.Tracef("Notifier: BlockHandler %s completed in %v",
					functionName(h), time.Since(tStart))
				if err := h(bh); err != nil {
					log.Errorf("block handler failed: %v", err)
					return
				}
			}(h)
		}
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.NewTimer(SyncHandlerDeadline).C:
			log.Errorf("at least 1 block handler has not completed before the deadline")
			return
		}
	}
	log.Debugf("handlers of Notifier.processBlock() completed in %v", time.Since(start))
}

// processTx calls the TxHandler groups one at a time in the order that they
// were registered.
func (notifier *LTCNotifier) processTx(tx *btcjson.TxRawResult) {
	start := time.Now()
	for i, handlers := range notifier.tx {
		wg := new(sync.WaitGroup)
		for j, h := range handlers {
			wg.Add(1)
			go func(h LtcTxHandler, i, j int) {
				defer wg.Done()
				defer log.Tracef("Notifier: TxHandler %d.%d completed", i, j)
				if err := h(tx); err != nil {
					log.Errorf("tx handler failed: %v", err)
					return
				}
			}(h, i, j)
		}
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.NewTimer(SyncHandlerDeadline).C:
			log.Errorf("at least 1 tx handler has not completed before the deadline")
			return
		}
	}
	log.Tracef("handlers of Notifier.onTxAcceptedVerbose() completed in %v", time.Since(start))
}
