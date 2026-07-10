package proxy

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
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
	readFunc   func(time.Duration) (*stun.Message, error)
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

func (c *recordingSTUNConn) writeRaw([]byte, time.Duration) error {
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

	if _, err := s.initialRequest(req, time.Second); err != nil {
		t.Fatalf("initial request failed after retransmission: %v", err)
	}
	if got := turnConn.writeCount.Load(); got != 2 {
		t.Fatalf("initial request writes = %d, want 2", got)
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
		if _, err := resolveDoH(host, cfg); err == nil {
			t.Fatal("resolveDoH() succeeded, want DNS error")
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("DoH requests = %d, want 1", got)
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
