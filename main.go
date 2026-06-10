package main

import (
	"crypto/md5"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pion/stun"
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
	maxSTUNMessageLength          = 64 * 1024
	allocationLifetime            = 10 * time.Minute
	allocationRefreshEvery        = 5 * time.Minute
)

type Config struct {
	Listen       string
	Turns        string
	TurnServers  []turnServerConfig
	TurnPool     *turnPool
	TurnCooldown time.Duration
	ConfigPath   string
	DoH          string
	DNSTTL       time.Duration
	Timeout      time.Duration
	LogVerbose   bool
}

type turnServerState struct {
	Server      turnServerConfig
	FailedUntil time.Time
	LastError   string
}

type turnServerConfig struct {
	Addr         string
	Username     string
	Password     string
	ExplicitAuth bool
}

type turnPool struct {
	mu       sync.Mutex
	servers  []turnServerState
	next     int
	cooldown time.Duration
}

type proxyController struct {
	mu      sync.Mutex
	cfg     Config
	ln      net.Listener
	running bool
}

type DoHResponse struct {
	Status int `json:"Status"`
	Answer []struct {
		Type int    `json:"type"`
		Data string `json:"data"`
		TTL  int    `json:"TTL"`
	} `json:"Answer"`
}

type dnsEntry struct {
	IP       net.IP
	ExpireAt time.Time
}

var dnsCache sync.Map

func main() {
	cfg := Config{}

	configPath := defaultConfigPath()
	configPath = preFlagValue("config", getenv("CONFIG_PATH", configPath))
	configPath = absPath(configPath)
	if err := loadEnvFile(configPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Fatalf("load env config failed: %v", err)
	}

	flag.StringVar(&cfg.Listen, "listen", getenv("LISTEN", "127.0.0.1:1080"), "SOCKS5 listen address")
	flag.StringVar(&cfg.Turns, "turns", getenv("TURN_SERVERS", ""), "comma-separated TURN server addresses")
	flag.DurationVar(&cfg.TurnCooldown, "turn-cooldown", 30*time.Second, "TURN server failure cooldown")
	flag.StringVar(&cfg.ConfigPath, "config", configPath, "env config file path")
	flag.StringVar(&cfg.DoH, "doh", getenv("DOH", "https://cloudflare-dns.com/dns-query"), "DoH endpoint")
	flag.DurationVar(&cfg.DNSTTL, "dns-ttl", 300*time.Second, "DNS cache TTL")
	flag.DurationVar(&cfg.Timeout, "timeout", 20*time.Second, "network timeout")
	flag.BoolVar(&cfg.LogVerbose, "v", false, "verbose log")
	flag.Parse()

	if cfg.Timeout <= 0 {
		log.Fatal("timeout must be greater than 0")
	}
	if cfg.DNSTTL <= 0 {
		log.Fatal("dns-ttl must be greater than 0")
	}
	if cfg.TurnCooldown <= 0 {
		log.Fatal("turn-cooldown must be greater than 0")
	}
	cfg.ConfigPath = absPath(cfg.ConfigPath)
	var err error
	cfg.TurnServers, err = loadTurnServers(cfg)
	if err != nil {
		log.Fatalf("load TURN config failed: %v", err)
	}
	if len(cfg.TurnServers) == 0 {
		log.Fatal("missing TURN servers, set TURN_SERVERS in config.env")
	}
	cfg.TurnPool = newTurnPool(cfg.TurnServers, cfg.TurnCooldown)
	dohURL, err := url.ParseRequestURI(cfg.DoH)
	if err != nil {
		log.Fatalf("invalid DoH endpoint: %v", err)
	}
	if dohURL.Scheme != "http" && dohURL.Scheme != "https" || dohURL.Host == "" {
		log.Fatal("DoH endpoint must be an http or https URL")
	}

	log.Printf("TURN servers: %s", strings.Join(turnServerAddrs(cfg.TurnServers), ", "))
	log.Printf("TURN auth: per-server inline only")
	proxy := newProxyController(cfg)
	if err := proxy.start(); err != nil {
		log.Fatalf("SOCKS5 start failed: %v", err)
	}
	go cleanupDNSCache(cfg.DNSTTL)

	select {}
}

func getenv(k, def string) string {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	return v
}

func defaultConfigPath() string {
	exe, err := os.Executable()
	if err == nil && exe != "" {
		return filepath.Join(filepath.Dir(exe), "config.env")
	}
	return "config.env"
}

func absPath(path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

func preFlagValue(name, def string) string {
	longPrefix := "--" + name + "="
	shortPrefix := "-" + name + "="
	for i, arg := range os.Args[1:] {
		if arg == "--"+name || arg == "-"+name {
			if i+2 < len(os.Args) {
				return os.Args[i+2]
			}
			return def
		}
		if strings.HasPrefix(arg, longPrefix) {
			return strings.TrimPrefix(arg, longPrefix)
		}
		if strings.HasPrefix(arg, shortPrefix) {
			return strings.TrimPrefix(arg, shortPrefix)
		}
	}
	return def
}

func loadEnvFile(path string) error {
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for lineNo, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("invalid env line %d", lineNo+1)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return fmt.Errorf("empty env key on line %d", lineNo+1)
		}
		if os.Getenv(key) != "" {
			continue
		}
		value = strings.TrimSpace(value)
		value = strings.Trim(value, "\"'")
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return nil
}

func newProxyController(cfg Config) *proxyController {
	return &proxyController{cfg: cfg}
}

func (p *proxyController) start() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.running {
		return nil
	}
	ln, err := net.Listen("tcp", p.cfg.Listen)
	if err != nil {
		return err
	}
	p.ln = ln
	p.running = true
	go p.acceptLoop(ln)
	log.Printf("SOCKS5 listening on %s", p.cfg.Listen)
	return nil
}

func (p *proxyController) acceptLoop(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			p.mu.Lock()
			current := p.ln == ln && p.running
			p.mu.Unlock()
			if current {
				log.Printf("accept failed: %v", err)
			}
			return
		}
		go handleSocksConn(c, p.cfg)
	}
}

func parseTurnServers(turns string) []turnServerConfig {
	seen := make(map[string]struct{})
	var servers []turnServerConfig
	add := func(raw string) {
		for _, part := range strings.Split(raw, ",") {
			server, err := parseTurnServerConfig(part)
			if err != nil || server.Addr == "" {
				continue
			}
			key := server.String()
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			servers = append(servers, server)
		}
	}

	add(turns)
	return servers
}

func loadTurnServers(cfg Config) ([]turnServerConfig, error) {
	return parseTurnServers(cfg.Turns), nil
}

func parseTurnServerConfig(raw string) (turnServerConfig, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return turnServerConfig{}, errors.New("TURN server address is empty")
	}

	server := turnServerConfig{}
	addr := raw
	if at := strings.LastIndex(raw, "@"); at >= 0 {
		cred := raw[:at]
		addr = raw[at+1:]
		user, pass, ok := strings.Cut(cred, ":")
		if !ok || user == "" {
			return turnServerConfig{}, errors.New("TURN auth format must be username:password@host:port")
		}
		server.Username = user
		server.Password = pass
		server.ExplicitAuth = true
	}

	addr = strings.TrimSpace(addr)
	if err := validateTurnAddr(addr); err != nil {
		return turnServerConfig{}, err
	}
	server.Addr = addr
	return server, nil
}

func (s turnServerConfig) String() string {
	if s.ExplicitAuth {
		return s.Username + ":" + s.Password + "@" + s.Addr
	}
	return s.Addr
}

func (s turnServerConfig) auth() (string, string) {
	return s.Username, s.Password
}

func turnServerAddrs(servers []turnServerConfig) []string {
	addrs := make([]string, 0, len(servers))
	for _, s := range servers {
		addrs = append(addrs, s.Addr)
	}
	return addrs
}

func newTurnPool(servers []turnServerConfig, cooldown time.Duration) *turnPool {
	p := &turnPool{cooldown: cooldown}
	for _, server := range servers {
		p.servers = append(p.servers, turnServerState{Server: server})
	}
	return p
}

func (p *turnPool) candidates() []turnServerConfig {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	var active []turnServerConfig
	var cooling []turnServerConfig
	for i := 0; i < len(p.servers); i++ {
		idx := (p.next + i) % len(p.servers)
		s := p.servers[idx]
		if now.Before(s.FailedUntil) {
			cooling = append(cooling, s.Server)
			continue
		}
		active = append(active, s.Server)
	}
	if len(active) > 0 {
		return active
	}

	// If every server is cooling down, allow a full pass anyway so recovery is fast.
	return cooling
}

func (p *turnPool) markSuccess(server turnServerConfig) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for i := range p.servers {
		if p.servers[i].Server.String() == server.String() {
			p.servers[i].FailedUntil = time.Time{}
			p.servers[i].LastError = ""
			p.next = i
			return
		}
	}
}

func (p *turnPool) markFailure(server turnServerConfig, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for i := range p.servers {
		if p.servers[i].Server.String() == server.String() {
			p.servers[i].FailedUntil = time.Now().Add(p.cooldown)
			p.servers[i].LastError = err.Error()
			return
		}
	}
}

func validateTurnAddr(addr string) error {
	if addr == "" {
		return errors.New("TURN server address is empty")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("TURN server must be host:port: %w", err)
	}
	if host == "" || port == "" {
		return errors.New("TURN server must include host and port")
	}
	return nil
}

func handleSocksConn(conn net.Conn, cfg Config) {
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(cfg.Timeout)); err != nil {
		return
	}

	if err := socksHandshake(conn); err != nil {
		if cfg.LogVerbose {
			log.Printf("SOCKS handshake failed: %v", err)
		}
		return
	}

	req, err := readSocksRequest(conn)
	if err != nil {
		if cfg.LogVerbose {
			log.Printf("SOCKS request failed: %v", err)
		}
		return
	}

	_ = conn.SetDeadline(time.Time{})

	switch req.Cmd {
	case 0x01:
		if req.Port == 0 {
			_ = writeSocksReply(conn, 0x01, "0.0.0.0", 0)
			return
		}
		handleTCPConnect(conn, cfg, req)
	case 0x03:
		handleUDPAssociate(conn, cfg)
	default:
		_ = writeSocksReply(conn, 0x07, "0.0.0.0", 0)
	}
}

type socksRequest struct {
	Cmd  byte
	Host string
	Port int
}

func socksHandshake(conn net.Conn) error {
	h := make([]byte, 2)
	if _, err := io.ReadFull(conn, h); err != nil {
		return err
	}
	if h[0] != 0x05 {
		return errors.New("not SOCKS5")
	}

	methods := make([]byte, int(h[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return err
	}

	supportsNoAuth := false
	for _, method := range methods {
		if method == 0x00 {
			supportsNoAuth = true
			break
		}
	}
	if !supportsNoAuth {
		_ = writeAll(conn, []byte{0x05, 0xff})
		return errors.New("SOCKS client does not support no-auth method")
	}

	return writeAll(conn, []byte{0x05, 0x00})
}

func readSocksRequest(conn net.Conn) (socksRequest, error) {
	var r socksRequest

	h := make([]byte, 4)
	if _, err := io.ReadFull(conn, h); err != nil {
		return r, err
	}
	if h[0] != 0x05 {
		return r, errors.New("invalid SOCKS version")
	}
	if h[2] != 0x00 {
		return r, errors.New("invalid SOCKS reserved byte")
	}

	r.Cmd = h[1]
	host, port, err := readSocksAddr(conn, h[3])
	if err != nil {
		return r, err
	}
	r.Host = host
	r.Port = port
	return r, nil
}

func readSocksAddr(conn net.Conn, atyp byte) (string, int, error) {
	var host string

	switch atyp {
	case 0x01:
		b := make([]byte, 4)
		if _, err := io.ReadFull(conn, b); err != nil {
			return "", 0, err
		}
		host = net.IP(b).String()

	case 0x03:
		l := make([]byte, 1)
		if _, err := io.ReadFull(conn, l); err != nil {
			return "", 0, err
		}
		if l[0] == 0 {
			return "", 0, errors.New("empty domain name")
		}
		b := make([]byte, int(l[0]))
		if _, err := io.ReadFull(conn, b); err != nil {
			return "", 0, err
		}
		host = string(b)

	case 0x04:
		b := make([]byte, 16)
		if _, err := io.ReadFull(conn, b); err != nil {
			return "", 0, err
		}
		host = net.IP(b).String()

	default:
		return "", 0, errors.New("unsupported ATYP")
	}

	p := make([]byte, 2)
	if _, err := io.ReadFull(conn, p); err != nil {
		return "", 0, err
	}
	port := int(binary.BigEndian.Uint16(p))
	return host, port, nil
}

func writeSocksReply(conn net.Conn, rep byte, bindHost string, bindPort int) error {
	ip := net.ParseIP(bindHost).To4()
	if ip == nil {
		ip = net.IPv4(0, 0, 0, 0)
	}
	b := []byte{0x05, rep, 0x00, 0x01, ip[0], ip[1], ip[2], ip[3], 0, 0}
	binary.BigEndian.PutUint16(b[8:10], uint16(bindPort))
	return writeAll(conn, b)
}

func writeAll(conn net.Conn, b []byte) error {
	for len(b) > 0 {
		n, err := conn.Write(b)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrUnexpectedEOF
		}
		b = b[n:]
	}
	return nil
}

func handleTCPConnect(client net.Conn, cfg Config, req socksRequest) {
	ip, err := resolveDoH(req.Host, cfg)
	if err != nil {
		log.Printf("resolve failed %s: %v", req.Host, err)
		_ = writeSocksReply(client, 0x04, "0.0.0.0", 0)
		return
	}

	if cfg.LogVerbose {
		log.Printf("TCP CONNECT %s:%d -> %s:%d", req.Host, req.Port, ip.String(), req.Port)
	}

	dataConn, ctrlConn, stopRefresh, turnAddr, err := dialTurnTCP(cfg, ip, req.Port)
	if err != nil {
		log.Printf("TURN TCP failed %s:%d: %v", ip.String(), req.Port, err)
		_ = writeSocksReply(client, 0x05, "0.0.0.0", 0)
		return
	}
	if cfg.LogVerbose {
		log.Printf("TURN TCP selected %s for %s:%d", turnAddr, req.Host, req.Port)
	}
	defer stopRefresh()
	defer dataConn.Close()
	defer ctrlConn.Close()

	if err := writeSocksReply(client, 0x00, "0.0.0.0", 0); err != nil {
		return
	}

	pipe(client, dataConn)
}

func pipe(a net.Conn, b net.Conn) {
	done := make(chan struct{}, 2)

	go func() {
		_, _ = io.Copy(a, b)
		_ = a.Close()
		_ = b.Close()
		done <- struct{}{}
	}()

	go func() {
		_, _ = io.Copy(b, a)
		_ = a.Close()
		_ = b.Close()
		done <- struct{}{}
	}()

	<-done
	<-done
}

func resolveDoH(host string, cfg Config) (net.IP, error) {
	ip := net.ParseIP(host)
	if ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			return ip4, nil
		}
		return nil, errors.New("IPv6 is not supported")
	}

	if v, ok := dnsCache.Load(host); ok {
		e := v.(dnsEntry)
		if time.Now().Before(e.ExpireAt) {
			return e.IP, nil
		}
		dnsCache.Delete(host)
	}

	u, err := buildDoHURL(cfg.DoH, host)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Accept", "application/dns-json")

	httpClient := &http.Client{Timeout: cfg.Timeout}
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("DoH HTTP status %s", resp.Status)
	}

	var dr DoHResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&dr); err != nil {
		return nil, err
	}
	if dr.Status != 0 {
		return nil, fmt.Errorf("DoH status %d", dr.Status)
	}

	for _, ans := range dr.Answer {
		if ans.Type == 1 {
			ip := net.ParseIP(ans.Data)
			if ip4 := ip.To4(); ip4 != nil {
				ttl := cfg.DNSTTL
				if ans.TTL > 0 {
					t := time.Duration(ans.TTL) * time.Second
					if t < ttl {
						ttl = t
					}
				}
				dnsCache.Store(host, dnsEntry{IP: ip4, ExpireAt: time.Now().Add(ttl)})
				return ip4, nil
			}
		}
	}

	return nil, fmt.Errorf("no A record for %s", host)
}

func cleanupDNSCache(interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	if interval > time.Minute {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		dnsCache.Range(func(key, value any) bool {
			entry, ok := value.(dnsEntry)
			if ok && now.After(entry.ExpireAt) {
				dnsCache.Delete(key)
			}
			return true
		})
	}
}

func buildDoHURL(endpoint, host string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("name", host)
	q.Set("type", "A")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func createKey(username, realm, password string) []byte {
	h := md5.New()
	_, _ = h.Write([]byte(username + ":" + realm + ":" + password))
	return h.Sum(nil)
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
	key := createKey(username, realm.String(), password)
	return stun.MessageIntegrity(key).AddTo(m)
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

func writeSTUNMessage(conn net.Conn, m *stun.Message) error {
	m.WriteHeader()
	return writeAll(conn, m.Raw)
}

func doSTUN(conn net.Conn, m *stun.Message, timeout time.Duration) (*stun.Message, error) {
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}
	defer conn.SetDeadline(time.Time{})

	if err := writeSTUNMessage(conn, m); err != nil {
		return nil, err
	}
	return readSTUNMessage(conn)
}

func getErrorCode(m *stun.Message) (int, string) {
	var code stun.ErrorCodeAttribute
	if err := code.GetFrom(m); err != nil {
		return 0, ""
	}
	return int(code.Code), string(code.Reason)
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

	cookie := make([]byte, 4)
	binary.BigEndian.PutUint32(cookie, stunMagicCookie)

	xIP := make([]byte, 4)
	for i := 0; i < 4; i++ {
		xIP[i] = ip4[i] ^ cookie[i]
	}

	v := make([]byte, 8)
	v[0] = 0
	v[1] = 1
	binary.BigEndian.PutUint16(v[2:4], xPort)
	copy(v[4:8], xIP)

	m.Add(AttrXORPeerAddress, v)
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

	cookie := make([]byte, 4)
	binary.BigEndian.PutUint32(cookie, stunMagicCookie)

	ip := make(net.IP, 4)
	for i := 0; i < 4; i++ {
		ip[i] = v[4+i] ^ cookie[i]
	}
	return ip, port, nil
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

	req2 := stun.New()
	req2.Type = stun.MessageType{Method: MethodAllocate, Class: stun.ClassRequest}
	req2.TransactionID = stun.NewTransactionID()
	req2.Add(AttrRequestedTransport, []byte{0x06, 0x00, 0x00, 0x00})
	username, password := turn.auth()
	if err := addAuthToMessage(req2, username, password, &realm, &nonce); err != nil {
		return realm, nonce, true, err
	}

	res2, err := doSTUN(conn, req2, cfg.Timeout)
	if err != nil {
		return realm, nonce, true, err
	}
	if res2.Type.Class != stun.ClassSuccessResponse {
		c, r := getErrorCode(res2)
		return realm, nonce, true, fmt.Errorf("allocate auth error %d %s", c, r)
	}

	return realm, nonce, true, nil
}

func dialTurnTCP(cfg Config, targetIP net.IP, targetPort int) (net.Conn, net.Conn, func(), string, error) {
	var errs []error
	candidates := cfg.TurnPool.candidates()
	if len(candidates) == 0 {
		return nil, nil, nil, "", errors.New("no TURN server candidates")
	}
	for _, turn := range candidates {
		dataConn, ctrlConn, stopRefresh, err := dialTurnTCPWithServer(cfg, turn, targetIP, targetPort)
		if err == nil {
			cfg.TurnPool.markSuccess(turn)
			return dataConn, ctrlConn, stopRefresh, turn.Addr, nil
		}
		cfg.TurnPool.markFailure(turn, err)
		errs = append(errs, fmt.Errorf("%s: %w", turn.Addr, err))
	}
	return nil, nil, nil, "", errors.Join(errs...)
}

func dialTurnTCPWithServer(cfg Config, turn turnServerConfig, targetIP net.IP, targetPort int) (net.Conn, net.Conn, func(), error) {
	username, password := turn.auth()
	ctrlConn, err := net.DialTimeout("tcp", turn.Addr, cfg.Timeout)
	if err != nil {
		return nil, nil, nil, err
	}

	realm, nonce, needAuth, err := allocateTCP(ctrlConn, cfg, turn)
	if err != nil {
		ctrlConn.Close()
		return nil, nil, nil, err
	}

	req := stun.New()
	req.Type = stun.MessageType{Method: MethodConnect, Class: stun.ClassRequest}
	req.TransactionID = stun.NewTransactionID()
	if err := addXORPeerAddress(req, targetIP, targetPort); err != nil {
		ctrlConn.Close()
		return nil, nil, nil, err
	}
	if needAuth {
		if err := addAuthToMessage(req, username, password, &realm, &nonce); err != nil {
			ctrlConn.Close()
			return nil, nil, nil, err
		}
	}

	res, err := doSTUN(ctrlConn, req, cfg.Timeout)
	if err != nil {
		ctrlConn.Close()
		return nil, nil, nil, err
	}
	if res.Type.Class != stun.ClassSuccessResponse {
		c, r := getErrorCode(res)
		ctrlConn.Close()
		return nil, nil, nil, fmt.Errorf("connect error %d %s", c, r)
	}

	connID, err := res.Get(AttrConnectionID)
	if err != nil || len(connID) == 0 {
		ctrlConn.Close()
		return nil, nil, nil, errors.New("missing CONNECTION-ID")
	}

	dataConn, err := net.DialTimeout("tcp", turn.Addr, cfg.Timeout)
	if err != nil {
		ctrlConn.Close()
		return nil, nil, nil, err
	}

	bind := stun.New()
	bind.Type = stun.MessageType{Method: MethodConnectionBind, Class: stun.ClassRequest}
	bind.TransactionID = stun.NewTransactionID()
	bind.Add(AttrConnectionID, connID)
	if needAuth {
		if err := addAuthToMessage(bind, username, password, &realm, &nonce); err != nil {
			dataConn.Close()
			ctrlConn.Close()
			return nil, nil, nil, err
		}
	}

	bindRes, err := doSTUN(dataConn, bind, cfg.Timeout)
	if err != nil {
		dataConn.Close()
		ctrlConn.Close()
		return nil, nil, nil, err
	}
	if bindRes.Type.Class != stun.ClassSuccessResponse {
		c, r := getErrorCode(bindRes)
		dataConn.Close()
		ctrlConn.Close()
		return nil, nil, nil, fmt.Errorf("connection-bind error %d %s", c, r)
	}

	stopRefresh := make(chan struct{})
	stopOnce := sync.Once{}
	go refreshTCPAllocation(ctrlConn, dataConn, cfg, turn, username, password, realm, nonce, needAuth, stopRefresh)

	return dataConn, ctrlConn, func() { stopOnce.Do(func() { close(stopRefresh) }) }, nil
}

func refreshTCPAllocation(ctrlConn net.Conn, dataConn net.Conn, cfg Config, turn turnServerConfig, username string, password string, realm stun.Realm, nonce stun.Nonce, needAuth bool, stop <-chan struct{}) {
	ticker := time.NewTicker(allocationRefreshEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := refreshAllocation(ctrlConn, cfg, username, password, realm, nonce, needAuth); err != nil {
				cfg.TurnPool.markFailure(turn, err)
				log.Printf("TCP allocation refresh failed via %s: %v", turn.Addr, err)
				_ = dataConn.Close()
				_ = ctrlConn.Close()
				return
			}
		case <-stop:
			return
		}
	}
}

func refreshAllocation(conn net.Conn, cfg Config, username string, password string, realm stun.Realm, nonce stun.Nonce, needAuth bool) error {
	req := stun.New()
	req.Type = stun.MessageType{Method: MethodRefresh, Class: stun.ClassRequest}
	req.TransactionID = stun.NewTransactionID()
	addLifetime(req, allocationLifetime)
	if needAuth {
		if err := addAuthToMessage(req, username, password, &realm, &nonce); err != nil {
			return err
		}
	}

	res, err := doSTUN(conn, req, cfg.Timeout)
	if err != nil {
		return err
	}
	if res.Type.Class != stun.ClassSuccessResponse {
		code, reason := getErrorCode(res)
		return fmt.Errorf("refresh error %d %s", code, reason)
	}
	return nil
}

type udpSession struct {
	cfg          Config
	turn         turnServerConfig
	username     string
	password     string
	clientTCP    net.Conn
	localUDP     *net.UDPConn
	turnConn     net.Conn
	realm        stun.Realm
	nonce        stun.Nonce
	needAuth     bool
	writeMu      sync.Mutex
	pendingMu    sync.Mutex
	pending      map[string]chan *stun.Message
	permissions  map[string]time.Time
	permissionMu sync.Mutex
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
		turnConn, err := net.DialTimeout("tcp", turn.Addr, cfg.Timeout)
		if err != nil {
			cfg.TurnPool.markFailure(turn, err)
			errs = append(errs, fmt.Errorf("%s: %w", turn.Addr, err))
			continue
		}
		username, password := turn.auth()

		s := &udpSession{
			cfg:         cfg,
			turn:        turn,
			username:    username,
			password:    password,
			clientTCP:   clientTCP,
			localUDP:    localUDP,
			turnConn:    turnConn,
			pending:     make(map[string]chan *stun.Message),
			permissions: make(map[string]time.Time),
			closed:      make(chan struct{}),
		}
		if err := s.allocate(); err != nil {
			cfg.TurnPool.markFailure(turn, err)
			_ = turnConn.Close()
			errs = append(errs, fmt.Errorf("%s: %w", turn.Addr, err))
			continue
		}
		cfg.TurnPool.markSuccess(turn)
		return s, turn.Addr, nil
	}
	return nil, "", errors.Join(errs...)
}

func (s *udpSession) close() {
	s.closeOnce.Do(func() {
		close(s.closed)
		_ = s.localUDP.Close()
		_ = s.turnConn.Close()
	})
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

	s.writeMu.Lock()
	_ = s.turnConn.SetWriteDeadline(time.Now().Add(timeout))
	err := writeSTUNMessage(s.turnConn, req)
	_ = s.turnConn.SetWriteDeadline(time.Time{})
	s.writeMu.Unlock()
	if err != nil {
		return nil, err
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case res := <-ch:
		return res, nil
	case <-timer.C:
		return nil, errors.New("TURN request timeout")
	case <-s.closed:
		return nil, errors.New("session closed")
	}
}

func (s *udpSession) allocate() error {
	req := stun.New()
	req.Type = stun.MessageType{Method: MethodAllocate, Class: stun.ClassRequest}
	req.TransactionID = stun.NewTransactionID()
	req.Add(AttrRequestedTransport, []byte{0x11, 0x00, 0x00, 0x00})

	// Allocation response is read synchronously here before readTurnLoop starts.
	if err := s.turnConn.SetDeadline(time.Now().Add(s.cfg.Timeout)); err != nil {
		return err
	}
	defer s.turnConn.SetDeadline(time.Time{})

	if err := writeSTUNMessage(s.turnConn, req); err != nil {
		return err
	}
	res, err := readSTUNMessage(s.turnConn)
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

	req2 := stun.New()
	req2.Type = stun.MessageType{Method: MethodAllocate, Class: stun.ClassRequest}
	req2.TransactionID = stun.NewTransactionID()
	req2.Add(AttrRequestedTransport, []byte{0x11, 0x00, 0x00, 0x00})
	if err := addAuthToMessage(req2, s.username, s.password, &s.realm, &s.nonce); err != nil {
		return err
	}

	if err := writeSTUNMessage(s.turnConn, req2); err != nil {
		return err
	}
	res2, err := readSTUNMessage(s.turnConn)
	if err != nil {
		return err
	}
	if res2.Type.Class != stun.ClassSuccessResponse {
		c, r := getErrorCode(res2)
		return fmt.Errorf("UDP allocate auth error %d %s", c, r)
	}
	return nil
}

func (s *udpSession) refreshLoop() {
	ticker := time.NewTicker(allocationRefreshEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := s.refreshAllocation(); err != nil {
				s.cfg.TurnPool.markFailure(s.turn, err)
				log.Printf("UDP allocation refresh failed via %s: %v", s.turn.Addr, err)
				s.close()
				return
			}
		case <-s.closed:
			return
		}
	}
}

func (s *udpSession) refreshAllocation() error {
	req := stun.New()
	req.Type = stun.MessageType{Method: MethodRefresh, Class: stun.ClassRequest}
	req.TransactionID = stun.NewTransactionID()
	addLifetime(req, allocationLifetime)
	if s.needAuth {
		if err := addAuthToMessage(req, s.username, s.password, &s.realm, &s.nonce); err != nil {
			return err
		}
	}

	res, err := s.request(req, s.cfg.Timeout)
	if err != nil {
		return err
	}
	if res.Type.Class != stun.ClassSuccessResponse {
		code, reason := getErrorCode(res)
		return fmt.Errorf("refresh error %d %s", code, reason)
	}
	return nil
}

func (s *udpSession) readTurnLoop() {
	for {
		m, err := readSTUNMessage(s.turnConn)
		if err != nil {
			s.close()
			return
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

	pkt := buildSocksUDPIPv4(ip, port, data)
	_, _ = s.localUDP.WriteToUDP(pkt, caddr)
}

func buildSocksUDPIPv4(ip net.IP, port int, payload []byte) []byte {
	ip4 := ip.To4()
	pkt := make([]byte, 10+len(payload))
	pkt[0] = 0
	pkt[1] = 0
	pkt[2] = 0
	pkt[3] = 0x01
	copy(pkt[4:8], ip4)
	binary.BigEndian.PutUint16(pkt[8:10], uint16(port))
	copy(pkt[10:], payload)
	return pkt
}

func (s *udpSession) readLocalUDPLoop() {
	buf := make([]byte, 65535)

	for {
		n, caddr, err := s.localUDP.ReadFromUDP(buf)
		if err != nil {
			s.close()
			return
		}

		s.clientAddrMu.Lock()
		s.clientAddr = caddr
		s.clientAddrMu.Unlock()

		host, port, payload, err := parseSocksUDPPacket(buf[:n])
		if err != nil {
			continue
		}

		ip, err := resolveDoH(host, s.cfg)
		if err != nil {
			if s.cfg.LogVerbose {
				log.Printf("UDP resolve failed %s: %v", host, err)
			}
			continue
		}

		if err := s.ensurePermission(ip); err != nil {
			if s.cfg.LogVerbose {
				log.Printf("CreatePermission failed %s: %v", ip.String(), err)
			}
			continue
		}

		msg := stun.New()
		msg.Type = stun.MessageType{Method: MethodSend, Class: stun.ClassIndication}
		msg.TransactionID = stun.NewTransactionID()
		if err := addXORPeerAddress(msg, ip, port); err != nil {
			continue
		}
		msg.Add(AttrData, payload)

		s.writeMu.Lock()
		_ = s.turnConn.SetWriteDeadline(time.Now().Add(s.cfg.Timeout))
		err = writeSTUNMessage(s.turnConn, msg)
		_ = s.turnConn.SetWriteDeadline(time.Time{})
		s.writeMu.Unlock()
		if err != nil {
			s.close()
			return
		}
	}
}

func parseSocksUDPPacket(pkt []byte) (string, int, []byte, error) {
	if len(pkt) < 4 {
		return "", 0, nil, errors.New("short UDP packet")
	}
	if pkt[0] != 0 || pkt[1] != 0 || pkt[2] != 0 {
		return "", 0, nil, errors.New("fragmented UDP is not supported")
	}

	atyp := pkt[3]
	switch atyp {
	case 0x01:
		if len(pkt) < 10 {
			return "", 0, nil, errors.New("short IPv4 packet")
		}
		host := net.IP(pkt[4:8]).String()
		port := int(binary.BigEndian.Uint16(pkt[8:10]))
		if port == 0 {
			return "", 0, nil, errors.New("invalid UDP port 0")
		}
		return host, port, pkt[10:], nil

	case 0x03:
		if len(pkt) < 5 {
			return "", 0, nil, errors.New("short domain packet")
		}
		l := int(pkt[4])
		if len(pkt) < 5+l+2 {
			return "", 0, nil, errors.New("bad domain packet")
		}
		if l == 0 {
			return "", 0, nil, errors.New("empty domain name")
		}
		host := string(pkt[5 : 5+l])
		port := int(binary.BigEndian.Uint16(pkt[5+l : 5+l+2]))
		if port == 0 {
			return "", 0, nil, errors.New("invalid UDP port 0")
		}
		return host, port, pkt[5+l+2:], nil

	case 0x04:
		return "", 0, nil, errors.New("IPv6 is not supported")

	default:
		return "", 0, nil, errors.New("unsupported ATYP")
	}
}

func (s *udpSession) ensurePermission(ip net.IP) error {
	key := ip.String()

	s.permissionMu.Lock()
	exp, ok := s.permissions[key]
	if ok && time.Now().Before(exp) {
		s.permissionMu.Unlock()
		return nil
	}
	s.permissionMu.Unlock()

	req := stun.New()
	req.Type = stun.MessageType{Method: MethodCreatePermission, Class: stun.ClassRequest}
	req.TransactionID = stun.NewTransactionID()
	if err := addXORPeerAddress(req, ip, 0); err != nil {
		return err
	}
	if s.needAuth {
		if err := addAuthToMessage(req, s.username, s.password, &s.realm, &s.nonce); err != nil {
			return err
		}
	}

	res, err := s.request(req, 5*time.Second)
	if err != nil {
		return err
	}
	if res.Type.Class != stun.ClassSuccessResponse {
		code, reason := getErrorCode(res)
		return fmt.Errorf("permission error %d %s", code, reason)
	}

	s.permissionMu.Lock()
	s.permissions[key] = time.Now().Add(240 * time.Second)
	s.permissionMu.Unlock()

	return nil
}
