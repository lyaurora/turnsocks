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

	"github.com/pion/stun/v4"
)

type recordingSTUNConn struct {
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

func TestAcceptLoopRecoversAfterError(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	ln := &retryListener{
		conn:     server,
		accepted: make(chan struct{}),
		closed:   make(chan struct{}),
	}
	defer ln.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		acceptLoop(ln, Config{Timeout: time.Second})
	}()
	select {
	case <-ln.accepted:
	case <-time.After(time.Second):
		t.Fatal("accept loop did not recover after an error")
	}
	_ = ln.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("accept loop did not stop after listener close")
	}
}

func TestSTUNMessageWriteTimeoutAndHeader(test *testing.T) {
	for _, transport := range []string{"tcp", "udp"} {
		test.Run(transport, func(test *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			var writer stunConn = &tcpSTUNConn{conn: client}
			if transport == "udp" {
				writer = &udpSTUNConn{conn: client}
			}
			message := stun.New()
			message.Type = stun.MessageType{Method: MethodAllocate, Class: stun.ClassRequest}
			message.TransactionID = stun.NewTransactionID()
			if err := writer.writeMessage(message, 10*time.Millisecond); !isTimeoutError(err) {
				test.Fatalf("blocked write = %v, want timeout", err)
			}
			done := make(chan error, 1)
			go func() {
				_ = server.SetReadDeadline(time.Now().Add(time.Second))
				received, err := readSTUNMessage(server)
				if err == nil && (received.Type != message.Type || received.TransactionID != message.TransactionID) {
					err = errors.New("STUN header changed during write")
				}
				done <- err
			}()
			if err := writer.writeMessage(message, 0); err != nil {
				test.Fatalf("write after timeout = %v", err)
			}
			if err := <-done; err != nil {
				test.Fatal(err)
			}
		})
	}
}

func TestSocksUDPIPv4Packet(test *testing.T) {
	session := &udpSession{}
	for _, address := range []net.IP{{192, 0, 2, 1}, net.IPv4(192, 0, 2, 1)} {
		for _, payload := range [][]byte{bytes.Repeat([]byte{0xa5}, 64), {7}, nil} {
			packet := session.buildSocksUDPIPv4(address, 5353, payload)
			want := append([]byte{0, 0, 0, 1, 192, 0, 2, 1, 0x14, 0xe9}, payload...)
			if !bytes.Equal(packet, want) {
				test.Fatalf("UDP packet = %x, want %x", packet, want)
			}
		}
	}
}

func TestUDPSessionRegistryCloseAllReleasesActiveAllocations(t *testing.T) {
	registry := newUDPSessionRegistry()
	client, server := net.Pipe()
	defer client.Close()
	var release *stun.Message
	turnConn := &recordingSTUNConn{onWrite: func(message *stun.Message, _ int) { release = message }}
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
	if release == nil || release.Type.Method != MethodRefresh || release.Type.Class != stun.ClassRequest {
		t.Fatalf("release = %v, want Refresh request", release)
	}
	lifetime, err := release.Get(AttrLifetime)
	if err != nil || !bytes.Equal(lifetime, []byte{0, 0, 0, 0}) {
		t.Fatalf("release lifetime = %x, err = %v; want 0", lifetime, err)
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

func TestSTUNReadersRespectIntegrityBoundary(t *testing.T) {
	for _, transport := range []string{"tcp", "udp"} {
		for _, signed := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/signed=%v", transport, signed), func(t *testing.T) {
				realm := stun.Realm("example.org")
				connID := []byte{0, 0, 0, 1}
				response := stun.New()
				response.Type = stun.MessageType{Method: MethodConnect, Class: stun.ClassSuccessResponse}
				response.TransactionID = stun.NewTransactionID()
				if signed {
					response.Add(AttrConnectionID, connID)
				}
				response.WriteHeader()
				if err := stun.NewLongTermIntegrity("user", realm.String(), "password").AddTo(response); err != nil {
					t.Fatal(err)
				}
				if !signed {
					response.Add(AttrConnectionID, connID)
				}
				if err := stun.Fingerprint.AddTo(response); err != nil {
					t.Fatal(err)
				}

				client, server := net.Pipe()
				defer client.Close()
				defer server.Close()
				var reader stunConn = &tcpSTUNConn{conn: client}
				if transport == "udp" {
					reader = &udpSTUNConn{conn: client}
				}
				written := make(chan error, 1)
				go func() {
					written <- (&tcpSTUNConn{conn: server}).writeMessage(response, time.Second)
				}()
				decoded, err := reader.readMessage(time.Second)
				if err != nil {
					t.Fatal(err)
				}
				if err := <-written; err != nil {
					t.Fatal(err)
				}
				if err := validateLongTermIntegrity(decoded, "user", "password", &realm); err != nil {
					t.Fatal(err)
				}
				if err := stun.Fingerprint.Check(decoded); err != nil {
					t.Fatal(err)
				}
				got, err := getConnectionID(decoded)
				if signed && (err != nil || !bytes.Equal(got, connID)) {
					t.Fatalf("authenticated CONNECTION-ID = %x, err = %v", got, err)
				}
				if !signed && err == nil {
					t.Fatal("accepted CONNECTION-ID after MESSAGE-INTEGRITY")
				}
			})
		}
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
	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()

	if got, want := connectedTurnAddr(conn, "fallback:3478"), conn.RemoteAddr().String(); got != want {
		t.Fatalf("connectedTurnAddr() = %q, want %q", got, want)
	}
}

func TestResolveDoHCachePolicy(test *testing.T) {
	for _, scenario := range []struct {
		host         string
		negative     bool
		wantRequests int32
	}{
		{"missing.example", true, 1},
		{"zero-ttl.example", false, 2},
	} {
		test.Run(scenario.host, func(test *testing.T) {
			dnsCache.Delete(scenario.host)
			defer dnsCache.Delete(scenario.host)

			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requests.Add(1)
				query, err := io.ReadAll(request.Body)
				if err != nil || len(query) < 12 {
					http.Error(writer, "bad query", http.StatusBadRequest)
					return
				}
				response := append([]byte(nil), query...)
				binary.BigEndian.PutUint16(response[2:4], 0x8180)
				if scenario.negative {
					response[3] = 0x83
				} else {
					binary.BigEndian.PutUint16(response[6:8], 1)
					response = append(response, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 0, 0, 4, 192, 0, 2, 1)
				}
				writer.Header().Set("Content-Type", "application/dns-message")
				_, _ = writer.Write(response)
			}))
			defer server.Close()

			cfg := Config{DoH: server.URL, DoHClient: server.Client(), DNSTTL: time.Minute, Timeout: time.Second}
			for range 2 {
				if _, err := resolveDoH(context.Background(), scenario.host, cfg); (err != nil) != scenario.negative {
					test.Fatalf("resolveDoH() error = %v, want error %v", err, scenario.negative)
				}
			}
			if got := requests.Load(); got != scenario.wantRequests {
				test.Fatalf("DoH requests = %d, want %d", got, scenario.wantRequests)
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
