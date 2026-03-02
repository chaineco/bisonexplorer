package txhelpers

import (
	"errors"
	"time"
)

// DefaultRPCTimeout is the timeout for individual RPC calls.
var DefaultRPCTimeout = 30 * time.Second

// ErrRPCTimeout is returned when an RPC call exceeds DefaultRPCTimeout.
var ErrRPCTimeout = errors.New("RPC call timed out")

// WithTimeout wraps an RPC call with a timeout. If the call does not complete
// within DefaultRPCTimeout, ErrRPCTimeout is returned. This prevents the
// application from freezing when the node becomes unresponsive.
func WithTimeout[T any](fn func() (T, error)) (T, error) {
	type result struct {
		val T
		err error
	}
	ch := make(chan result, 1)
	go func() {
		v, err := fn()
		ch <- result{v, err}
	}()
	timer := time.NewTimer(DefaultRPCTimeout)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r.val, r.err
	case <-timer.C:
		var zero T
		return zero, ErrRPCTimeout
	}
}
