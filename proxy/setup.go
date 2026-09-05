package proxy

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"time"
)

// A setup belongs to one SOCKS request, never to a pooled TURN allocation.
type setupContext struct {
	context.Context
	cancel context.CancelFunc
	stop   func() bool
	stage  atomic.Value
}

func startSetup(conn net.Conn, timeout time.Duration) *setupContext {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	s := &setupContext{Context: ctx, cancel: cancel}
	s.stage.Store("SOCKS 握手")
	s.stop = context.AfterFunc(ctx, func() { _ = conn.Close() })
	return s
}

func (s *setupContext) finish(conn net.Conn) error {
	s.stop()
	if err := s.err(); err != nil {
		return err
	}
	s.cancel()
	return conn.SetDeadline(time.Time{})
}

func (s *setupContext) err() error {
	if err := contextError(s.Context); err != nil {
		if stage, ok := s.stage.Load().(string); ok {
			return fmt.Errorf("%s：%w", stage, err)
		}
		return err
	}
	return nil
}

func contextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// A socket deadline may fire just before the context's timer goroutine.
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

func setupDeadline(ctx context.Context, timeout time.Duration) time.Time {
	deadline := time.Now().Add(timeout)
	if parent, ok := ctx.Deadline(); ok && parent.Before(deadline) {
		return parent
	}
	return deadline
}
