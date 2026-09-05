package proxy

import (
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/stun/v3"
)

func TestDialTurnTCPRecoversStaleAllocation(t *testing.T) {
	for _, tc := range []struct {
		name        string
		prewarm     bool
		disconnects int32
		connectCode stun.ErrorCode
		timeout     bool
		wantAllocs  int32
		wantOK      bool
		wantCooling bool
	}{
		{name: "stale control", prewarm: true, disconnects: 1, wantAllocs: 2, wantOK: true},
		{name: "replacement also fails", prewarm: true, disconnects: 2, wantAllocs: 2, wantCooling: true},
		{name: "fresh control fails", disconnects: 1, wantAllocs: 1, wantCooling: true},
		{name: "peer refused", prewarm: true, connectCode: 403, wantAllocs: 1},
		{name: "auth rejected", prewarm: true, connectCode: 401, wantAllocs: 1, wantCooling: true},
		{name: "peer timeout", prewarm: true, timeout: true, wantAllocs: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var allocations atomic.Int32
			turn := serveSetupTURN(t, func(conn net.Conn, req, res *stun.Message) bool {
				switch req.Type.Method {
				case MethodAllocate:
					return allocations.Add(1) <= tc.disconnects
				case MethodConnect:
					if tc.timeout {
						_, _ = io.Copy(io.Discard, conn)
						return true
					}
					if tc.connectCode != 0 {
						res.Type.Class = stun.ClassErrorResponse
						_ = (stun.ErrorCodeAttribute{Code: tc.connectCode, Reason: []byte("test refusal")}).AddTo(res)
					}
				case MethodConnectionBind:
				default:
					t.Errorf("unexpected TURN method: %v", req.Type.Method)
					return true
				}
				return false
			})
			cfg := setupTestConfig(t, turn)
			if tc.prewarm {
				if err := cfg.TCPAllocs.addIdle(cfg, turn); err != nil {
					t.Fatal(err)
				}
			}

			conn, release, _, err := dialTurnTCP(&setupContext{Context: context.Background()}, cfg, net.IPv4(192, 0, 2, 1), 443)
			if err == nil {
				defer release()
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(time.Second))
				if _, err := conn.Write([]byte("hello")); err != nil {
					t.Fatal(err)
				}
				payload := make([]byte, 5)
				if _, err := io.ReadFull(conn, payload); err != nil || string(payload) != "hello" {
					t.Fatalf("replacement cannot relay data: %q, %v", payload, err)
				}
			}
			if (err == nil) != tc.wantOK {
				t.Errorf("dial error = %v, want success %v", err, tc.wantOK)
			}
			if got := allocations.Load(); got != tc.wantAllocs {
				t.Errorf("created %d allocations, want %d", got, tc.wantAllocs)
			}
			if got := !cfg.TurnPool.servers[0].FailedUntil.IsZero(); got != tc.wantCooling {
				t.Errorf("node cooling = %v, want %v", got, tc.wantCooling)
			}
		})
	}
}
