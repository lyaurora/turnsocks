package proxy

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/stun/v3"
)

type recordingSTUNConn struct {
	writes     chan *stun.Message
	closed     atomic.Bool
	writeCount atomic.Int32
	onWrite    func(*stun.Message, int)
	onWriteRaw func([]byte)
	readFunc   func(time.Duration) (*stun.Message, error)
}

type retryListener struct {
	conn      net.Conn
	calls     atomic.Int32
	accepted  chan struct{}
	closed    chan struct{}
	closeOnce sync.Once
}

type temporaryAcceptError struct{}

func (temporaryAcceptError) Error() string   { return "temporary accept failure" }
func (temporaryAcceptError) Timeout() bool   { return false }
func (temporaryAcceptError) Temporary() bool { return true }

func (l *retryListener) Accept() (net.Conn, error) {
	switch l.calls.Add(1) {
	case 1:
		return nil, temporaryAcceptError{}
	case 2:
		close(l.accepted)
		return l.conn, nil
	default:
		<-l.closed
		return nil, net.ErrClosed
	}
}

func (l *retryListener) Close() error {
	l.closeOnce.Do(func() { close(l.closed) })
	return nil
}

func (l *retryListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}
}

func (c *recordingSTUNConn) readMessage(timeout time.Duration) (*stun.Message, error) {
	if c.readFunc != nil {
		return c.readFunc(timeout)
	}
	return nil, errors.New("not implemented")
}

func (c *recordingSTUNConn) readMessageOrData(time.Duration) (*stun.Message, turnUDPData, bool, error) {
	return nil, turnUDPData{}, false, errors.New("not implemented")
}

func (c *recordingSTUNConn) writeMessage(m *stun.Message, _ time.Duration) error {
	m.WriteHeader()
	clone := stun.New()
	clone.Raw = append([]byte(nil), m.Raw...)
	if err := clone.Decode(); err != nil {
		return err
	}
	count := int(c.writeCount.Add(1))
	if c.writes != nil {
		c.writes <- clone
	}
	if c.onWrite != nil {
		c.onWrite(clone, count)
	}
	return nil
}

func (c *recordingSTUNConn) writeRaw(raw []byte, _ time.Duration) error {
	if c.onWriteRaw != nil {
		c.onWriteRaw(raw)
	}
	return nil
}

func (c *recordingSTUNConn) close() error {
	c.closed.Store(true)
	return nil
}

func TestUDPSessionFailureClosesControlConnection(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	s := &udpSession{clientTCP: server, closed: make(chan struct{})}
	s.fail()

	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	var buf [1]byte
	if _, err := client.Read(buf[:]); !errors.Is(err, io.EOF) {
		t.Fatalf("control connection read error = %v, want EOF", err)
	}
}

func TestAcceptLoopRecoversAfterError(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	ln := &retryListener{
		conn:     server,
		accepted: make(chan struct{}),
		closed:   make(chan struct{}),
	}
	p := &proxyController{
		cfg:     Config{Timeout: time.Second},
		ln:      ln,
		running: true,
	}

	go p.acceptLoop(ln)
	select {
	case <-ln.accepted:
	case <-time.After(time.Second):
		t.Fatal("accept loop did not recover after an error")
	}
	p.stop()
}

func TestUDPSessionCloseReleasesAllocation(t *testing.T) {
	turnConn := &recordingSTUNConn{writes: make(chan *stun.Message, 1)}
	s := &udpSession{turnConn: turnConn, closed: make(chan struct{})}

	s.close()

	select {
	case msg := <-turnConn.writes:
		if msg.Type.Method != MethodRefresh || msg.Type.Class != stun.ClassRequest {
			t.Fatalf("release type = %v, want Refresh request", msg.Type)
		}
		lifetime, err := msg.Get(AttrLifetime)
		if err != nil {
			t.Fatalf("release missing lifetime: %v", err)
		}
		if got := binary.BigEndian.Uint32(lifetime); got != 0 {
			t.Fatalf("release lifetime = %d, want 0", got)
		}
	case <-time.After(time.Second):
		t.Fatal("release request was not sent")
	}
	if !turnConn.closed.Load() {
		t.Fatal("TURN connection was not closed")
	}
}

func TestUDPSessionRegistryCloseAllReleasesActiveAllocations(t *testing.T) {
	registry := newUDPSessionRegistry()
	client, server := net.Pipe()
	defer client.Close()
	turnConn := &recordingSTUNConn{}
	s := &udpSession{
		clientTCP: server,
		turnConn:  turnConn,
		closed:    make(chan struct{}),
	}
	if !registry.add(s) {
		t.Fatal("active UDP session was not registered")
	}

	registry.closeAll()

	if !turnConn.closed.Load() {
		t.Fatal("TURN connection was not closed")
	}
	if got := turnConn.writeCount.Load(); got != 1 {
		t.Fatalf("release writes = %d, want 1", got)
	}
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	var buf [1]byte
	if _, err := client.Read(buf[:]); !errors.Is(err, io.EOF) {
		t.Fatalf("control connection read error = %v, want EOF", err)
	}
	if registry.add(&udpSession{}) {
		t.Fatal("closed registry accepted a new session")
	}
}

func TestValidateSTUNResponse(t *testing.T) {
	req := stun.New()
	req.Type = stun.MessageType{Method: MethodRefresh, Class: stun.ClassRequest}
	req.TransactionID = stun.NewTransactionID()

	res := stun.New()
	res.Type = stun.MessageType{Method: MethodRefresh, Class: stun.ClassSuccessResponse}
	res.TransactionID = req.TransactionID
	if err := validateSTUNResponse(req, res); err != nil {
		t.Fatal(err)
	}

	res.TransactionID = stun.NewTransactionID()
	if err := validateSTUNResponse(req, res); err == nil {
		t.Fatal("mismatched transaction ID was accepted")
	}
	res.TransactionID = req.TransactionID
	res.Type = stun.MessageType{Method: MethodAllocate, Class: stun.ClassSuccessResponse}
	if err := validateSTUNResponse(req, res); err == nil {
		t.Fatal("mismatched method was accepted")
	}
}

func TestValidateLongTermIntegrity(t *testing.T) {
	const username = "user"
	const password = "password"
	realm := stun.Realm("example.org")
	res := stun.New()
	res.Type = stun.MessageType{Method: MethodRefresh, Class: stun.ClassSuccessResponse}
	res.TransactionID = stun.NewTransactionID()
	if err := stun.NewLongTermIntegrity(username, realm.String(), password).AddTo(res); err != nil {
		t.Fatal(err)
	}
	if err := validateLongTermIntegrity(res, username, password, &realm); err != nil {
		t.Fatal(err)
	}
	if err := validateLongTermIntegrity(res, username, "wrong", &realm); err == nil {
		t.Fatal("response with invalid integrity was accepted")
	}
}

func TestRefreshAllocationRetriesUnsignedStaleNonce(t *testing.T) {
	const username = "user"
	const password = "password"
	realm := stun.Realm("example.org")
	nonce := stun.Nonce("old-nonce")
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	done := make(chan error, 1)
	go func() {
		first, err := readSTUNMessage(server)
		if err != nil {
			done <- err
			return
		}
		stale := stun.New()
		stale.Type = stun.MessageType{Method: MethodRefresh, Class: stun.ClassErrorResponse}
		stale.TransactionID = first.TransactionID
		stun.ErrorCodeAttribute{Code: stun.CodeStaleNonce, Reason: []byte("Stale Nonce")}.AddTo(stale)
		stun.Nonce("new-nonce").AddTo(stale)
		realm.AddTo(stale)
		if err := writeSTUNMessage(server, stale); err != nil {
			done <- err
			return
		}

		second, err := readSTUNMessage(server)
		if err != nil {
			done <- err
			return
		}
		var gotNonce stun.Nonce
		if err := gotNonce.GetFrom(second); err != nil || gotNonce.String() != "new-nonce" {
			done <- fmt.Errorf("retry nonce = %q, err = %v", gotNonce.String(), err)
			return
		}
		success := stun.New()
		success.Type = stun.MessageType{Method: MethodRefresh, Class: stun.ClassSuccessResponse}
		success.TransactionID = second.TransactionID
		success.WriteHeader()
		if err := stun.NewLongTermIntegrity(username, realm.String(), password).AddTo(success); err != nil {
			done <- err
			return
		}
		done <- writeSTUNMessage(server, success)
	}()

	err := refreshAllocation(client, Config{Timeout: time.Second}, username, password, &realm, &nonce, true)
	if err != nil {
		t.Fatal(err)
	}
	if nonce.String() != "new-nonce" {
		t.Fatalf("stored nonce = %q, want new-nonce", nonce.String())
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRetiredTCPAllocationClosesAfterLastPeer(t *testing.T) {
	ctrlClient, ctrlServer := net.Pipe()
	defer ctrlServer.Close()
	a := &tcpAllocation{
		ctrlConn:    ctrlClient,
		stop:        make(chan struct{}),
		activePeers: map[string]struct{}{"active": {}, "timed-out": {}},
		dataConns:   make(map[net.Conn]struct{}),
	}

	a.retire()
	if a.isClosed() {
		t.Fatal("retired allocation closed while peers were active")
	}
	if a.tryReservePeer("new") {
		t.Fatal("retired allocation accepted a new peer")
	}
	a.releasePeer("timed-out")
	if a.isClosed() {
		t.Fatal("retired allocation closed before the last peer ended")
	}
	a.releasePeer("active")
	if !a.isClosed() {
		t.Fatal("retired allocation stayed open after the last peer ended")
	}
}

func TestDoSTUNIgnoresUnrelatedResponse(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	done := make(chan error, 1)
	go func() {
		req, err := readSTUNMessage(server)
		if err != nil {
			done <- err
			return
		}
		unrelated := stun.New()
		unrelated.Type = stun.MessageType{Method: req.Type.Method, Class: stun.ClassSuccessResponse}
		unrelated.TransactionID = stun.NewTransactionID()
		if err := writeSTUNMessage(server, unrelated); err != nil {
			done <- err
			return
		}
		res := stun.New()
		res.Type = stun.MessageType{Method: req.Type.Method, Class: stun.ClassSuccessResponse}
		res.TransactionID = req.TransactionID
		done <- writeSTUNMessage(server, res)
	}()

	req := stun.New()
	req.Type = stun.MessageType{Method: MethodRefresh, Class: stun.ClassRequest}
	req.TransactionID = stun.NewTransactionID()
	res, err := doSTUN(context.Background(), client, req, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res.TransactionID != req.TransactionID {
		t.Fatal("doSTUN returned unrelated response")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestUDPRequestRetransmitsAfterDroppedResponse(t *testing.T) {
	turnConn := &recordingSTUNConn{}
	s := &udpSession{
		cfg:         Config{Timeout: time.Second},
		turnConn:    turnConn,
		turnNetwork: "udp",
		pending:     make(map[string]chan *stun.Message),
		closed:      make(chan struct{}),
	}
	turnConn.onWrite = func(req *stun.Message, count int) {
		if count != 2 {
			return
		}
		res := stun.New()
		res.Type = stun.MessageType{Method: req.Type.Method, Class: stun.ClassSuccessResponse}
		res.TransactionID = req.TransactionID
		key := s.txIDKey(req.TransactionID)
		s.pendingMu.Lock()
		ch := s.pending[key]
		s.pendingMu.Unlock()
		ch <- res
	}

	req := stun.New()
	req.Type = stun.MessageType{Method: MethodRefresh, Class: stun.ClassRequest}
	req.TransactionID = stun.NewTransactionID()
	if _, err := s.request(req, time.Second); err != nil {
		t.Fatalf("request failed after retransmission: %v", err)
	}
	if got := turnConn.writeCount.Load(); got != 2 {
		t.Fatalf("request writes = %d, want 2", got)
	}
}

func TestUDPInitialRequestRetransmitsAfterTimeout(t *testing.T) {
	turnConn := &recordingSTUNConn{}
	req := stun.New()
	req.Type = stun.MessageType{Method: MethodAllocate, Class: stun.ClassRequest}
	req.TransactionID = stun.NewTransactionID()
	var reads atomic.Int32
	turnConn.readFunc = func(timeout time.Duration) (*stun.Message, error) {
		if reads.Add(1) == 1 {
			time.Sleep(timeout)
			return nil, &net.DNSError{Err: "dropped response", IsTimeout: true}
		}
		res := stun.New()
		res.Type = stun.MessageType{Method: MethodAllocate, Class: stun.ClassSuccessResponse}
		res.TransactionID = req.TransactionID
		return res, nil
	}
	s := &udpSession{turnConn: turnConn, turnNetwork: "udp"}

	if _, err := s.initialRequest(context.Background(), req, time.Second); err != nil {
		t.Fatalf("initial request failed after retransmission: %v", err)
	}
	if got := turnConn.writeCount.Load(); got != 2 {
		t.Fatalf("initial request writes = %d, want 2", got)
	}
}

func TestUDPFirstPacketWaitsForPermission(t *testing.T) {
	local, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	turnConn := &recordingSTUNConn{}
	s := &udpSession{
		cfg:      Config{Timeout: time.Second},
		localUDP: local, turnConn: turnConn, turnNetwork: "udp",
		pending:     make(map[string]chan *stun.Message),
		permissions: make(map[[4]byte]time.Time),
		closed:      make(chan struct{}),
	}
	var granted atomic.Bool
	sent := make(chan []byte, 2)
	turnConn.onWriteRaw = func(raw []byte) {
		if granted.Load() {
			sent <- append([]byte(nil), raw...)
		}
	}
	turnConn.onWrite = func(req *stun.Message, count int) {
		// Drop the first permission request, then accept its retransmission.
		if req.Type.Method != MethodCreatePermission || count != 2 {
			return
		}
		granted.Store(true)
		res := stun.New()
		res.Type = stun.MessageType{Method: MethodCreatePermission, Class: stun.ClassSuccessResponse}
		res.TransactionID = req.TransactionID
		s.pendingMu.Lock()
		ch := s.pending[s.txIDKey(req.TransactionID)]
		s.pendingMu.Unlock()
		ch <- res
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.readLocalUDPLoop()
	}()
	defer func() {
		s.close()
		<-done
	}()
	client, err := net.DialUDP("udp", nil, local.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for _, payload := range []byte{1, 2} {
		if _, err := client.Write([]byte{0, 0, 0, 1, 192, 0, 2, 1, 0, 53, payload}); err != nil {
			t.Fatal(err)
		}
		select {
		case raw := <-sent:
			msg, err := decodeSTUNMessage(raw)
			if err != nil {
				t.Fatal(err)
			}
			data, err := msg.Get(AttrData)
			if err != nil || len(data) != 1 || data[0] != payload {
				t.Fatalf("forwarded payload = %v, err = %v", data, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("UDP payload was lost before permission was established")
		}
	}
	if got := turnConn.writeCount.Load(); got != 2 {
		t.Fatalf("permission requests = %d, want 2 including the retry", got)
	}
}

func TestUDPPermissionRetriesStaleNonce(t *testing.T) {
	turnConn := &recordingSTUNConn{}
	s := &udpSession{
		cfg: Config{Timeout: time.Second}, turnConn: turnConn, turnNetwork: "udp",
		username: "user", password: "password", realm: stun.Realm("example.org"),
		nonce: stun.Nonce("old-nonce"), needAuth: true,
		pending:     make(map[string]chan *stun.Message),
		permissions: make(map[[4]byte]time.Time),
		closed:      make(chan struct{}),
	}
	defer s.close()
	var retryNonce stun.Nonce
	turnConn.onWrite = func(req *stun.Message, count int) {
		if req.Type.Method != MethodCreatePermission {
			return
		}
		res := stun.New()
		res.Type = stun.MessageType{Method: MethodCreatePermission, Class: stun.ClassSuccessResponse}
		res.TransactionID = req.TransactionID
		if count == 1 {
			res.Type.Class = stun.ClassErrorResponse
			stun.ErrorCodeAttribute{Code: stun.CodeStaleNonce, Reason: []byte("Stale Nonce")}.AddTo(res)
			stun.Nonce("new-nonce").AddTo(res)
		} else {
			_ = retryNonce.GetFrom(req)
			res.WriteHeader()
			_ = stun.NewLongTermIntegrity(s.username, s.realm.String(), s.password).AddTo(res)
		}
		s.pendingMu.Lock()
		ch := s.pending[s.txIDKey(req.TransactionID)]
		s.pendingMu.Unlock()
		ch <- res
	}
	ip := net.IPv4(192, 0, 2, 1)
	if err := s.ensurePermission(ip); err != nil {
		t.Fatal(err)
	}
	if retryNonce.String() != "new-nonce" {
		t.Fatalf("retry nonce = %q", retryNonce.String())
	}
	if err := s.ensurePermission(ip); err != nil {
		t.Fatal(err)
	}
	if got := turnConn.writeCount.Load(); got != 2 {
		t.Fatalf("cached permission sent another request: %d writes", got)
	}
}

func TestUDPPermissionFailureClosesOnlyUnusableSessions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		code     stun.ErrorCode
		wantLive bool
	}{
		{name: "expired allocation", code: stun.CodeAllocMismatch},
		{name: "authentication rejected", code: stun.CodeUnauthorized},
		{name: "request timeout"},
		{name: "forbidden peer", code: stun.CodeForbidden, wantLive: true},
		{name: "peer address family", code: stun.CodePeerAddrFamilyMismatch, wantLive: true},
		{name: "permission capacity", code: stun.CodeInsufficientCapacity, wantLive: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			local, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			control, peer := net.Pipe()
			defer peer.Close()
			turnConn := &recordingSTUNConn{}
			s := &udpSession{
				cfg: Config{Timeout: time.Second}, clientTCP: control, localUDP: local,
				turnConn: turnConn, turnNetwork: "udp", closed: make(chan struct{}),
				pending: make(map[string]chan *stun.Message),
				permissions: map[[4]byte]time.Time{
					{192, 0, 2, 2}: time.Now().Add(time.Minute),
				},
			}
			forwarded := make(chan []byte, 1)
			turnConn.onWriteRaw = func(raw []byte) { forwarded <- append([]byte(nil), raw...) }
			turnConn.onWrite = func(req *stun.Message, _ int) {
				if req.Type.Method != MethodCreatePermission || tc.code == 0 {
					return
				}
				res := stun.New()
				res.Type = stun.MessageType{Method: MethodCreatePermission, Class: stun.ClassErrorResponse}
				res.TransactionID = req.TransactionID
				_ = (stun.ErrorCodeAttribute{Code: tc.code}).AddTo(res)
				s.pendingMu.Lock()
				ch := s.pending[s.txIDKey(req.TransactionID)]
				s.pendingMu.Unlock()
				ch <- res
			}
			done := make(chan struct{})
			go func() { defer close(done); s.readLocalUDPLoop() }()
			defer func() { s.fail(); <-done }()
			client, err := net.DialUDP("udp4", nil, local.LocalAddr().(*net.UDPAddr))
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			targets := []byte{1}
			if tc.wantLive {
				targets = append(targets, 2)
			}
			for _, last := range targets {
				if _, err := client.Write([]byte{0, 0, 0, 1, 192, 0, 2, last, 0, 53, last}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.wantLive {
				select {
				case raw := <-forwarded:
					msg, err := decodeSTUNMessage(raw)
					if err != nil {
						t.Fatal(err)
					}
					data, err := msg.Get(AttrData)
					if err != nil || !bytes.Equal(data, []byte{2}) || s.isClosed() {
						t.Fatalf("unrelated peer stopped forwarding: %v, %v", data, err)
					}
				case <-time.After(time.Second):
					t.Fatal("peer refusal blocked an existing permission")
				}
				return
			}
			_ = peer.SetReadDeadline(time.Now().Add(7 * time.Second))
			var buf [1]byte
			if _, err := peer.Read(buf[:]); !errors.Is(err, io.EOF) {
				t.Fatalf("unusable session left SOCKS control open: %v", err)
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("UDP reader did not stop")
			}
			if !turnConn.closed.Load() || len(forwarded) != 0 {
				t.Fatal("unusable TURN session was still used")
			}
		})
	}
}

func TestUDPPrewarmTakeStopsExpiry(t *testing.T) {
	turn := turnServerConfig{Addr: "turn.example:3478"}
	s := &udpSession{closed: make(chan struct{})}
	fired := make(chan struct{}, 1)
	p := &udpPrewarmPool{
		session: s,
		turnKey: turn.String(),
		network: "udp",
		created: time.Now(),
	}
	p.expiry = time.AfterFunc(20*time.Millisecond, func() {
		fired <- struct{}{}
	})

	got, _, ok := p.take(Config{}, turn, nil, nil)
	if !ok || got != s {
		t.Fatal("prewarmed session was not returned")
	}
	defer got.close()

	select {
	case <-fired:
		t.Fatal("prewarm expiry fired after session was taken")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestUDPPrewarmCloseRejectsNewSession(t *testing.T) {
	p := newUDPPrewarmPool()
	p.close()
	if err := p.add(Config{}, turnServerConfig{Addr: "turn.example:3478"}); err != nil {
		t.Fatal(err)
	}
	if p.session != nil || !p.closed {
		t.Fatal("closed prewarm pool accepted a new session")
	}
}

func TestConnectedTurnAddrUsesControlPeer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, _ := ln.Accept()
		accepted <- conn
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	peer := <-accepted
	defer peer.Close()

	if got, want := connectedTurnAddr(conn, "fallback:3478"), conn.RemoteAddr().String(); got != want {
		t.Fatalf("connectedTurnAddr() = %q, want %q", got, want)
	}
}

func TestResolveDoHCachesDNSFailure(t *testing.T) {
	const host = "missing.example"
	dnsCache.Delete(host)
	defer dnsCache.Delete(host)

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		raw, err := io.ReadAll(r.Body)
		if err != nil || len(raw) < 2 {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		response := make([]byte, 12)
		binary.BigEndian.PutUint16(response[0:2], binary.BigEndian.Uint16(raw[0:2]))
		response[2] = 0x81
		response[3] = 0x83
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(response)
	}))
	defer server.Close()

	cfg := Config{DoH: server.URL, DoHClient: server.Client(), DNSTTL: time.Minute, Timeout: time.Second}
	for i := 0; i < 2; i++ {
		if _, err := resolveDoH(context.Background(), host, cfg); err == nil {
			t.Fatal("resolveDoH() succeeded, want DNS error")
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("DoH requests = %d, want 1", got)
	}
}

func TestResolveDoHDoesNotCacheZeroTTL(t *testing.T) {
	const host = "zero-ttl.example"
	dnsCache.Delete(host)
	defer dnsCache.Delete(host)

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		query, err := io.ReadAll(r.Body)
		if err != nil || len(query) < 12 {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		response := append([]byte(nil), query...)
		response[2] = 0x81
		response[3] = 0x80
		binary.BigEndian.PutUint16(response[6:8], 1)
		response = append(response,
			0xc0, 0x0c,
			0x00, 0x01,
			0x00, 0x01,
			0x00, 0x00, 0x00, 0x00,
			0x00, 0x04,
			192, 0, 2, 1,
		)
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(response)
	}))
	defer server.Close()

	cfg := Config{DoH: server.URL, DoHClient: server.Client(), DNSTTL: time.Minute, Timeout: time.Second}
	for i := 0; i < 2; i++ {
		if _, err := resolveDoH(context.Background(), host, cfg); err != nil {
			t.Fatal(err)
		}
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("DoH requests = %d, want 2 for TTL 0", got)
	}
}

func TestDNSCacheRespectsCNAMETTL(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cnameTTL uint32
		aFirst   bool
		maxTTL   time.Duration
		want     time.Duration
	}{
		{"short alias", 1, false, 300 * time.Second, time.Second},
		{"A before alias", 1, true, 300 * time.Second, time.Second},
		{"zero alias TTL", 0, true, 300 * time.Second, 0},
		{"TTL cap", 300, false, time.Minute, time.Minute},
		{"uncapped", 600, false, 0, 300 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg, id, err := buildDNSAQuery("alias.example")
			if err != nil {
				t.Fatal(err)
			}
			binary.BigEndian.PutUint16(msg[2:4], 0x8180)
			binary.BigEndian.PutUint16(msg[6:8], 2)
			target, err := encodeDNSName("target.example")
			if err != nil {
				t.Fatal(err)
			}
			cname := []byte{0xc0, 0x0c, 0, 5, 0, 1}
			cname = binary.BigEndian.AppendUint32(cname, tc.cnameTTL)
			cname = binary.BigEndian.AppendUint16(cname, uint16(len(target)))
			cname = append(cname, target...)
			a := append([]byte(nil), target...)
			a = append(a, 0, 1, 0, 1, 0, 0, 1, 44, 0, 4, 192, 0, 2, 1)
			if tc.aFirst {
				msg = append(append(msg, a...), cname...)
			} else {
				msg = append(append(msg, cname...), a...)
			}
			ip, ttl, err := parseDNSAResponse(msg, id, "alias.example", tc.maxTTL)
			if err != nil || !ip.Equal(net.IPv4(192, 0, 2, 1)) || ttl != tc.want {
				t.Fatalf("IP = %v, TTL = %s, err = %v; want TTL %s", ip, ttl, err, tc.want)
			}
		})
	}
}

func TestConnectErrorClassification(t *testing.T) {
	for _, code := range []int{403, 446, 447} {
		if !isConnectPeerError(code) {
			t.Fatalf("CONNECT error %d should remain peer-specific", code)
		}
	}
	for _, code := range []int{401, 437, 441, 500, 508} {
		if isConnectPeerError(code) {
			t.Fatalf("CONNECT error %d should allow TURN failover", code)
		}
	}
}

func TestPruneExpiredPermissions(t *testing.T) {
	now := time.Now()
	expired := [4]byte{192, 0, 2, 1}
	active := [4]byte{192, 0, 2, 2}
	s := &udpSession{permissions: map[[4]byte]time.Time{
		expired: now.Add(-time.Second),
		active:  now.Add(time.Minute),
	}}

	s.pruneExpiredPermissions(now)

	if _, ok := s.permissions[expired]; ok {
		t.Fatal("expired permission was not removed")
	}
	if _, ok := s.permissions[active]; !ok {
		t.Fatal("active permission was removed")
	}
}
