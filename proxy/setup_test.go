package proxy

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/stun/v3"
)

// A loopback TURN peer exercises allocation, connection binding, and data relay.
func serveSetupTURN(t *testing.T, respond func(net.Conn, *stun.Message, *stun.Message) bool) turnServerConfig {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	var mu sync.Mutex
	conns := make(map[net.Conn]struct{})
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns[conn] = struct{}{}
			mu.Unlock()
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
				for {
					req, err := readSTUNMessage(conn)
					if err != nil {
						return
					}
					res := stun.New()
					res.Type = stun.MessageType{Method: req.Type.Method, Class: stun.ClassSuccessResponse}
					res.TransactionID = req.TransactionID
					disconnect := respond != nil && respond(conn, req, res)
					if req.Type.Method == MethodConnect && res.Type.Class == stun.ClassSuccessResponse {
						res.Add(AttrConnectionID, []byte{0, 0, 0, 1})
					}
					if writeSTUNMessage(conn, res) != nil || disconnect {
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
		_ = ln.Close()
		mu.Lock()
		for conn := range conns {
			_ = conn.Close()
		}
		mu.Unlock()
		workers.Wait()
	})
	return turnServerConfig{Addr: ln.Addr().String()}
}

func setupTestConfig(t *testing.T, turns ...turnServerConfig) Config {
	t.Helper()
	pool := newTCPAllocationPool()
	t.Cleanup(func() {
		pool.mu.Lock()
		defer pool.mu.Unlock()
		for _, allocs := range pool.allocs {
			for _, allocation := range allocs {
				allocation.close()
			}
		}
	})
	return Config{Timeout: time.Second, TCPAllocs: pool, TurnPool: newTurnPool(turns, time.Minute, filepath.Join(t.TempDir(), "state"))}
}

func setupSOCKSRequest(t *testing.T, cfg Config, command byte, host string, pause time.Duration) (net.Conn, <-chan struct{}, error) {
	t.Helper()
	client, server := net.Pipe()
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	done := make(chan struct{})
	go func() {
		defer close(done)
		handleSocksConn(server, cfg)
	}()
	t.Cleanup(func() {
		_ = client.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("SOCKS handler did not finish")
		}
	})
	if err := writeAll(client, []byte{5, 1, 0}); err != nil {
		return client, done, err
	}
	var method [2]byte
	if _, err := io.ReadFull(client, method[:]); err != nil {
		return client, done, err
	}
	time.Sleep(pause)
	req := []byte{5, command, 0, 3, byte(len(host))}
	req = append(req, host...)
	req = append(req, 0x01, 0xbb)
	if err := writeAll(client, req); err != nil {
		return client, done, err
	}
	var reply [10]byte
	_, err := io.ReadFull(client, reply[:])
	if err == nil && reply[1] != 0 {
		err = fmt.Errorf("SOCKS reply %d", reply[1])
	}
	return client, done, err
}

func writeSetupDNSResponse(w http.ResponseWriter, query []byte) {
	response := append([]byte(nil), query...)
	binary.BigEndian.PutUint16(response[2:4], 0x8180)
	binary.BigEndian.PutUint16(response[6:8], 1)
	response = append(response, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 192, 0, 2, 1)
	w.Header().Set("Content-Type", "application/dns-message")
	_, _ = w.Write(response)
}

func TestSOCKSSetupSharesDeadlineAcrossStages(t *testing.T) {
	for _, tc := range []struct {
		name, host string
		pause, dns time.Duration
	}{
		{name: "handshake and TURN", host: "192.0.2.1", pause: 80 * time.Millisecond},
		{name: "DNS and TURN", host: "setup-budget.example", dns: 80 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			turn := serveSetupTURN(t, func(_ net.Conn, req, _ *stun.Message) bool {
				if req.Type.Method == MethodAllocate || req.Type.Method == MethodConnect {
					time.Sleep(80 * time.Millisecond)
				}
				return false
			})
			cfg := setupTestConfig(t, turn)
			cfg.Timeout = 200 * time.Millisecond
			if tc.dns != 0 {
				dnsCache.Delete(tc.host)
				defer dnsCache.Delete(tc.host)
				dns := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					query, _ := io.ReadAll(r.Body)
					time.Sleep(tc.dns)
					writeSetupDNSResponse(w, query)
				}))
				defer dns.Close()
				cfg.DoH, cfg.DoHClient = dns.URL, dns.Client()
			}
			start := time.Now()
			_, done, err := setupSOCKSRequest(t, cfg, 1, tc.host, tc.pause)
			if err == nil || time.Since(start) > 350*time.Millisecond {
				t.Fatalf("setup did not stop at its shared deadline: %v, %v", err, time.Since(start))
			}
			<-done
			failure := readRuntimeState(cfg.TurnPool.statePath).LastFailure
			if failure == nil || !strings.Contains(failure.Message, "TURN 目标连接") || !strings.Contains(failure.Message, "deadline exceeded") {
				t.Fatalf("missing timeout stage: %+v", failure)
			}
			cfg.TurnPool.mu.Lock()
			cooling := !cfg.TurnPool.servers[0].FailedUntil.IsZero()
			cfg.TurnPool.mu.Unlock()
			if cooling {
				t.Fatal("request budget incorrectly cooled a healthy node")
			}
		})
	}
}

func TestEstablishedTCPAndUDPSurviveSetupDeadline(t *testing.T) {
	for _, command := range []byte{1, 3} {
		t.Run(fmt.Sprint(command), func(t *testing.T) {
			turn := serveSetupTURN(t, nil)
			cfg := setupTestConfig(t, turn)
			cfg.Timeout = 80 * time.Millisecond
			// Use the loopback TCP transport for this UDP association as well.
			cfg.TurnPool.servers[0].UDPFailedUntil = time.Now().Add(time.Minute)
			client, _, err := setupSOCKSRequest(t, cfg, command, "192.0.2.1", 0)
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(2 * cfg.Timeout)
			if err := writeAll(client, []byte("still alive")); err != nil {
				t.Fatalf("established connection inherited setup deadline: %v", err)
			}
			if command == 1 {
				data := make([]byte, len("still alive"))
				if _, err := io.ReadFull(client, data); err != nil || string(data) != "still alive" {
					t.Fatalf("established TCP cannot relay: %q, %v", data, err)
				}
			}
		})
	}
}

func TestSharedDNSLookupSurvivesWaiterDeadline(t *testing.T) {
	const host = "shared-setup.example"
	dnsCache.Delete(host)
	defer dnsCache.Delete(host)
	started, unblock := make(chan struct{}), make(chan struct{})
	allow := sync.OnceFunc(func() { close(unblock) })
	var requests atomic.Int32
	dns := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			close(started)
		}
		query, _ := io.ReadAll(r.Body)
		<-unblock
		writeSetupDNSResponse(w, query)
	}))
	defer dns.Close()
	defer allow()
	cfg := Config{DoH: dns.URL, DoHClient: dns.Client(), Timeout: time.Second, DNSTTL: time.Minute}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := resolveDoH(ctx, host, cfg)
		done <- err
	}()
	<-started
	if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("DNS waiter exceeded its own budget: %v", err)
	}
	allow()
	ip, err := resolveDoH(context.Background(), host, cfg)
	if err != nil || !ip.Equal(net.IPv4(192, 0, 2, 1)) || requests.Load() != 1 {
		t.Fatalf("canceled waiter affected shared lookup: %v, %v, requests=%d", ip, err, requests.Load())
	}
}

func TestTCPSetupDeadlinePreservesSharedAllocation(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(fmt.Sprintf("queued=%v", queued), func(t *testing.T) {
			unblock := make(chan struct{})
			allow := sync.OnceFunc(func() { close(unblock) })
			defer allow()
			var connects atomic.Int32
			turn := serveSetupTURN(t, func(_ net.Conn, req, _ *stun.Message) bool {
				if req.Type.Method == MethodConnect && connects.Add(1) == 2 && !queued {
					<-unblock
				}
				return false
			})
			cfg := setupTestConfig(t, turn)
			first, release, _, err := dialTurnTCP(&setupContext{Context: context.Background()}, cfg, net.IPv4(192, 0, 2, 1), 443)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			defer first.Close()
			a := cfg.TCPAllocs.allocs[turn.String()][0]
			unlock := func() {}
			if queued {
				a.ctrlMu.Lock()
				unlock = sync.OnceFunc(a.ctrlMu.Unlock)
				defer unlock()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
			defer cancel()
			start := time.Now()
			_, _, _, err = dialTurnTCP(&setupContext{Context: ctx}, cfg, net.IPv4(192, 0, 2, 2), 443)
			if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 250*time.Millisecond {
				t.Fatalf("shared control wait exceeded setup deadline: %v, %v", err, time.Since(start))
			}
			_ = first.SetDeadline(time.Now().Add(time.Second))
			if err := writeAll(first, []byte("live")); err != nil {
				t.Fatal(err)
			}
			var data [4]byte
			if _, err := io.ReadFull(first, data[:]); err != nil || string(data[:]) != "live" {
				t.Fatalf("another request's timeout broke active data: %q, %v", data, err)
			}
			unlock()
			allow()
			deadline := time.Now().Add(time.Second)
			for {
				a.peerMu.Lock()
				pending := a.connecting
				a.peerMu.Unlock()
				if pending == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("expired setup reservation was not released")
				}
				time.Sleep(time.Millisecond)
			}
			if a.isClosed() || !a.retired.Load() {
				t.Fatal("expired setup did not preserve established peers on a retired allocation")
			}
			if err := a.refresh(); err != nil {
				t.Fatalf("shared control framing or deadline was damaged: %v", err)
			}
			if queued && connects.Load() != 1 {
				t.Fatal("expired queued setup sent a new CONNECT")
			}
		})
	}
}

func TestSetupDeadlineCoversNodeRetries(t *testing.T) {
	first := serveSetupTURN(t, func(_ net.Conn, req, res *stun.Message) bool {
		if req.Type.Method == MethodAllocate {
			time.Sleep(50 * time.Millisecond)
			res.Type.Class = stun.ClassErrorResponse
			_ = (stun.ErrorCodeAttribute{Code: 500, Reason: []byte("test failure")}).AddTo(res)
		}
		return false
	})
	second := serveSetupTURN(t, func(_ net.Conn, req, _ *stun.Message) bool {
		if req.Type.Method == MethodAllocate {
			time.Sleep(100 * time.Millisecond)
		}
		return false
	})
	var thirdAttempts atomic.Int32
	third := serveSetupTURN(t, func(_ net.Conn, req, _ *stun.Message) bool {
		if req.Type.Method == MethodAllocate {
			thirdAttempts.Add(1)
		}
		return false
	})
	cfg := setupTestConfig(t, first, second, third)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, _, err := dialTurnTCP(&setupContext{Context: ctx}, cfg, net.IPv4(192, 0, 2, 1), 443)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 270*time.Millisecond {
		t.Fatalf("node retry reset the setup deadline: %v, %v", err, time.Since(start))
	}
	if thirdAttempts.Load() != 0 {
		t.Fatal("started another node after budget expired")
	}
	cfg.TurnPool.mu.Lock()
	defer cfg.TurnPool.mu.Unlock()
	if cfg.TurnPool.servers[0].FailedUntil.IsZero() || !cfg.TurnPool.servers[1].FailedUntil.IsZero() {
		t.Fatal("node health did not distinguish a real failure from exhausted request time")
	}
}

func TestSetupDeadlineCoversAuthenticationRetries(t *testing.T) {
	for _, udp := range []bool{false, true} {
		t.Run(fmt.Sprintf("udp=%v", udp), func(t *testing.T) {
			var attempts atomic.Int32
			turn := serveSetupTURN(t, func(_ net.Conn, req, res *stun.Message) bool {
				if req.Type.Method != MethodAllocate {
					return false
				}
				n := attempts.Add(1)
				time.Sleep(70 * time.Millisecond)
				if n <= 2 {
					res.Type.Class = stun.ClassErrorResponse
					code := stun.ErrorCode(401)
					if n == 2 {
						code = 438
					}
					_ = (stun.ErrorCodeAttribute{Code: code, Reason: []byte("authentication")}).AddTo(res)
					_ = stun.Realm("setup.example").AddTo(res)
					_ = stun.Nonce("setup-nonce").AddTo(res)
				}
				if n > 1 {
					_ = stun.NewLongTermIntegrity("test", "setup.example", "secret").AddTo(res)
				}
				return false
			})
			turn.Username, turn.Password, turn.ExplicitAuth = "test", "secret", true
			cfg := setupTestConfig(t, turn)
			ctx, cancel := context.WithTimeout(context.Background(), 110*time.Millisecond)
			defer cancel()
			setup := &setupContext{Context: ctx}
			start := time.Now()
			var err error
			if udp {
				_, err = newUDPSessionWithNetwork(setup, cfg, nil, nil, turn, "tcp")
				if ctxErr := setup.err(); ctxErr != nil {
					err = ctxErr
				}
			} else {
				_, _, _, err = dialTurnTCP(setup, cfg, net.IPv4(192, 0, 2, 1), 443)
			}
			if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 260*time.Millisecond || attempts.Load() != 2 {
				t.Fatalf("authentication reset setup budget: %v, %v, attempts=%d", err, time.Since(start), attempts.Load())
			}
		})
	}
}
