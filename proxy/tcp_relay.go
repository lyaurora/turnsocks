package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/stun/v4"
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
	retired     atomic.Bool
	closeOnce   sync.Once
	peerMu      sync.Mutex
	activePeers map[string]struct{}
	connecting  int
	dataMu      sync.Mutex
	dataConns   map[net.Conn]struct{}
	closeData   bool
}

func allocateTCP(ctx *setupContext, conn net.Conn, cfg Config, turn turnServerConfig) (stun.Realm, stun.Nonce, bool, error) {
	req := stun.New()
	req.Type = stun.MessageType{Method: MethodAllocate, Class: stun.ClassRequest}
	req.TransactionID = stun.NewTransactionID()
	req.Add(AttrRequestedTransport, []byte{0x06, 0x00, 0x00, 0x00})

	res, err := doSTUN(ctx, conn, req, cfg.Timeout)
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

		res2, err := doSTUN(ctx, conn, req2, cfg.Timeout)
		if err != nil {
			return realm, nonce, true, err
		}
		if err := validateLongTermIntegrity(res2, username, password, &realm); err != nil {
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

	return realm, nonce, true, errors.New("allocate authentication retry exhausted")
}

func dialTurnTCP(ctx *setupContext, cfg Config, targetIP net.IP, targetPort int) (net.Conn, func(), string, error) {
	type result struct {
		conn    net.Conn
		release func()
		addr    string
		err     error
	}
	ctx.stage.Store("TURN 节点选择")
	results := make(chan result)
	// ponytail: one setup worker may finish an in-flight shared control transaction
	// under its existing timeout after this caller leaves, preserving other peers.
	go func() {
		conn, release, addr, err := dialTurnTCPCandidates(ctx, cfg, targetIP, targetPort)
		select {
		case results <- result{conn, release, addr, err}:
		case <-ctx.Done():
			if conn != nil {
				_ = conn.Close()
				release()
			}
		}
	}()
	select {
	case <-ctx.Done():
		return nil, nil, "", ctx.err()
	case r := <-results:
		if err := ctx.err(); err != nil {
			if r.conn != nil {
				_ = r.conn.Close()
				r.release()
			}
			return nil, nil, "", err
		}
		return r.conn, r.release, r.addr, r.err
	}
}

func dialTurnTCPCandidates(ctx *setupContext, cfg Config, targetIP net.IP, targetPort int) (net.Conn, func(), string, error) {
	var errs []error
	candidates := cfg.TurnPool.candidates()
	if len(candidates) == 0 {
		return nil, nil, "", errors.New("no TURN server candidates")
	}
	for _, turn := range candidates {
		if err := ctx.err(); err != nil {
			return nil, nil, "", err
		}
		dataConn, release, err := dialTurnTCPWithServer(ctx, cfg, turn, targetIP, targetPort)
		if err == nil {
			if err := ctx.err(); err != nil {
				_ = dataConn.Close()
				release()
				return nil, nil, "", err
			}
			cfg.TurnPool.markSuccess(turn)
			return dataConn, release, turn.Addr, nil
		}
		if ctxErr := ctx.err(); ctxErr != nil {
			return nil, nil, "", ctxErr
		}
		if isTurnServerFailure(err) {
			cfg.TurnPool.markFailure(turn, err)
			log.Printf("TURN TCP candidate failed via %s: %v", turn.Addr, err)
		} else {
			cfg.TurnPool.recordFailure(turn.Addr, "TCP 目标连接", err)
			log.Printf("TURN TCP peer connect failed via %s without cooling: %v", turn.Addr, err)
			return nil, nil, "", err
		}
		errs = append(errs, fmt.Errorf("%s: %w", turn.Addr, err))
	}
	return nil, nil, "", errors.Join(errs...)
}

func dialTurnTCPWithServer(ctx *setupContext, cfg Config, turn turnServerConfig, targetIP net.IP, targetPort int) (net.Conn, func(), error) {
	peer := tcpPeerKey(targetIP, targetPort)
	for attempt := 0; ; attempt++ {
		if err := ctx.err(); err != nil {
			return nil, nil, err
		}
		ctx.stage.Store("等待 TURN 会话（" + turn.Addr + "）")
		allocation, reused, err := cfg.TCPAllocs.getOrCreate(ctx, cfg, turn, peer)
		if err != nil {
			return nil, nil, err
		}
		dataConn, err := allocation.connect(ctx, targetIP, targetPort)
		if err == nil && !allocation.trackDataConn(dataConn) {
			err = net.ErrClosed
		}
		if err == nil {
			allocation.finishConnect()
			return dataConn, func() {
				allocation.untrackDataConn(dataConn)
				cfg.TCPAllocs.release(turn, allocation, peer)
			}, nil
		}
		// Remove the failed allocation before allowing another peer to reserve it.
		if ctx.err() != nil {
			cfg.TCPAllocs.retire(turn, allocation)
		} else if isTurnServerFailure(err) {
			cfg.TCPAllocs.invalidate(turn, allocation)
		} else if isTimeoutError(err) {
			cfg.TCPAllocs.retire(turn, allocation)
		}
		allocation.finishConnect()
		cfg.TCPAllocs.release(turn, allocation, peer)
		// Retry a stale pooled transport once, before any application data is sent.
		if ctx.err() != nil || attempt != 0 || !reused || !isDisconnectedError(err) {
			return nil, nil, err
		}
	}
}

func isDisconnectedError(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr) && !opErr.Timeout() && (opErr.Op == "read" || opErr.Op == "write")
}

func tcpPeerKey(ip net.IP, port int) string {
	return net.JoinHostPort(ip.String(), fmt.Sprintf("%d", port))
}

func newTCPAllocation(ctx *setupContext, cfg Config, turn turnServerConfig) (*tcpAllocation, error) {
	username, password := turn.auth()
	ctx.stage.Store("TURN TCP 拨号（" + turn.Addr + "）")
	ctrlConn, err := dialTCPKeepAlive(ctx, turn.Addr, shorterTimeout(cfg.Timeout, turnTCPDialTimeout))
	if err != nil {
		return nil, err
	}

	ctx.stage.Store("TURN 认证与分配（" + turn.Addr + "）")
	realm, nonce, needAuth, err := allocateTCP(ctx, ctrlConn, cfg, turn)
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

func (a *tcpAllocation) connect(ctx *setupContext, targetIP net.IP, targetPort int) (net.Conn, error) {
	ctx.stage.Store("等待 TURN 控制连接（" + a.turn.Addr + "）")
	a.ctrlMu.Lock()
	connID, err := a.connectPeerLocked(ctx, targetIP, targetPort)
	a.ctrlMu.Unlock()
	if err != nil {
		return nil, err
	}

	if err := ctx.err(); err != nil {
		return nil, err
	}
	ctx.stage.Store("TURN 数据通道拨号（" + a.turn.Addr + "）")
	dataConn, err := dialTCPKeepAlive(ctx, a.serverAddr, shorterTimeout(a.cfg.Timeout, turnTCPDialTimeout))
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = dataConn.Close() })
	defer stop()

	ctx.stage.Store("TURN 数据通道绑定（" + a.turn.Addr + "）")
	if err := a.bindDataConn(ctx, dataConn, connID); err != nil {
		dataConn.Close()
		return nil, err
	}
	stop()
	if err := ctx.err(); err != nil {
		_ = dataConn.Close()
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

func (a *tcpAllocation) connectPeerLocked(ctx *setupContext, targetIP net.IP, targetPort int) ([]byte, error) {
	if a.closed.Load() {
		return nil, net.ErrClosed
	}

	for attempt := 0; attempt < 2; attempt++ {
		if err := ctx.err(); err != nil {
			return nil, err
		}
		ctx.stage.Store("TURN 目标连接（" + a.turn.Addr + "）")
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

		transactionCtx := context.Context(ctx)
		a.dataMu.Lock()
		shared := len(a.dataConns) > 0
		a.dataMu.Unlock()
		if shared {
			// Preserve framing and refreshes for established peers after this caller leaves.
			transactionCtx = context.Background()
		}
		connectRes, err := doSTUN(transactionCtx, a.ctrlConn, connectReq, a.cfg.Timeout)
		if err != nil {
			if isTimeoutError(err) {
				return nil, turnPeerError(err)
			}
			return nil, err
		}
		if a.needAuth {
			if err := validateLongTermIntegrity(connectRes, a.username, a.password, &a.realm); err != nil {
				return nil, err
			}
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

func (a *tcpAllocation) bindDataConn(ctx *setupContext, dataConn net.Conn, connID []byte) error {
	for attempt := 0; attempt < 2; attempt++ {
		if err := ctx.err(); err != nil {
			return err
		}
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

		bindRes, err := doSTUN(ctx, dataConn, bind, a.cfg.Timeout)
		if err != nil {
			return err
		}
		a.ctrlMu.Lock()
		if a.needAuth {
			if err := validateLongTermIntegrity(bindRes, a.username, a.password, &a.realm); err != nil {
				a.ctrlMu.Unlock()
				return err
			}
		}
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
	if a.isClosed() || a.retired.Load() {
		return false
	}
	a.peerMu.Lock()
	defer a.peerMu.Unlock()
	if a.isClosed() || a.retired.Load() {
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
	closeNow := a.retired.Load() && len(a.activePeers) == 0 && a.connecting == 0
	a.peerMu.Unlock()
	if closeNow {
		a.close()
	}
}

func (a *tcpAllocation) retire() {
	a.retired.Store(true)
	a.peerMu.Lock()
	closeNow := len(a.activePeers) == 0 && a.connecting == 0
	a.peerMu.Unlock()
	if closeNow {
		a.close()
	}
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
					a.cfg.TurnPool.markFailure(a.turn, fmt.Errorf("会话续期失败：%w", retryErr))
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

		res, err := doSTUN(context.Background(), conn, req, cfg.Timeout)
		if err != nil {
			return err
		}
		if needAuth {
			if err := validateLongTermIntegrity(res, username, password, realm); err != nil {
				return err
			}
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
