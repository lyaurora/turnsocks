package proxy

import (
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/stun/v3"
)

type tcpAllocation struct {
	cfg         Config
	turn        turnServerConfig
	username    string
	password    string
	ctrlConn    net.Conn
	serverAddr  string
	realm       stun.Realm
	nonce       stun.Nonce
	needAuth    bool
	stop        chan struct{}
	ctrlMu      sync.Mutex
	closed      atomic.Bool
	closeOnce   sync.Once
	peerMu      sync.Mutex
	activePeers map[string]struct{}
	connecting  int
	dataMu      sync.Mutex
	dataConns   map[net.Conn]struct{}
	closeData   bool
}

func allocateTCP(conn net.Conn, cfg Config, turn turnServerConfig) (stun.Realm, stun.Nonce, bool, error) {
	req := stun.New()
	req.Type = stun.MessageType{Method: MethodAllocate, Class: stun.ClassRequest}
	req.TransactionID = stun.NewTransactionID()
	req.Add(AttrRequestedTransport, []byte{0x06, 0x00, 0x00, 0x00})

	res, err := doSTUN(conn, req, cfg.Timeout)
	if err != nil {
		return stun.Realm{}, stun.Nonce{}, false, err
	}

	if res.Type.Class == stun.ClassSuccessResponse {
		return stun.Realm{}, stun.Nonce{}, false, nil
	}

	if res.Type.Class != stun.ClassErrorResponse {
		return stun.Realm{}, stun.Nonce{}, false, fmt.Errorf("unexpected allocate response: %v", res.Type)
	}

	code, reason := getErrorCode(res)
	if code != 401 {
		return stun.Realm{}, stun.Nonce{}, false, fmt.Errorf("allocate error %d %s", code, reason)
	}

	var realm stun.Realm
	var nonce stun.Nonce
	if err := realm.GetFrom(res); err != nil {
		return stun.Realm{}, stun.Nonce{}, true, fmt.Errorf("allocate auth missing realm: %w", err)
	}
	if err := nonce.GetFrom(res); err != nil {
		return stun.Realm{}, stun.Nonce{}, true, fmt.Errorf("allocate auth missing nonce: %w", err)
	}

	username, password := turn.auth()
	for attempt := 0; attempt < 2; attempt++ {
		req2 := stun.New()
		req2.Type = stun.MessageType{Method: MethodAllocate, Class: stun.ClassRequest}
		req2.TransactionID = stun.NewTransactionID()
		req2.Add(AttrRequestedTransport, []byte{0x06, 0x00, 0x00, 0x00})
		if err := addAuthToMessage(req2, username, password, &realm, &nonce); err != nil {
			return realm, nonce, true, err
		}

		res2, err := doSTUN(conn, req2, cfg.Timeout)
		if err != nil {
			return realm, nonce, true, err
		}
		if res2.Type.Class == stun.ClassSuccessResponse {
			return realm, nonce, true, nil
		}
		stale, err := updateAuthFromError(res2, &realm, &nonce)
		if stale {
			if err != nil {
				return realm, nonce, true, err
			}
			if attempt == 0 {
				continue
			}
		}
		c, r := getErrorCode(res2)
		return realm, nonce, true, fmt.Errorf("allocate auth error %d %s", c, r)
	}

	return realm, nonce, true, nil
}

func dialTurnTCP(cfg Config, targetIP net.IP, targetPort int) (net.Conn, func(), string, error) {
	var errs []error
	candidates := cfg.TurnPool.candidates()
	if len(candidates) == 0 {
		return nil, nil, "", errors.New("no TURN server candidates")
	}
	for _, turn := range candidates {
		dataConn, release, err := dialTurnTCPWithServer(cfg, turn, targetIP, targetPort)
		if err == nil {
			cfg.TurnPool.markSuccess(turn)
			return dataConn, release, turn.Addr, nil
		}
		if isTurnServerFailure(err) {
			cfg.TurnPool.markFailure(turn, err)
			log.Printf("TURN TCP candidate failed via %s: %v", turn.Addr, err)
		} else {
			log.Printf("TURN TCP peer connect failed via %s without cooling: %v", turn.Addr, err)
			return nil, nil, "", err
		}
		errs = append(errs, fmt.Errorf("%s: %w", turn.Addr, err))
	}
	return nil, nil, "", errors.Join(errs...)
}

func dialTurnTCPWithServer(cfg Config, turn turnServerConfig, targetIP net.IP, targetPort int) (net.Conn, func(), error) {
	peer := tcpPeerKey(targetIP, targetPort)
	if cfg.TCPAllocs == nil {
		allocation, err := newTCPAllocation(cfg, turn)
		if err != nil {
			return nil, nil, err
		}
		dataConn, err := allocation.connect(targetIP, targetPort)
		if err != nil {
			allocation.close()
			return nil, nil, err
		}
		if !allocation.trackDataConn(dataConn) {
			allocation.close()
			return nil, nil, errors.New("TCP allocation is closed")
		}
		return dataConn, func() {
			allocation.untrackDataConn(dataConn)
			allocation.close()
		}, nil
	}

	allocation, err := cfg.TCPAllocs.getOrCreate(cfg, turn, peer)
	if err != nil {
		return nil, nil, err
	}
	dataConn, err := allocation.connect(targetIP, targetPort)
	if err != nil {
		allocation.finishConnect()
		cfg.TCPAllocs.release(turn, allocation, peer)
		if isTurnServerFailure(err) || isTimeoutError(err) {
			cfg.TCPAllocs.invalidate(turn, allocation)
		}
		return nil, nil, err
	}
	if !allocation.trackDataConn(dataConn) {
		allocation.finishConnect()
		cfg.TCPAllocs.release(turn, allocation, peer)
		return nil, nil, errors.New("TCP allocation is closed")
	}
	allocation.finishConnect()
	return dataConn, func() {
		allocation.untrackDataConn(dataConn)
		cfg.TCPAllocs.release(turn, allocation, peer)
	}, nil
}

func tcpPeerKey(ip net.IP, port int) string {
	return net.JoinHostPort(ip.String(), fmt.Sprintf("%d", port))
}

func newTCPAllocation(cfg Config, turn turnServerConfig) (*tcpAllocation, error) {
	username, password := turn.auth()
	ctrlConn, err := dialTCPKeepAlive(turn.Addr, shorterTimeout(cfg.Timeout, turnTCPDialTimeout))
	if err != nil {
		return nil, err
	}

	realm, nonce, needAuth, err := allocateTCP(ctrlConn, cfg, turn)
	if err != nil {
		ctrlConn.Close()
		return nil, err
	}

	a := &tcpAllocation{
		cfg:         cfg,
		turn:        turn,
		username:    username,
		password:    password,
		ctrlConn:    ctrlConn,
		serverAddr:  connectedTurnAddr(ctrlConn, turn.Addr),
		realm:       realm,
		nonce:       nonce,
		needAuth:    needAuth,
		stop:        make(chan struct{}),
		activePeers: make(map[string]struct{}),
		dataConns:   make(map[net.Conn]struct{}),
	}
	go a.refreshLoop()
	return a, nil
}

func (a *tcpAllocation) connect(targetIP net.IP, targetPort int) (net.Conn, error) {
	a.ctrlMu.Lock()
	connID, err := a.connectPeerLocked(targetIP, targetPort)
	a.ctrlMu.Unlock()
	if err != nil {
		return nil, err
	}

	dataConn, err := dialTCPKeepAlive(a.serverAddr, shorterTimeout(a.cfg.Timeout, turnTCPDialTimeout))
	if err != nil {
		return nil, err
	}

	if err := a.bindDataConn(dataConn, connID); err != nil {
		dataConn.Close()
		return nil, err
	}

	return dataConn, nil
}

func connectedTurnAddr(conn net.Conn, fallback string) string {
	if conn != nil && conn.RemoteAddr() != nil {
		return conn.RemoteAddr().String()
	}
	return fallback
}

func (a *tcpAllocation) connectPeerLocked(targetIP net.IP, targetPort int) ([]byte, error) {
	if a.closed.Load() {
		return nil, errors.New("TCP allocation is closed")
	}

	for attempt := 0; attempt < 2; attempt++ {
		connectReq := stun.New()
		connectReq.Type = stun.MessageType{Method: MethodConnect, Class: stun.ClassRequest}
		connectReq.TransactionID = stun.NewTransactionID()
		if err := addXORPeerAddress(connectReq, targetIP, targetPort); err != nil {
			return nil, err
		}
		if a.needAuth {
			if err := addAuthToMessage(connectReq, a.username, a.password, &a.realm, &a.nonce); err != nil {
				return nil, err
			}
		}

		connectRes, err := doSTUN(a.ctrlConn, connectReq, a.cfg.Timeout)
		if err != nil {
			if isTimeoutError(err) {
				return nil, turnPeerError(err)
			}
			return nil, err
		}

		stale, err := a.updateAuthFromErrorLocked(connectRes)
		if stale {
			if err != nil {
				return nil, err
			}
			if attempt == 0 {
				if a.cfg.LogVerbose {
					log.Printf("TURN TCP nonce refreshed via %s after stale CONNECT nonce", a.turn.Addr)
				}
				continue
			}
			return nil, fmt.Errorf("connect error %d Stale Nonce after nonce retry", staleNonceCode)
		}
		return getConnectionID(connectRes)
	}
	return nil, fmt.Errorf("connect error %d Stale Nonce", staleNonceCode)
}

func (a *tcpAllocation) updateAuthFromErrorLocked(res *stun.Message) (bool, error) {
	if !a.needAuth {
		return false, nil
	}
	return updateAuthFromError(res, &a.realm, &a.nonce)
}

func (a *tcpAllocation) bindDataConn(dataConn net.Conn, connID []byte) error {
	for attempt := 0; attempt < 2; attempt++ {
		bind := stun.New()
		bind.Type = stun.MessageType{Method: MethodConnectionBind, Class: stun.ClassRequest}
		bind.TransactionID = stun.NewTransactionID()
		bind.Add(AttrConnectionID, connID)
		if a.needAuth {
			a.ctrlMu.Lock()
			err := addAuthToMessage(bind, a.username, a.password, &a.realm, &a.nonce)
			a.ctrlMu.Unlock()
			if err != nil {
				return err
			}
		}

		bindRes, err := doSTUN(dataConn, bind, a.cfg.Timeout)
		if err != nil {
			return err
		}
		a.ctrlMu.Lock()
		stale, updateErr := a.updateAuthFromErrorLocked(bindRes)
		a.ctrlMu.Unlock()
		if stale {
			if updateErr != nil {
				return updateErr
			}
			if attempt == 0 {
				if a.cfg.LogVerbose {
					log.Printf("TURN TCP nonce refreshed via %s after stale ConnectionBind nonce", a.turn.Addr)
				}
				continue
			}
			return fmt.Errorf("connection-bind error %d Stale Nonce after nonce retry", staleNonceCode)
		}
		if bindRes.Type.Class != stun.ClassSuccessResponse {
			c, r := getErrorCode(bindRes)
			return fmt.Errorf("connection-bind error %d %s", c, r)
		}
		return nil
	}
	return fmt.Errorf("connection-bind error %d Stale Nonce", staleNonceCode)
}

func getConnectionID(res *stun.Message) ([]byte, error) {
	if res.Type.Class != stun.ClassSuccessResponse {
		c, r := getErrorCode(res)
		err := fmt.Errorf("connect error %d %s", c, strings.TrimRight(r, "\x00"))
		if isConnectPeerError(c) {
			return nil, turnPeerError(err)
		}
		return nil, err
	}
	connID, err := res.Get(AttrConnectionID)
	if err != nil || len(connID) == 0 {
		return nil, errors.New("missing CONNECTION-ID")
	}
	return connID, nil
}

func isConnectPeerError(code int) bool {
	// These responses do not show that the TURN server itself is unhealthy.
	return code == 403 || code == 446 || code == 447
}

func (a *tcpAllocation) isClosed() bool {
	return a.closed.Load()
}

func (a *tcpAllocation) tryReservePeer(peer string) bool {
	if a.isClosed() {
		return false
	}
	a.peerMu.Lock()
	defer a.peerMu.Unlock()
	if a.isClosed() {
		return false
	}
	if a.connecting > 0 {
		return false
	}
	if _, ok := a.activePeers[peer]; ok {
		return false
	}
	a.activePeers[peer] = struct{}{}
	a.connecting++
	return true
}

func (a *tcpAllocation) finishConnect() {
	a.peerMu.Lock()
	if a.connecting > 0 {
		a.connecting--
	}
	a.peerMu.Unlock()
}

func (a *tcpAllocation) releasePeer(peer string) {
	a.peerMu.Lock()
	delete(a.activePeers, peer)
	a.peerMu.Unlock()
}

func (a *tcpAllocation) hasActivePeers() bool {
	a.peerMu.Lock()
	defer a.peerMu.Unlock()
	return len(a.activePeers) > 0
}

func (a *tcpAllocation) trackDataConn(conn net.Conn) bool {
	a.dataMu.Lock()
	if a.closeData || a.closed.Load() {
		a.dataMu.Unlock()
		_ = conn.Close()
		return false
	}
	a.dataConns[conn] = struct{}{}
	a.dataMu.Unlock()
	return true
}

func (a *tcpAllocation) untrackDataConn(conn net.Conn) {
	a.dataMu.Lock()
	delete(a.dataConns, conn)
	a.dataMu.Unlock()
}

func (a *tcpAllocation) closeTrackedDataConns() {
	var conns []net.Conn
	a.dataMu.Lock()
	a.closeData = true
	for conn := range a.dataConns {
		conns = append(conns, conn)
		delete(a.dataConns, conn)
	}
	a.dataMu.Unlock()

	for _, conn := range conns {
		_ = conn.Close()
	}
}

func (a *tcpAllocation) close() {
	a.closeOnce.Do(func() {
		a.closed.Store(true)
		close(a.stop)
		_ = a.ctrlConn.Close()
	})
}

func (a *tcpAllocation) refreshLoop() {
	ticker := time.NewTicker(allocationRefreshEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := a.refresh(); err != nil {
				select {
				case <-time.After(refreshRetryDelay):
				case <-a.stop:
					return
				}
				if retryErr := a.refresh(); retryErr != nil {
					a.cfg.TurnPool.markFailure(a.turn, retryErr)
					log.Printf("TCP allocation refresh failed via %s after retry: %v", a.turn.Addr, errors.Join(err, retryErr))
					a.close()
					a.closeTrackedDataConns()
					return
				}
				if a.cfg.LogVerbose {
					log.Printf("TCP allocation refresh recovered via %s after retry: %v", a.turn.Addr, err)
				}
			}
		case <-a.stop:
			return
		}
	}
}

func (a *tcpAllocation) refresh() error {
	a.ctrlMu.Lock()
	defer a.ctrlMu.Unlock()
	if a.closed.Load() {
		return errors.New("TCP allocation is closed")
	}
	return refreshAllocation(a.ctrlConn, a.cfg, a.username, a.password, &a.realm, &a.nonce, a.needAuth)
}

func refreshAllocation(conn net.Conn, cfg Config, username string, password string, realm *stun.Realm, nonce *stun.Nonce, needAuth bool) error {
	for attempt := 0; attempt < 2; attempt++ {
		req := stun.New()
		req.Type = stun.MessageType{Method: MethodRefresh, Class: stun.ClassRequest}
		req.TransactionID = stun.NewTransactionID()
		addLifetime(req, allocationLifetime)
		if needAuth {
			if err := addAuthToMessage(req, username, password, realm, nonce); err != nil {
				return err
			}
		}

		res, err := doSTUN(conn, req, cfg.Timeout)
		if err != nil {
			return err
		}
		if res.Type.Class == stun.ClassSuccessResponse {
			return nil
		}
		stale, err := updateAuthFromError(res, realm, nonce)
		if stale {
			if err != nil {
				return err
			}
			if attempt == 0 {
				continue
			}
		}
		code, reason := getErrorCode(res)
		return fmt.Errorf("refresh error %d %s", code, reason)
	}
	return fmt.Errorf("refresh error %d Stale Nonce", staleNonceCode)
}
