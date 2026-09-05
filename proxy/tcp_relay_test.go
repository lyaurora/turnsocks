package proxy

import (
	"context"
	"io"
	"net"
	"sync"
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
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			var allocations atomic.Int32
			var workers sync.WaitGroup
			workers.Add(1)
			go func() {
				defer workers.Done()
				for {
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					workers.Add(1)
					go func() {
						defer workers.Done()
						defer conn.Close()
						_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
						for {
							req, err := readSTUNMessage(conn)
							if err != nil {
								return
							}
							res := stun.New()
							res.Type = stun.MessageType{Method: req.Type.Method, Class: stun.ClassSuccessResponse}
							res.TransactionID = req.TransactionID
							disconnect := false
							switch req.Type.Method {
							case MethodAllocate:
								disconnect = allocations.Add(1) <= tc.disconnects
							case MethodConnect:
								if tc.timeout {
									_, _ = io.Copy(io.Discard, conn)
									return
								}
								if tc.connectCode != 0 {
									res.Type.Class = stun.ClassErrorResponse
									_ = (stun.ErrorCodeAttribute{Code: tc.connectCode, Reason: []byte("test refusal")}).AddTo(res)
								} else {
									res.Add(AttrConnectionID, []byte{0, 0, 0, 1})
								}
							case MethodConnectionBind:
							default:
								t.Errorf("unexpected TURN method: %v", req.Type.Method)
								return
							}
							if err := writeSTUNMessage(conn, res); err != nil || disconnect {
								return
							}
							if req.Type.Method == MethodConnectionBind {
								_, _ = io.Copy(conn, conn)
								return
							}
						}
					}()
				}
			}()
			t.Cleanup(func() {
				_ = listener.Close()
				workers.Wait()
			})
			turn := turnServerConfig{Addr: listener.Addr().String()}
			cfg := Config{
				Timeout:   time.Second,
				TurnPool:  newTurnPool([]turnServerConfig{turn}, time.Minute, ""),
				TCPAllocs: newTCPAllocationPool(),
			}
			t.Cleanup(func() {
				for _, allocation := range cfg.TCPAllocs.allocs[turn.String()] {
					allocation.close()
				}
			})
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
