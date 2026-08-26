// Copyright (c) 2026, The Decred developers
// See LICENSE for details.

package dcrpg

import "sync"

// rawTxCache memoizes previous transactions fetched from a chain daemon, keyed
// by txid. Only confirmed transactions are stored, and the consumers read
// nothing from them but the immutable output values, so entries never need
// invalidating.
//
// Eviction is deliberately crude: once the map exceeds limit, entries are
// dropped in Go's randomized map order until it is back down to evictTo. That
// approximates random replacement without the bookkeeping of a real LRU, which
// is the right trade here — the goal is a hard memory ceiling, not an optimal
// hit rate.
type rawTxCache[T any] struct {
	mtx     sync.Mutex
	limit   int
	evictTo int
	txs     map[string]*T
}

func newRawTxCache[T any](limit int) *rawTxCache[T] {
	return &rawTxCache[T]{
		limit:   limit,
		evictTo: limit * 3 / 4,
		txs:     make(map[string]*T),
	}
}

func (c *rawTxCache[T]) get(txid string) *T {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	return c.txs[txid]
}

func (c *rawTxCache[T]) put(txid string, tx *T) {
	if tx == nil {
		return
	}
	c.mtx.Lock()
	defer c.mtx.Unlock()
	if len(c.txs) >= c.limit {
		for k := range c.txs {
			delete(c.txs, k)
			if len(c.txs) <= c.evictTo {
				break
			}
		}
	}
	c.txs[txid] = tx
}
