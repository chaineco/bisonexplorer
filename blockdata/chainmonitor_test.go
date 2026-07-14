package blockdata

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestIsTransientRPCError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		// Raw context errors (errors.Is path).
		{"context.Canceled", context.Canceled, true},
		{"context.DeadlineExceeded", context.DeadlineExceeded, true},
		// The exact error observed in production before the panic, wrapped the
		// same way collectOnce wraps it (%v loses the error chain, so these
		// exercise the string-matching path).
		{"canceled by caller wrapped",
			fmt.Errorf("blockdata.CollectHash(hash) failed: %v",
				errors.New("request was canceled by the caller")), true},
		{"deadline exceeded wrapped",
			fmt.Errorf("failed to get chain height: %v", context.DeadlineExceeded), true},
		{"context canceled wrapped",
			fmt.Errorf("failed to get block abc: %v", context.Canceled), true},
		// Common transport-level failures.
		{"connection reset",
			errors.New("read tcp 127.0.0.1:53>127.0.0.1:9109: connection reset by peer"), true},
		{"connection refused",
			errors.New("dial tcp 127.0.0.1:9109: connect: connection refused"), true},
		{"broken pipe", errors.New("write: broken pipe"), true},
		{"websocket closed", errors.New("websocket: close 1006 (abnormal closure)"), true},
		{"client disconnected", errors.New("the client has been disconnected"), true},
		{"unexpected EOF", errors.New("unexpected EOF"), true},
		{"io timeout", errors.New("read tcp: i/o timeout"), true},
		{"closed network connection",
			errors.New("use of closed network connection"), true},
		// Permanent errors must fail fast (no retry).
		{"block not found",
			errors.New("failed to get block 000000...: -5: block not found"), false},
		{"invalid parameter", errors.New("invalid hash"), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTransientRPCError(tc.err); got != tc.want {
				t.Errorf("isTransientRPCError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
