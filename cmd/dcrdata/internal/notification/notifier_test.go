// Copyright (c) 2019-2021, The Decred developers

package notification

import (
	"context"
	"sync"
	"testing"

	"github.com/decred/dcrd/chaincfg/chainhash"
	chainjson "github.com/decred/dcrd/rpc/jsonrpc/types/v4"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrdata/v8/txhelpers"
)

type dummyNode struct{}

func (node *dummyNode) NotifyBlocks(context.Context) error                { return nil }
func (node *dummyNode) NotifyNewTransactions(context.Context, bool) error { return nil }
func (node *dummyNode) NotifyWinningTickets(context.Context) error        { return nil }

var counter int64
var hashTails = []string{"00", "01", "02", "03", "04", "05", "06", "07", "08", "09"}

func newHash() *chainhash.Hash {
	counter++
	h, _ := chainhash.NewHash([]byte("000000000000000000000000000000" + hashTails[int(counter)%len(hashTails)]))
	return h
}

func (node *dummyNode) GetBestBlock(context.Context) (*chainhash.Hash, int64, error) {
	hash := newHash()
	return hash, counter, nil
}

var commonAncestorHash = newHash()
var commonAncestor = &wire.MsgBlock{
	Header: wire.BlockHeader{
		PrevBlock: *commonAncestorHash,
		Height:    uint32(5),
	},
}

// GetBlock will only be called by rpcutils.CommonAncestor, so it should return
// the same block every time.
func (node *dummyNode) GetBlock(_ context.Context, blockHash *chainhash.Hash) (*wire.MsgBlock, error) {
	return commonAncestor, nil
}
func (node *dummyNode) GetBlockHash(_ context.Context, blockHeight int64) (*chainhash.Hash, error) {
	hash := newHash()
	return hash, nil
}
func (node *dummyNode) GetBlockHeaderVerbose(_ context.Context, hash *chainhash.Hash) (*chainjson.GetBlockHeaderVerboseResult, error) {
	return nil, nil
}

var callCounter int

// testTxHandler will be tested async
var mtx sync.RWMutex
var wg = new(sync.WaitGroup)
var notifier *Notifier

func testTxHandler(_ *chainjson.TxRawResult) error {
	mtx.Lock()
	defer mtx.Unlock()
	defer wg.Done()
	callCounter++
	return nil
}

var testTxHandler2 = testTxHandler

func testBlockHandler(_ *wire.BlockHeader) error {
	defer wg.Done()
	callCounter++
	return nil
}
func testBlockHandlerLite(_ uint32, _ string) error {
	defer wg.Done()
	callCounter++
	return nil
}
func testReorgHandler(reorg *txhelpers.ReorgData) error {
	defer wg.Done()
	callCounter++
	notifier.SetPreviousBlock(reorg.NewChainHead, uint32(reorg.NewChainHeight))
	return nil
}

func TestNotifier(t *testing.T) {

	notifier = NewNotifier()
	signals := notifier.DcrdHandlers()
	notifier.RegisterTxHandlerGroup(testTxHandler, testTxHandler2)
	notifier.RegisterBlockHandlerGroup(testBlockHandler)
	notifier.RegisterBlockHandlerLiteGroup(testBlockHandlerLite)
	notifier.RegisterReorgHandlerGroup(testReorgHandler)
	wg.Add(5)

	ctx, shutdown := context.WithCancel(context.Background())
	defer shutdown()

	notifier.Listen(ctx, &dummyNode{})

	prevBlock := newHash()
	header := wire.BlockHeader{
		PrevBlock: *prevBlock,
		Height:    uint32(counter),
	}
	notifier.previous.hash = *prevBlock
	bytes, _ := header.Bytes()
	signals.OnBlockConnected(bytes, nil)

	oldHash := newHash()
	ohdHeight := int32(counter)
	newHash := newHash()
	newHeight := counter
	signals.OnReorganization(oldHash, ohdHeight, newHash, int32(newHeight))

	signals.OnTxAcceptedVerbose(new(chainjson.TxRawResult))

	wg.Wait()

	if notifier.previous.hash.String() != newHash.String() {
		t.Errorf("unexpected previous.hash after reorg. %s != %s",
			notifier.previous.hash.String(), newHash.String())
	}

	if notifier.previous.height != uint32(newHeight) {
		t.Errorf("unexpected previous.height after reorg. %d != %d",
			notifier.previous.height, uint32(newHeight))
	}

	if callCounter != 5 {
		t.Errorf("callCounter = %d. Should be 5.", callCounter)
	}

	shutdown()
}

// chainNode implements DCRDNode over a fixed main chain of linked headers, for
// exercising the missed-block catch-up in processBlock.
type chainNode struct {
	headers []*wire.BlockHeader // index = height
}

func (m *chainNode) GetBestBlock(context.Context) (*chainhash.Hash, int64, error) {
	tip := m.headers[len(m.headers)-1]
	h := tip.BlockHash()
	return &h, int64(tip.Height), nil
}

func (m *chainNode) GetBlock(_ context.Context, blockHash *chainhash.Hash) (*wire.MsgBlock, error) {
	for _, hdr := range m.headers {
		if hdr.BlockHash() == *blockHash {
			return &wire.MsgBlock{Header: *hdr}, nil
		}
	}
	return nil, context.DeadlineExceeded
}

func (m *chainNode) GetBlockHash(_ context.Context, blockHeight int64) (*chainhash.Hash, error) {
	if blockHeight < 0 || blockHeight >= int64(len(m.headers)) {
		return nil, context.DeadlineExceeded
	}
	h := m.headers[blockHeight].BlockHash()
	return &h, nil
}

func (m *chainNode) GetBlockHeaderVerbose(context.Context, *chainhash.Hash) (*chainjson.GetBlockHeaderVerboseResult, error) {
	return nil, context.DeadlineExceeded
}

func (m *chainNode) NotifyBlocks(context.Context) error                  { return nil }
func (m *chainNode) NotifyNewTransactions(context.Context, bool) error   { return nil }

// buildChain creates n linked headers with heights 0..n-1.
func buildChain(n int) []*wire.BlockHeader {
	headers := make([]*wire.BlockHeader, n)
	var prevHash chainhash.Hash
	for i := 0; i < n; i++ {
		hdr := &wire.BlockHeader{
			Height:    uint32(i),
			PrevBlock: prevHash,
		}
		headers[i] = hdr
		prevHash = hdr.BlockHash()
	}
	return headers
}

func TestProcessBlockCatchUp(t *testing.T) {
	headers := buildChain(10)
	n := NewNotifier()
	n.node = &chainNode{headers: headers}

	var handledMtx sync.Mutex
	var handled []uint32
	n.RegisterBlockHandlerGroup(func(bh *wire.BlockHeader) error {
		handledMtx.Lock()
		handled = append(handled, bh.Height)
		handledMtx.Unlock()
		return nil
	})

	// Last processed block is height 4; blocks 5-7 were missed; block 8
	// arrives. All of 5, 6, 7, 8 must be handled in order.
	n.SetPreviousBlock(headers[4].BlockHash(), 4)
	n.processBlock(headers[8])

	want := []uint32{5, 6, 7, 8}
	handledMtx.Lock()
	defer handledMtx.Unlock()
	if len(handled) != len(want) {
		t.Fatalf("handled heights = %v, want %v", handled, want)
	}
	for i := range want {
		if handled[i] != want[i] {
			t.Fatalf("handled heights = %v, want %v", handled, want)
		}
	}
	if n.previous.height != 8 || n.previous.hash != headers[8].BlockHash() {
		t.Fatalf("previous = %d/%v, want 8/%v", n.previous.height,
			n.previous.hash, headers[8].BlockHash())
	}
}

func TestProcessBlockReorgNoCatchUp(t *testing.T) {
	headers := buildChain(10)
	n := NewNotifier()
	n.node = &chainNode{headers: headers}

	var handled int
	n.RegisterBlockHandlerGroup(func(*wire.BlockHeader) error {
		handled++
		return nil
	})

	// The last processed block at height 4 is NOT on the main chain
	// (different hash) — a reorg is in progress, so there must be no
	// catch-up and no processing until the reorg notification arrives.
	var sideHash chainhash.Hash
	sideHash[0] = 0xde
	n.SetPreviousBlock(sideHash, 4)
	n.processBlock(headers[8])

	if handled != 0 {
		t.Fatalf("handlers ran %d times, want 0 (should wait for reorg ntfn)", handled)
	}
	if n.previous.hash != sideHash {
		t.Fatal("previous block should be unchanged while waiting for reorg")
	}
}

func TestProcessBlockHandlerErrorNoHole(t *testing.T) {
	headers := buildChain(10)
	n := NewNotifier()
	n.node = &chainNode{headers: headers}

	// The handler fails the first time it sees height 8, then succeeds.
	var failedOnce bool
	var handled []uint32
	n.RegisterBlockHandlerGroup(func(bh *wire.BlockHeader) error {
		if bh.Height == 8 && !failedOnce {
			failedOnce = true
			return context.DeadlineExceeded
		}
		handled = append(handled, bh.Height)
		return nil
	})

	n.SetPreviousBlock(headers[7].BlockHash(), 7)

	// First attempt fails: block 8 must NOT be recorded as processed.
	n.processBlock(headers[8])
	if n.previous.height != 7 {
		t.Fatalf("previous height = %d after failed handler, want 7 (no hole)",
			n.previous.height)
	}

	// Next block arrives: catch-up must replay 8, then process 9.
	n.processBlock(headers[9])
	want := []uint32{8, 9}
	if len(handled) != len(want) || handled[0] != want[0] || handled[1] != want[1] {
		t.Fatalf("handled heights = %v, want %v", handled, want)
	}
	if n.previous.height != 9 {
		t.Fatalf("previous height = %d, want 9", n.previous.height)
	}
}

func TestProcessBlockNormalConnect(t *testing.T) {
	headers := buildChain(10)
	n := NewNotifier()
	n.node = &chainNode{headers: headers}

	var handled int
	n.RegisterBlockHandlerGroup(func(*wire.BlockHeader) error {
		handled++
		return nil
	})

	n.SetPreviousBlock(headers[7].BlockHash(), 7)
	n.processBlock(headers[8])

	if handled != 1 {
		t.Fatalf("handlers ran %d times, want 1", handled)
	}
	if n.previous.height != 8 {
		t.Fatalf("previous height = %d, want 8", n.previous.height)
	}
}
