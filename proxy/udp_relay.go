package proxy

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/pion/stun/v3"
)

type udpSession struct {
	cfg          Config
	turn         turnServerConfig
	username     string
	password     string
	clientTCP    net.Conn
	localUDP     *net.UDPConn
	turnConn     stunConn
	turnNetwork  string
	realm        stun.Realm
	nonce        stun.Nonce
	needAuth     bool
	authMu       sync.Mutex
	writeMu      sync.Mutex
	trafficOnce  sync.Once
	pendingMu    sync.Mutex
	pending      map[string]chan *stun.Message
	permissions  map[[4]byte]time.Time
	permPending  map[[4]byte]struct{}
	permissionMu sync.Mutex
	socksUDPBuf  []byte
	sendBuf      []byte
	sendTxID     uint64
	clientAddrMu sync.RWMutex
	clientAddr   *net.UDPAddr
	closed       chan struct{}
	closeOnce    sync.Once
}

func handleUDPAssociate(clientTCP net.Conn, cfg Config) {
	localUDP, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		_ = writeSocksReply(clientTCP, 0x01, "0.0.0.0", 0)
		return
	}
	tuneUDPConn(localUDP)

	bindPort := localUDP.LocalAddr().(*net.UDPAddr).Port
	s, turnAddr, err := newUDPSession(cfg, clientTCP, localUDP)
	if err != nil {
		log.Printf("UDP TURN failed: %v", err)
		_ = writeSocksReply(clientTCP, 0x05, "0.0.0.0", 0)
		localUDP.Close()
		return
	}

	if err := writeSocksReply(clientTCP, 0x00, "127.0.0.1", bindPort); err != nil {
		s.close()
		return
	}

	if cfg.LogVerbose {
		log.Printf("UDP ASSOCIATE listening on 127.0.0.1:%d via %s", bindPort, turnAddr)
	}

	go s.readTurnLoop()
	go s.readLocalUDPLoop()
	go s.refreshLoop()

	// SOCKS5 UDP association lasts while TCP control connection remains open.
	_, _ = io.Copy(io.Discard, clientTCP)
	s.close()
}

func newUDPSession(cfg Config, clientTCP net.Conn, localUDP *net.UDPConn) (*udpSession, string, error) {
	var errs []error
	candidates := cfg.TurnPool.candidates()
	if len(candidates) == 0 {
		return nil, "", errors.New("no TURN server candidates")
	}
	for _, turn := range candidates {
		var err error
		if cfg.TurnPool.udpAllowed(turn) {
			if s, turnAddr, ok := cfg.UDPPrewarm.take(cfg, turn, clientTCP, localUDP); ok {
				cfg.TurnPool.markUDPSuccess(turn)
				return s, turnAddr, nil
			}
			var s *udpSession
			s, err = newUDPSessionWithNetwork(cfg, clientTCP, localUDP, turn, "udp")
			if err == nil {
				cfg.TurnPool.markUDPSuccess(turn)
				go prewarmUDPAllocation(cfg)
				return s, turn.Addr + "/udp", nil
			}
			cfg.TurnPool.markUDPFailure(turn, err)
			errs = append(errs, fmt.Errorf("%s/udp: %w", turn.Addr, err))
			if cfg.LogVerbose {
				log.Printf("UDP TURN-over-UDP candidate failed via %s: %v", turn.Addr, err)
			}
		} else if cfg.LogVerbose {
			log.Printf("skip TURN-over-UDP candidate via %s during cooldown", turn.Addr)
		}

		s, tcpErr := newUDPSessionWithNetwork(cfg, clientTCP, localUDP, turn, "tcp")
		if tcpErr == nil {
			return s, turn.Addr + "/tcp", nil
		}
		errs = append(errs, fmt.Errorf("%s/tcp: %w", turn.Addr, tcpErr))
		if err != nil {
			log.Printf("UDP TURN candidate failed via %s: %v", turn.Addr, errors.Join(err, tcpErr))
		} else {
			log.Printf("UDP TURN candidate failed via %s/tcp: %v", turn.Addr, tcpErr)
		}
	}
	return nil, "", errors.Join(errs...)
}

func newUDPSessionWithNetwork(cfg Config, clientTCP net.Conn, localUDP *net.UDPConn, turn turnServerConfig, network string) (*udpSession, error) {
	if !cfg.TurnPool.contains(turn) {
		return nil, errors.New("TURN server removed from pool")
	}
	conn, err := dialSTUNConn(network, turn.Addr, cfg.Timeout)
	if err != nil {
		return nil, err
	}
	username, password := turn.auth()

	s := &udpSession{
		cfg:         cfg,
		turn:        turn,
		username:    username,
		password:    password,
		clientTCP:   clientTCP,
		localUDP:    localUDP,
		turnConn:    conn,
		turnNetwork: network,
		pending:     make(map[string]chan *stun.Message),
		permissions: make(map[[4]byte]time.Time),
		permPending: make(map[[4]byte]struct{}),
		sendTxID:    uint64(time.Now().UnixNano()),
		closed:      make(chan struct{}),
	}
	allocateTimeout := cfg.Timeout
	if network == "udp" {
		allocateTimeout = shorterTimeout(cfg.Timeout, turnUDPAttemptTimeout)
	}
	if err := s.allocate(allocateTimeout); err != nil {
		_ = conn.close()
		return nil, err
	}
	if !cfg.TurnPool.contains(turn) {
		s.close()
		return nil, errors.New("TURN server removed from pool")
	}
	return s, nil
}

func shorterTimeout(a time.Duration, b time.Duration) time.Duration {
	if a <= 0 {
		return b
	}
	if b <= 0 || a < b {
		return a
	}
	return b
}

func dialSTUNConn(network string, addr string, timeout time.Duration) (stunConn, error) {
	var (
		conn net.Conn
		err  error
	)
	if network == "tcp" {
		conn, err = dialTCPKeepAlive(addr, shorterTimeout(timeout, turnTCPDialTimeout))
	} else {
		conn, err = net.DialTimeout(network, addr, timeout)
		if err == nil {
			tuneUDPConn(conn)
		}
	}
	if err != nil {
		return nil, err
	}
	switch network {
	case "udp":
		return &udpSTUNConn{conn: conn, readBuf: make([]byte, 65535)}, nil
	case "tcp":
		return &tcpSTUNConn{conn: conn}, nil
	default:
		_ = conn.Close()
		return nil, fmt.Errorf("unsupported STUN network %q", network)
	}
}

func tuneUDPConn(conn net.Conn) {
	type bufferSetter interface {
		SetReadBuffer(int) error
		SetWriteBuffer(int) error
	}
	if c, ok := conn.(bufferSetter); ok {
		_ = c.SetReadBuffer(udpSocketBufferSize)
		_ = c.SetWriteBuffer(udpSocketBufferSize)
	}
}

type stunConn interface {
	readMessage(timeout time.Duration) (*stun.Message, error)
	readMessageOrData(timeout time.Duration) (*stun.Message, turnUDPData, bool, error)
	writeMessage(m *stun.Message, timeout time.Duration) error
	writeRaw(raw []byte, timeout time.Duration) error
	close() error
}

type turnUDPData struct {
	ip4     [4]byte
	port    int
	payload []byte
}

type tcpSTUNConn struct {
	conn net.Conn
}

func (c *tcpSTUNConn) readMessage(timeout time.Duration) (*stun.Message, error) {
	if timeout > 0 {
		if err := c.conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			return nil, err
		}
		defer c.conn.SetReadDeadline(time.Time{})
	}
	return readSTUNMessage(c.conn)
}

func (c *tcpSTUNConn) readMessageOrData(timeout time.Duration) (*stun.Message, turnUDPData, bool, error) {
	m, err := c.readMessage(timeout)
	return m, turnUDPData{}, false, err
}

func (c *tcpSTUNConn) writeMessage(m *stun.Message, timeout time.Duration) error {
	if timeout > 0 {
		if err := c.conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
			return err
		}
		defer c.conn.SetWriteDeadline(time.Time{})
	}
	return writeSTUNMessage(c.conn, m)
}

func (c *tcpSTUNConn) writeRaw(raw []byte, timeout time.Duration) error {
	if timeout > 0 {
		if err := c.conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
			return err
		}
		defer c.conn.SetWriteDeadline(time.Time{})
	}
	return writeAll(c.conn, raw)
}

func (c *tcpSTUNConn) close() error {
	return c.conn.Close()
}

type udpSTUNConn struct {
	conn    net.Conn
	readBuf []byte
}

func (c *udpSTUNConn) readMessage(timeout time.Duration) (*stun.Message, error) {
	if timeout > 0 {
		if err := c.conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			return nil, err
		}
		defer c.conn.SetReadDeadline(time.Time{})
	}

	if c.readBuf == nil {
		c.readBuf = make([]byte, 65535)
	}
	n, err := c.conn.Read(c.readBuf)
	if err != nil {
		return nil, err
	}
	return decodeSTUNMessage(c.readBuf[:n])
}

func (c *udpSTUNConn) readMessageOrData(timeout time.Duration) (*stun.Message, turnUDPData, bool, error) {
	if timeout > 0 {
		if err := c.conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			return nil, turnUDPData{}, false, err
		}
		defer c.conn.SetReadDeadline(time.Time{})
	}

	if c.readBuf == nil {
		c.readBuf = make([]byte, 65535)
	}
	n, err := c.conn.Read(c.readBuf)
	if err != nil {
		return nil, turnUDPData{}, false, err
	}
	raw := c.readBuf[:n]
	if len(raw) >= 2 && binary.BigEndian.Uint16(raw[0:2]) == dataIndicationMessageType {
		data, ok := parseTurnUDPDataIndication(raw)
		if !ok {
			return nil, turnUDPData{}, false, nil
		}
		return nil, data, true, nil
	}
	m, err := decodeSTUNMessage(raw)
	return m, turnUDPData{}, false, err
}

func (c *udpSTUNConn) writeMessage(m *stun.Message, timeout time.Duration) error {
	if timeout > 0 {
		if err := c.conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
			return err
		}
		defer c.conn.SetWriteDeadline(time.Time{})
	}
	m.WriteHeader()
	n, err := c.conn.Write(m.Raw)
	if err != nil {
		return err
	}
	if n != len(m.Raw) {
		return io.ErrShortWrite
	}
	return nil
}

func (c *udpSTUNConn) writeRaw(raw []byte, timeout time.Duration) error {
	if timeout > 0 {
		if err := c.conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
			return err
		}
		defer c.conn.SetWriteDeadline(time.Time{})
	}
	n, err := c.conn.Write(raw)
	if err != nil {
		return err
	}
	if n != len(raw) {
		return io.ErrShortWrite
	}
	return nil
}

func (c *udpSTUNConn) close() error {
	return c.conn.Close()
}

func (s *udpSession) close() {
	s.closeOnce.Do(func() {
		close(s.closed)
		if s.localUDP != nil {
			_ = s.localUDP.Close()
		}
		if s.turnConn != nil {
			s.releaseAllocation()
			_ = s.turnConn.close()
		}
	})
}

func (s *udpSession) releaseAllocation() {
	req := stun.New()
	req.Type = stun.MessageType{Method: MethodRefresh, Class: stun.ClassRequest}
	req.TransactionID = stun.NewTransactionID()
	addLifetime(req, 0)
	if err := s.addAuthToRequest(req); err != nil {
		return
	}

	s.writeMu.Lock()
	_ = s.turnConn.writeMessage(req, shorterTimeout(s.cfg.Timeout, turnReleaseTimeout))
	s.writeMu.Unlock()
}

func (s *udpSession) fail() {
	if s.clientTCP != nil {
		_ = s.clientTCP.Close()
	}
	s.close()
}

func (s *udpSession) isClosed() bool {
	select {
	case <-s.closed:
		return true
	default:
		return false
	}
}

func (s *udpSession) txIDKey(id [12]byte) string {
	return string(id[:])
}

func (s *udpSession) request(req *stun.Message, timeout time.Duration) (*stun.Message, error) {
	ch := make(chan *stun.Message, 1)
	key := s.txIDKey(req.TransactionID)

	s.pendingMu.Lock()
	s.pending[key] = ch
	s.pendingMu.Unlock()

	defer func() {
		s.pendingMu.Lock()
		delete(s.pending, key)
		s.pendingMu.Unlock()
	}()

	if err := s.writeRequest(req, timeout); err != nil {
		return nil, err
	}
	return s.waitForResponse(req, ch, timeout)
}

func (s *udpSession) writeRequest(req *stun.Message, timeout time.Duration) error {
	s.writeMu.Lock()
	err := s.turnConn.writeMessage(req, timeout)
	s.writeMu.Unlock()
	return err
}

func (s *udpSession) waitForResponse(req *stun.Message, ch <-chan *stun.Message, timeout time.Duration) (*stun.Message, error) {
	deadline := time.Now().Add(timeout)
	timeoutTimer := time.NewTimer(timeout)
	defer timeoutTimer.Stop()

	var retryTimer *time.Timer
	var retry <-chan time.Time
	retryDelay := turnUDPRetryRTO
	if s.turnNetwork == "udp" && retryDelay < timeout {
		retryTimer = time.NewTimer(retryDelay)
		retry = retryTimer.C
		defer retryTimer.Stop()
	}

	for {
		select {
		case res := <-ch:
			return res, nil
		case <-retry:
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return nil, errTURNRequestTimeout
			}
			if err := s.writeRequest(req, shorterTimeout(s.cfg.Timeout, remaining)); err != nil {
				return nil, err
			}
			retryDelay *= 2
			remaining = time.Until(deadline)
			if retryDelay >= remaining {
				retry = nil
				continue
			}
			retryTimer.Reset(retryDelay)
		case <-timeoutTimer.C:
			return nil, errTURNRequestTimeout
		case <-s.closed:
			return nil, errors.New("session closed")
		}
	}
}

func (s *udpSession) addAuthToRequest(req *stun.Message) error {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	if !s.needAuth {
		return nil
	}
	return addAuthToMessage(req, s.username, s.password, &s.realm, &s.nonce)
}

func (s *udpSession) updateStaleNonce(res *stun.Message) (bool, error) {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	if !s.needAuth {
		return false, nil
	}
	return updateAuthFromError(res, &s.realm, &s.nonce)
}

func (s *udpSession) allocate(timeout time.Duration) error {
	req := stun.New()
	req.Type = stun.MessageType{Method: MethodAllocate, Class: stun.ClassRequest}
	req.TransactionID = stun.NewTransactionID()
	req.Add(AttrRequestedTransport, []byte{0x11, 0x00, 0x00, 0x00})

	res, err := s.initialRequest(req, timeout)
	if err != nil {
		return err
	}

	if res.Type.Class == stun.ClassSuccessResponse {
		s.needAuth = false
		return nil
	}

	if res.Type.Class != stun.ClassErrorResponse {
		return fmt.Errorf("unexpected UDP allocate response: %v", res.Type)
	}

	code, reason := getErrorCode(res)
	if code != 401 {
		return fmt.Errorf("UDP allocate error %d %s", code, reason)
	}

	if err := s.realm.GetFrom(res); err != nil {
		return fmt.Errorf("UDP allocate auth missing realm: %w", err)
	}
	if err := s.nonce.GetFrom(res); err != nil {
		return fmt.Errorf("UDP allocate auth missing nonce: %w", err)
	}
	s.needAuth = true

	for attempt := 0; attempt < 2; attempt++ {
		req2 := stun.New()
		req2.Type = stun.MessageType{Method: MethodAllocate, Class: stun.ClassRequest}
		req2.TransactionID = stun.NewTransactionID()
		req2.Add(AttrRequestedTransport, []byte{0x11, 0x00, 0x00, 0x00})
		if err := addAuthToMessage(req2, s.username, s.password, &s.realm, &s.nonce); err != nil {
			return err
		}

		res2, err := s.initialRequest(req2, timeout)
		if err != nil {
			return err
		}
		if res2.Type.Class == stun.ClassSuccessResponse {
			return nil
		}
		stale, err := updateAuthFromError(res2, &s.realm, &s.nonce)
		if stale {
			if err != nil {
				return err
			}
			if attempt == 0 {
				continue
			}
		}
		c, r := getErrorCode(res2)
		return fmt.Errorf("UDP allocate auth error %d %s", c, r)
	}
	return nil
}

func (s *udpSession) initialRequest(req *stun.Message, timeout time.Duration) (*stun.Message, error) {
	deadline := time.Now().Add(timeout)
	retryDelay := turnUDPRetryRTO

	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, errTURNRequestTimeout
		}
		if err := s.turnConn.writeMessage(req, remaining); err != nil {
			return nil, err
		}

		wait := remaining
		if s.turnNetwork == "udp" && retryDelay < wait {
			wait = retryDelay
		}
		res, err := s.turnConn.readMessage(wait)
		if err == nil {
			if res.TransactionID == req.TransactionID {
				return res, nil
			}
			continue
		}
		if s.turnNetwork != "udp" || !isTimeoutError(err) || time.Now().After(deadline) {
			return nil, err
		}
		retryDelay *= 2
	}
}

func (s *udpSession) refreshLoop() {
	ticker := time.NewTicker(allocationRefreshEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.pruneExpiredPermissions(time.Now())
			if err := s.refreshAllocation(); err != nil {
				select {
				case <-time.After(refreshRetryDelay):
				case <-s.closed:
					return
				}
				if retryErr := s.refreshAllocation(); retryErr != nil {
					s.cfg.TurnPool.markUDPFailure(s.turn, retryErr)
					log.Printf("UDP allocation refresh failed via %s after retry: %v", s.turn.Addr, errors.Join(err, retryErr))
					s.fail()
					return
				}
				if s.cfg.LogVerbose {
					log.Printf("UDP allocation refresh recovered via %s after retry: %v", s.turn.Addr, err)
				}
			}
		case <-s.closed:
			return
		}
	}
}

func (s *udpSession) pruneExpiredPermissions(now time.Time) {
	s.permissionMu.Lock()
	for key, expires := range s.permissions {
		if !now.Before(expires) {
			delete(s.permissions, key)
		}
	}
	s.permissionMu.Unlock()
}

func (s *udpSession) refreshAllocation() error {
	for attempt := 0; attempt < 2; attempt++ {
		req := stun.New()
		req.Type = stun.MessageType{Method: MethodRefresh, Class: stun.ClassRequest}
		req.TransactionID = stun.NewTransactionID()
		addLifetime(req, allocationLifetime)
		if err := s.addAuthToRequest(req); err != nil {
			return err
		}

		res, err := s.request(req, s.cfg.Timeout)
		if err != nil {
			return err
		}
		if res.Type.Class == stun.ClassSuccessResponse {
			return nil
		}
		stale, err := s.updateStaleNonce(res)
		if stale {
			if err != nil {
				return err
			}
			if attempt == 0 {
				if s.cfg.LogVerbose {
					log.Printf("TURN UDP nonce refreshed via %s after stale refresh nonce", s.turn.Addr)
				}
				continue
			}
		}
		code, reason := getErrorCode(res)
		return fmt.Errorf("refresh error %d %s", code, reason)
	}
	return fmt.Errorf("refresh error %d Stale Nonce", staleNonceCode)
}

func (s *udpSession) readTurnLoop() {
	for {
		m, data, ok, err := s.turnConn.readMessageOrData(0)
		if err != nil {
			s.fail()
			return
		}
		if ok {
			s.handleUDPData(data)
			continue
		}
		if m == nil {
			continue
		}

		if m.Type.Method == MethodData && m.Type.Class == stun.ClassIndication {
			s.handleDataIndication(m)
			continue
		}

		key := s.txIDKey(m.TransactionID)
		s.pendingMu.Lock()
		ch := s.pending[key]
		s.pendingMu.Unlock()

		if ch != nil {
			select {
			case ch <- m:
			default:
			}
		}
	}
}

func parseTurnUDPDataIndication(raw []byte) (turnUDPData, bool) {
	if len(raw) < 20 {
		return turnUDPData{}, false
	}
	if binary.BigEndian.Uint32(raw[4:8]) != stunMagicCookie {
		return turnUDPData{}, false
	}
	length := int(binary.BigEndian.Uint16(raw[2:4]))
	if length%4 != 0 || length > maxSTUNMessageLength {
		return turnUDPData{}, false
	}
	total := 20 + length
	if len(raw) < total {
		return turnUDPData{}, false
	}

	var (
		ip4     [4]byte
		port    int
		hasPeer bool
		payload []byte
	)
	for offset := 20; offset < total; {
		if offset+4 > total {
			return turnUDPData{}, false
		}
		attrType := stun.AttrType(binary.BigEndian.Uint16(raw[offset : offset+2]))
		attrLen := int(binary.BigEndian.Uint16(raw[offset+2 : offset+4]))
		valueStart := offset + 4
		valueEnd := valueStart + attrLen
		if valueEnd > total {
			return turnUDPData{}, false
		}

		switch attrType {
		case AttrXORPeerAddress:
			if attrLen != 8 || raw[valueStart+1] != 1 {
				return turnUDPData{}, false
			}
			port = int(binary.BigEndian.Uint16(raw[valueStart+2:valueStart+4]) ^ uint16(stunMagicCookie>>16))
			ip4[0] = raw[valueStart+4] ^ byte(stunMagicCookie>>24)
			ip4[1] = raw[valueStart+5] ^ byte(stunMagicCookie>>16&0xff)
			ip4[2] = raw[valueStart+6] ^ byte(stunMagicCookie>>8&0xff)
			ip4[3] = raw[valueStart+7] ^ byte(stunMagicCookie&0xff)
			hasPeer = true
		case AttrData:
			payload = raw[valueStart:valueEnd]
		}

		next := valueEnd + ((4 - attrLen%4) % 4)
		if next > total {
			return turnUDPData{}, false
		}
		offset = next
	}
	if !hasPeer || payload == nil || port == 0 {
		return turnUDPData{}, false
	}
	return turnUDPData{ip4: ip4, port: port, payload: payload}, true
}

func (s *udpSession) handleUDPData(data turnUDPData) {
	s.clientAddrMu.RLock()
	caddr := s.clientAddr
	s.clientAddrMu.RUnlock()
	if caddr == nil {
		return
	}

	pkt := s.buildSocksUDPIPv4Raw(data.ip4, data.port, data.payload)
	_, _ = s.localUDP.WriteToUDP(pkt, caddr)
}

func (s *udpSession) handleDataIndication(m *stun.Message) {
	peerRaw, err := m.Get(AttrXORPeerAddress)
	if err != nil {
		return
	}
	data, err := m.Get(AttrData)
	if err != nil {
		return
	}
	ip, port, err := decodeXORPeerAddress(peerRaw)
	if err != nil {
		return
	}

	s.clientAddrMu.RLock()
	caddr := s.clientAddr
	s.clientAddrMu.RUnlock()
	if caddr == nil {
		return
	}

	pkt := s.buildSocksUDPIPv4(ip, port, data)
	_, _ = s.localUDP.WriteToUDP(pkt, caddr)
}

func (s *udpSession) acceptClientAddr(addr *net.UDPAddr) bool {
	s.clientAddrMu.Lock()
	defer s.clientAddrMu.Unlock()

	if s.clientAddr == nil {
		s.clientAddr = cloneUDPAddr(addr)
		return true
	}
	return sameUDPAddr(s.clientAddr, addr)
}

func cloneUDPAddr(addr *net.UDPAddr) *net.UDPAddr {
	if addr == nil {
		return nil
	}
	ip := append(net.IP(nil), addr.IP...)
	return &net.UDPAddr{IP: ip, Port: addr.Port, Zone: addr.Zone}
}

func sameUDPAddr(a *net.UDPAddr, b *net.UDPAddr) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Port == b.Port && a.Zone == b.Zone && a.IP.Equal(b.IP)
}

func (s *udpSession) buildSocksUDPIPv4(ip net.IP, port int, payload []byte) []byte {
	ip4 := ip.To4()
	size := 10 + len(payload)
	if cap(s.socksUDPBuf) < size {
		s.socksUDPBuf = make([]byte, size)
	}
	pkt := s.socksUDPBuf[:size]
	pkt[0] = 0
	pkt[1] = 0
	pkt[2] = 0
	pkt[3] = 0x01
	copy(pkt[4:8], ip4)
	binary.BigEndian.PutUint16(pkt[8:10], uint16(port))
	copy(pkt[10:], payload)
	return pkt
}

func (s *udpSession) buildSocksUDPIPv4Raw(ip4 [4]byte, port int, payload []byte) []byte {
	size := 10 + len(payload)
	if cap(s.socksUDPBuf) < size {
		s.socksUDPBuf = make([]byte, size)
	}
	pkt := s.socksUDPBuf[:size]
	pkt[0] = 0
	pkt[1] = 0
	pkt[2] = 0
	pkt[3] = 0x01
	copy(pkt[4:8], ip4[:])
	binary.BigEndian.PutUint16(pkt[8:10], uint16(port))
	copy(pkt[10:], payload)
	return pkt
}

func (s *udpSession) readLocalUDPLoop() {
	buf := make([]byte, 65535)

	for {
		n, caddr, err := s.localUDP.ReadFromUDP(buf)
		if err != nil {
			s.fail()
			return
		}

		if !s.acceptClientAddr(caddr) {
			if s.cfg.LogVerbose {
				log.Printf("drop UDP packet from unexpected client %s", caddr.String())
			}
			continue
		}

		ip, host, port, payload, err := parseSocksUDPPacket(buf[:n])
		if err != nil {
			continue
		}

		if ip == nil {
			ip, err = resolveDoH(host, s.cfg)
			if err != nil {
				if s.cfg.LogVerbose {
					log.Printf("UDP resolve failed %s: %v", host, err)
				}
				continue
			}
		}

		if err := s.ensurePermission(ip); err != nil {
			if s.cfg.LogVerbose {
				log.Printf("CreatePermission failed %s: %v", ip.String(), err)
			}
			continue
		}

		raw, err := s.buildSendIndication(ip, port, payload)
		if err != nil {
			continue
		}

		s.writeMu.Lock()
		err = s.turnConn.writeRaw(raw, s.cfg.Timeout)
		s.writeMu.Unlock()
		if err != nil {
			s.fail()
			return
		}
		s.markUDPTraffic()
	}
}

func (s *udpSession) markUDPTraffic() {
	s.trafficOnce.Do(func() {
		if s.cfg.TurnPool != nil {
			s.cfg.TurnPool.markSuccess(s.turn)
		}
	})
}

func (s *udpSession) buildSendIndication(ip net.IP, port int, payload []byte) ([]byte, error) {
	if port < 0 || port > 65535 {
		return nil, fmt.Errorf("invalid port %d", port)
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return nil, errors.New("only IPv4 is supported")
	}
	if len(payload) > 0xffff {
		return nil, fmt.Errorf("UDP payload too large: %d", len(payload))
	}

	dataPad := (4 - len(payload)%4) % 4
	msgLen := 12 + 4 + len(payload) + dataPad
	size := 20 + msgLen
	if cap(s.sendBuf) < size {
		s.sendBuf = make([]byte, size)
	}
	raw := s.sendBuf[:size]

	binary.BigEndian.PutUint16(raw[0:2], sendIndicationMessageType)
	binary.BigEndian.PutUint16(raw[2:4], uint16(msgLen))
	binary.BigEndian.PutUint32(raw[4:8], stunMagicCookie)
	s.sendTxID++
	binary.BigEndian.PutUint32(raw[8:12], uint32(s.sendTxID>>32))
	binary.BigEndian.PutUint64(raw[12:20], s.sendTxID)

	offset := 20
	binary.BigEndian.PutUint16(raw[offset:offset+2], uint16(AttrXORPeerAddress))
	binary.BigEndian.PutUint16(raw[offset+2:offset+4], 8)
	raw[offset+4] = 0
	raw[offset+5] = 1
	binary.BigEndian.PutUint16(raw[offset+6:offset+8], uint16(port)^uint16(stunMagicCookie>>16))
	raw[offset+8] = ip4[0] ^ byte(stunMagicCookie>>24)
	raw[offset+9] = ip4[1] ^ byte(stunMagicCookie>>16&0xff)
	raw[offset+10] = ip4[2] ^ byte(stunMagicCookie>>8&0xff)
	raw[offset+11] = ip4[3] ^ byte(stunMagicCookie&0xff)

	offset += 12
	binary.BigEndian.PutUint16(raw[offset:offset+2], uint16(AttrData))
	binary.BigEndian.PutUint16(raw[offset+2:offset+4], uint16(len(payload)))
	copy(raw[offset+4:], payload)
	for i := offset + 4 + len(payload); i < size; i++ {
		raw[i] = 0
	}

	return raw, nil
}

func parseSocksUDPPacket(pkt []byte) (net.IP, string, int, []byte, error) {
	if len(pkt) < 4 {
		return nil, "", 0, nil, errors.New("short UDP packet")
	}
	if pkt[0] != 0 || pkt[1] != 0 || pkt[2] != 0 {
		return nil, "", 0, nil, errors.New("fragmented UDP is not supported")
	}

	atyp := pkt[3]
	switch atyp {
	case 0x01:
		if len(pkt) < 10 {
			return nil, "", 0, nil, errors.New("short IPv4 packet")
		}
		ip := net.IP(pkt[4:8])
		port := int(binary.BigEndian.Uint16(pkt[8:10]))
		if port == 0 {
			return nil, "", 0, nil, errors.New("invalid UDP port 0")
		}
		return ip, "", port, pkt[10:], nil

	case 0x03:
		if len(pkt) < 5 {
			return nil, "", 0, nil, errors.New("short domain packet")
		}
		l := int(pkt[4])
		if len(pkt) < 5+l+2 {
			return nil, "", 0, nil, errors.New("bad domain packet")
		}
		if l == 0 {
			return nil, "", 0, nil, errors.New("empty domain name")
		}
		host := string(pkt[5 : 5+l])
		port := int(binary.BigEndian.Uint16(pkt[5+l : 5+l+2]))
		if port == 0 {
			return nil, "", 0, nil, errors.New("invalid UDP port 0")
		}
		return nil, host, port, pkt[5+l+2:], nil

	case 0x04:
		return nil, "", 0, nil, errors.New("IPv6 is not supported")

	default:
		return nil, "", 0, nil, errors.New("unsupported ATYP")
	}
}

func (s *udpSession) ensurePermission(ip net.IP) error {
	key, ok := permissionKey(ip)
	if !ok {
		return errors.New("only IPv4 is supported")
	}

	now := time.Now()
	s.permissionMu.Lock()
	exp, ok := s.permissions[key]
	if ok && now.Before(exp) {
		s.permissionMu.Unlock()
		return nil
	}
	if _, ok := s.permPending[key]; ok {
		s.permissionMu.Unlock()
		return nil
	}
	s.permissionMu.Unlock()

	req, err := s.buildCreatePermission(ip)
	if err != nil {
		return err
	}

	// Queue CreatePermission before the Send Indication, but do not wait for
	// the response. This removes one TURN RTT from the first UDP packet.
	s.permissionMu.Lock()
	exp, ok = s.permissions[key]
	if ok && time.Now().Before(exp) {
		s.permissionMu.Unlock()
		return nil
	}
	if _, ok := s.permPending[key]; ok {
		s.permissionMu.Unlock()
		return nil
	}
	s.permPending[key] = struct{}{}
	s.permissionMu.Unlock()

	txKey, ch, err := s.sendPermissionRequest(req, 5*time.Second)
	if err != nil {
		s.permissionMu.Lock()
		delete(s.permPending, key)
		s.permissionMu.Unlock()
		return err
	}
	go s.finishPermission(key, req, txKey, ch)

	return nil
}

func (s *udpSession) buildCreatePermission(ip net.IP) (*stun.Message, error) {
	req := stun.New()
	req.Type = stun.MessageType{Method: MethodCreatePermission, Class: stun.ClassRequest}
	req.TransactionID = stun.NewTransactionID()
	if err := addXORPeerAddress(req, ip, 0); err != nil {
		return nil, err
	}
	if err := s.addAuthToRequest(req); err != nil {
		return nil, err
	}
	return req, nil
}

func (s *udpSession) sendPermissionRequest(req *stun.Message, timeout time.Duration) (string, chan *stun.Message, error) {
	ch := make(chan *stun.Message, 1)
	txKey := s.txIDKey(req.TransactionID)

	s.pendingMu.Lock()
	s.pending[txKey] = ch
	s.pendingMu.Unlock()

	if err := s.writeRequest(req, timeout); err != nil {
		s.pendingMu.Lock()
		delete(s.pending, txKey)
		s.pendingMu.Unlock()
		return "", nil, err
	}

	return txKey, ch, nil
}

func (s *udpSession) finishPermission(key [4]byte, req *stun.Message, txKey string, ch chan *stun.Message) {
	defer func() {
		s.pendingMu.Lock()
		delete(s.pending, txKey)
		s.pendingMu.Unlock()

		s.permissionMu.Lock()
		delete(s.permPending, key)
		s.permissionMu.Unlock()
	}()

	res, err := s.waitForResponse(req, ch, 5*time.Second)
	if err == nil {
		if res.Type.Class != stun.ClassSuccessResponse {
			code, reason := getErrorCode(res)
			if code == staleNonceCode {
				if _, err := s.updateStaleNonce(res); err != nil {
					if s.cfg.LogVerbose {
						log.Printf("permission stale nonce update failed: %v", err)
					}
				} else if s.retryPermission(key) {
					return
				}
			}
			if s.cfg.LogVerbose {
				log.Printf("permission error %d %s", code, reason)
			}
			return
		}
		s.permissionMu.Lock()
		s.permissions[key] = time.Now().Add(240 * time.Second)
		s.permissionMu.Unlock()
		return
	}
	if errors.Is(err, errTURNRequestTimeout) {
		if s.cfg.LogVerbose {
			log.Printf("CreatePermission timed out")
		}
	}
}

func (s *udpSession) retryPermission(key [4]byte) bool {
	ip := net.IPv4(key[0], key[1], key[2], key[3])
	req, err := s.buildCreatePermission(ip)
	if err != nil {
		if s.cfg.LogVerbose {
			log.Printf("permission retry build failed %s: %v", ip.String(), err)
		}
		return false
	}

	txKey, ch, err := s.sendPermissionRequest(req, 5*time.Second)
	if err != nil {
		if s.cfg.LogVerbose {
			log.Printf("permission retry send failed %s: %v", ip.String(), err)
		}
		return false
	}
	defer func() {
		s.pendingMu.Lock()
		delete(s.pending, txKey)
		s.pendingMu.Unlock()
	}()

	res, waitErr := s.waitForResponse(req, ch, 5*time.Second)
	if waitErr == nil {
		if res.Type.Class == stun.ClassSuccessResponse {
			s.permissionMu.Lock()
			s.permissions[key] = time.Now().Add(240 * time.Second)
			s.permissionMu.Unlock()
			if s.cfg.LogVerbose {
				log.Printf("CreatePermission recovered after stale nonce for %s", ip.String())
			}
			return true
		}
		code, reason := getErrorCode(res)
		if code == staleNonceCode {
			if _, err := s.updateStaleNonce(res); err != nil && s.cfg.LogVerbose {
				log.Printf("permission retry stale nonce update failed: %v", err)
			}
		}
		if s.cfg.LogVerbose {
			log.Printf("permission retry error %d %s", code, reason)
		}
	} else if errors.Is(waitErr, errTURNRequestTimeout) {
		if s.cfg.LogVerbose {
			log.Printf("CreatePermission retry timed out")
		}
	}
	return false
}

func permissionKey(ip net.IP) ([4]byte, bool) {
	ip4 := ip.To4()
	if ip4 == nil {
		return [4]byte{}, false
	}
	return [4]byte{ip4[0], ip4[1], ip4[2], ip4[3]}, true
}
