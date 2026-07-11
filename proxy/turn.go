package proxy

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/pion/stun/v3"
)

const (
	MethodAllocate         stun.Method = 0x0003
	MethodRefresh          stun.Method = 0x0004
	MethodCreatePermission stun.Method = 0x0008
	MethodConnect          stun.Method = 0x000a
	MethodConnectionBind   stun.Method = 0x000b
	MethodSend             stun.Method = 0x0006
	MethodData             stun.Method = 0x0007

	AttrRequestedTransport stun.AttrType = 0x0019
	AttrLifetime           stun.AttrType = 0x000d
	AttrConnectionID       stun.AttrType = 0x002a
	AttrXORPeerAddress     stun.AttrType = 0x0012
	AttrData               stun.AttrType = 0x0013

	stunMagicCookie        uint32 = 0x2112A442
	staleNonceCode                = 438
	maxSTUNMessageLength          = 64 * 1024
	allocationLifetime            = 10 * time.Minute
	allocationRefreshEvery        = 5 * time.Minute
	turnUDPAttemptTimeout         = time.Second
	turnUDPRetryRTO               = 300 * time.Millisecond
	turnTCPDialTimeout            = 3 * time.Second
	tcpKeepAlivePeriod            = 30 * time.Second
	udpSocketBufferSize           = 512 << 10
	proxyCopyBufferSize           = 32 << 10
	refreshRetryDelay             = time.Second
	turnReleaseTimeout            = 500 * time.Millisecond
	turnConfigPollInterval        = 2 * time.Second
)

var sendIndicationMessageType = stun.MessageType{Method: MethodSend, Class: stun.ClassIndication}.Value()
var dataIndicationMessageType = stun.MessageType{Method: MethodData, Class: stun.ClassIndication}.Value()
var errTURNRequestTimeout = errors.New("TURN request timeout")

var proxyCopyBufferPool = sync.Pool{
	New: func() any {
		buf := make([]byte, proxyCopyBufferSize)
		return &buf
	},
}

type turnAttemptError struct {
	err           error
	serverFailure bool
}

func (e *turnAttemptError) Error() string {
	return e.err.Error()
}

func (e *turnAttemptError) Unwrap() error {
	return e.err
}

func isTurnServerFailure(err error) bool {
	var attemptErr *turnAttemptError
	if errors.As(err, &attemptErr) {
		return attemptErr.serverFailure
	}
	return true
}

func turnPeerError(err error) error {
	return &turnAttemptError{err: err, serverFailure: false}
}

func isTimeoutError(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func addAuthToMessage(m *stun.Message, username, password string, realm *stun.Realm, nonce *stun.Nonce) error {
	if username == "" || password == "" {
		return errors.New("TURN authentication required but username or password is empty")
	}
	if realm == nil || nonce == nil || realm.String() == "" {
		return nil
	}
	stun.Username(username).AddTo(m)
	realm.AddTo(m)
	nonce.AddTo(m)
	m.WriteHeader()
	return stun.NewLongTermIntegrity(username, realm.String(), password).AddTo(m)
}

func readSTUNMessage(conn net.Conn) (*stun.Message, error) {
	h := make([]byte, 20)
	if _, err := io.ReadFull(conn, h); err != nil {
		return nil, err
	}
	if h[0]&0xC0 != 0 {
		return nil, errors.New("invalid STUN message type")
	}
	if binary.BigEndian.Uint32(h[4:8]) != stunMagicCookie {
		return nil, errors.New("invalid STUN magic cookie")
	}
	length := int(binary.BigEndian.Uint16(h[2:4]))
	if length%4 != 0 {
		return nil, fmt.Errorf("invalid STUN length %d", length)
	}
	if length > maxSTUNMessageLength {
		return nil, fmt.Errorf("STUN message too large: %d", length)
	}
	raw := make([]byte, 20+length)
	copy(raw, h)
	if length > 0 {
		if _, err := io.ReadFull(conn, raw[20:]); err != nil {
			return nil, err
		}
	}

	m := stun.New()
	m.Raw = raw
	if err := m.Decode(); err != nil {
		return nil, err
	}
	return m, nil
}

func decodeSTUNMessage(raw []byte) (*stun.Message, error) {
	if len(raw) < 20 {
		return nil, errors.New("short STUN message")
	}
	if raw[0]&0xC0 != 0 {
		return nil, errors.New("invalid STUN message type")
	}
	if binary.BigEndian.Uint32(raw[4:8]) != stunMagicCookie {
		return nil, errors.New("invalid STUN magic cookie")
	}
	length := int(binary.BigEndian.Uint16(raw[2:4]))
	if length%4 != 0 {
		return nil, fmt.Errorf("invalid STUN length %d", length)
	}
	if length > maxSTUNMessageLength {
		return nil, fmt.Errorf("STUN message too large: %d", length)
	}
	total := 20 + length
	if len(raw) < total {
		return nil, errors.New("truncated STUN message")
	}

	m := stun.New()
	m.Raw = append([]byte(nil), raw[:total]...)
	if err := m.Decode(); err != nil {
		return nil, err
	}
	return m, nil
}

func writeSTUNMessage(conn net.Conn, m *stun.Message) error {
	m.WriteHeader()
	return writeAll(conn, m.Raw)
}

func dialTCPKeepAlive(addr string, timeout time.Duration) (net.Conn, error) {
	dialer := net.Dialer{Timeout: timeout, KeepAlive: tcpKeepAlivePeriod}
	conn, err := dialer.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		_ = tcpConn.SetKeepAlive(true)
		_ = tcpConn.SetKeepAlivePeriod(tcpKeepAlivePeriod)
	}
	return conn, nil
}

func doSTUN(conn net.Conn, m *stun.Message, timeout time.Duration) (*stun.Message, error) {
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}
	defer conn.SetDeadline(time.Time{})

	if err := writeSTUNMessage(conn, m); err != nil {
		return nil, err
	}
	for {
		res, err := readSTUNMessage(conn)
		if err != nil {
			return nil, err
		}
		if res.TransactionID != m.TransactionID || res.Type.Method != m.Type.Method {
			continue
		}
		if err := validateSTUNResponse(m, res); err != nil {
			return nil, err
		}
		return res, nil
	}
}

func validateSTUNResponse(req *stun.Message, res *stun.Message) error {
	if req == nil || res == nil {
		return errors.New("nil STUN transaction message")
	}
	if res.TransactionID != req.TransactionID {
		return errors.New("STUN response transaction ID mismatch")
	}
	if res.Type.Method != req.Type.Method {
		return fmt.Errorf("STUN response method %v does not match request %v", res.Type.Method, req.Type.Method)
	}
	if res.Type.Class != stun.ClassSuccessResponse && res.Type.Class != stun.ClassErrorResponse {
		return fmt.Errorf("unexpected STUN response class %v", res.Type.Class)
	}
	return nil
}

func validateLongTermIntegrity(res *stun.Message, username string, password string, realm *stun.Realm) error {
	if realm == nil || realm.String() == "" {
		return errors.New("cannot validate TURN response without realm")
	}
	if err := stun.NewLongTermIntegrity(username, realm.String(), password).Check(res); err != nil {
		return fmt.Errorf("TURN response MESSAGE-INTEGRITY check failed: %w", err)
	}
	return nil
}

func getErrorCode(m *stun.Message) (int, string) {
	var code stun.ErrorCodeAttribute
	if err := code.GetFrom(m); err != nil {
		return 0, ""
	}
	return int(code.Code), string(code.Reason)
}

func updateAuthFromError(m *stun.Message, realm *stun.Realm, nonce *stun.Nonce) (bool, error) {
	if m == nil || m.Type.Class != stun.ClassErrorResponse {
		return false, nil
	}
	code, _ := getErrorCode(m)
	if code != staleNonceCode {
		return false, nil
	}
	if realm == nil || nonce == nil {
		return true, errors.New("stale nonce response cannot update empty auth state")
	}

	var newNonce stun.Nonce
	if err := newNonce.GetFrom(m); err != nil {
		return true, fmt.Errorf("stale nonce response missing nonce: %w", err)
	}
	var newRealm stun.Realm
	if err := newRealm.GetFrom(m); err == nil && newRealm.String() != "" {
		*realm = newRealm
	}
	*nonce = newNonce
	return true, nil
}

func addXORPeerAddress(m *stun.Message, ip net.IP, port int) error {
	if port < 0 || port > 65535 {
		return fmt.Errorf("invalid port %d", port)
	}

	ip4 := ip.To4()
	if ip4 == nil {
		return errors.New("only IPv4 is supported")
	}

	magicPort := uint16(stunMagicCookie >> 16)
	xPort := uint16(port) ^ magicPort

	var v [8]byte
	v[0] = 0
	v[1] = 1
	binary.BigEndian.PutUint16(v[2:4], xPort)
	v[4] = ip4[0] ^ byte(stunMagicCookie>>24)
	v[5] = ip4[1] ^ byte(stunMagicCookie>>16&0xff)
	v[6] = ip4[2] ^ byte(stunMagicCookie>>8&0xff)
	v[7] = ip4[3] ^ byte(stunMagicCookie&0xff)

	m.Add(AttrXORPeerAddress, v[:])
	return nil
}

func addLifetime(m *stun.Message, d time.Duration) {
	seconds := uint32(d / time.Second)
	v := make([]byte, 4)
	binary.BigEndian.PutUint32(v, seconds)
	m.Add(AttrLifetime, v)
}

func decodeXORPeerAddress(v []byte) (net.IP, int, error) {
	if len(v) != 8 || v[1] != 1 {
		return nil, 0, errors.New("invalid XOR-PEER-ADDRESS")
	}

	magicPort := uint16(stunMagicCookie >> 16)
	xPort := binary.BigEndian.Uint16(v[2:4])
	port := int(xPort ^ magicPort)

	ip := make(net.IP, 4)
	ip[0] = v[4] ^ byte(stunMagicCookie>>24)
	ip[1] = v[5] ^ byte(stunMagicCookie>>16&0xff)
	ip[2] = v[6] ^ byte(stunMagicCookie>>8&0xff)
	ip[3] = v[7] ^ byte(stunMagicCookie&0xff)
	return ip, port, nil
}
