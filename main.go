package main

import (
	"bytes"
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
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
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
	staleNonceCode                = 438
	maxSTUNMessageLength          = 64 * 1024
	allocationLifetime            = 10 * time.Minute
	allocationRefreshEvery        = 5 * time.Minute
	turnUDPAttemptTimeout         = time.Second
	turnTCPDialTimeout            = 3 * time.Second
	tcpKeepAlivePeriod            = 30 * time.Second
	udpSocketBufferSize           = 512 << 10
	proxyCopyBufferSize           = 32 << 10
	refreshRetryDelay             = time.Second
	turnConfigPollInterval        = 2 * time.Second
)

var sendIndicationMessageType = stun.MessageType{Method: MethodSend, Class: stun.ClassIndication}.Value()
var dataIndicationMessageType = stun.MessageType{Method: MethodData, Class: stun.ClassIndication}.Value()

var proxyCopyBufferPool = sync.Pool{
	New: func() any {
		buf := make([]byte, proxyCopyBufferSize)
		return &buf
	},
}

type Config struct {
	Listen       string
	Turns        string
	TurnServers  []turnServerConfig
	TurnPool     *turnPool
	TurnCooldown time.Duration
	ConfigPath   string
	DoH          string
	DoHClient    *http.Client
	StatePath    string
	DNSTTL       time.Duration
	Timeout      time.Duration
	LogVerbose   bool
	TCPAllocs    *tcpAllocationPool
	UDPPrewarm   *udpPrewarmPool
}

type turnServerState struct {
	Server         turnServerConfig
	FailedUntil    time.Time
	UDPFailedUntil time.Time
	LastError      string
}

type turnServerConfig struct {
	Addr         string
	Username     string
	Password     string
	ExplicitAuth bool
}

type turnPool struct {
	mu        sync.Mutex
	servers   []turnServerState
	cooldown  time.Duration
	statePath string
	current   string
}

// A TURN TCP allocation may carry multiple peers, but some servers reject
// concurrent CONNECT requests to the same peer with 446 Connection Already Exists.
type tcpAllocationPool struct {
	mu      sync.Mutex
	allocs  map[string][]*tcpAllocation
	allowed map[string]struct{}
}

type udpPrewarmPool struct {
	mu       sync.Mutex
	session  *udpSession
	turnKey  string
	network  string
	created  time.Time
	creating bool
}

type tcpAllocation struct {
	cfg         Config
	turn        turnServerConfig
	username    string
	password    string
	ctrlConn    net.Conn
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

type proxyController struct {
	mu      sync.Mutex
	cfg     Config
	ln      net.Listener
	running bool
}

type dnsEntry struct {
	IP       net.IP
	ExpireAt time.Time
}

type dnsLookupResult struct {
	IP  net.IP
	Err error
}

type dnsLookupCall struct {
	done   chan struct{}
	result dnsLookupResult
}

type runtimeState struct {
	CurrentAddr string `json:"current_addr"`
	UpdatedAt   string `json:"updated_at"`
}

var (
	dnsCache    sync.Map
	dnsLookupMu sync.Mutex
	dnsLookups  = make(map[string]*dnsLookupCall)
)

func main() {
	cfg := Config{}
	var cpuProfile string
	var memProfile string

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
	flag.StringVar(&cfg.StatePath, "state", getenv("STATE_PATH", ""), "runtime state file path")
	flag.DurationVar(&cfg.DNSTTL, "dns-ttl", 300*time.Second, "DNS cache TTL")
	flag.DurationVar(&cfg.Timeout, "timeout", 20*time.Second, "network timeout")
	flag.BoolVar(&cfg.LogVerbose, "v", false, "verbose log")
	flag.StringVar(&cpuProfile, "cpuprofile", "", "write CPU profile to file")
	flag.StringVar(&memProfile, "memprofile", "", "write heap profile to file on exit")
	flag.Parse()

	stopProfile := startProfiling(cpuProfile, memProfile)
	defer stopProfile()
	if cpuProfile != "" || memProfile != "" {
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			<-signals
			stopProfile()
			os.Exit(0)
		}()
	}

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
	if cfg.StatePath == "" {
		cfg.StatePath = defaultStatePath(cfg.ConfigPath)
	}
	cfg.StatePath = absPath(cfg.StatePath)
	var err error
	cfg.TurnServers, err = loadTurnServers(cfg)
	if err != nil {
		log.Fatalf("load TURN config failed: %v", err)
	}
	if len(cfg.TurnServers) == 0 {
		log.Fatal("missing TURN servers, set TURN_SERVERS in config.env")
	}
	cfg.TurnPool = newTurnPool(cfg.TurnServers, cfg.TurnCooldown, cfg.StatePath)
	cfg.TCPAllocs = newTCPAllocationPool()
	cfg.TCPAllocs.setAllowed(cfg.TurnServers)
	cfg.UDPPrewarm = newUDPPrewarmPool()
	cfg.TurnPool.markSuccess(cfg.TurnServers[0])
	dohURL, err := url.ParseRequestURI(cfg.DoH)
	if err != nil {
		log.Fatalf("invalid DoH endpoint: %v", err)
	}
	if dohURL.Scheme != "http" && dohURL.Scheme != "https" || dohURL.Host == "" {
		log.Fatal("DoH endpoint must be an http or https URL")
	}
	cfg.DoHClient = &http.Client{Timeout: cfg.Timeout}

	go prewarmDoH(cfg)
	log.Printf("TURN servers: %s", strings.Join(turnServerAddrs(cfg.TurnServers), ", "))
	log.Printf("TURN auth: per-server inline only")
	proxy := newProxyController(cfg)
	if err := proxy.start(); err != nil {
		log.Fatalf("SOCKS5 start failed: %v", err)
	}
	go prewarmTCPAllocation(cfg)
	go prewarmUDPAllocation(cfg)
	go watchTurnConfig(cfg)
	go cleanupDNSCache(cfg.DNSTTL)

	select {}
}

func startProfiling(cpuPath string, memPath string) func() {
	var (
		once    sync.Once
		cpuFile *os.File
	)

	if cpuPath != "" {
		f, err := os.Create(cpuPath)
		if err != nil {
			log.Fatalf("create CPU profile failed: %v", err)
		}
		cpuFile = f
		if err := pprof.StartCPUProfile(cpuFile); err != nil {
			_ = cpuFile.Close()
			log.Fatalf("start CPU profile failed: %v", err)
		}
	}

	return func() {
		once.Do(func() {
			if cpuFile != nil {
				pprof.StopCPUProfile()
				_ = cpuFile.Close()
			}
			if memPath == "" {
				return
			}
			runtime.GC()
			f, err := os.Create(memPath)
			if err != nil {
				log.Printf("create heap profile failed: %v", err)
				return
			}
			if err := pprof.WriteHeapProfile(f); err != nil {
				log.Printf("write heap profile failed: %v", err)
			}
			_ = f.Close()
		})
	}
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

func defaultStatePath(configPath string) string {
	if configPath != "" {
		return filepath.Join(filepath.Dir(configPath), "turnsocks.state")
	}
	return "turnsocks.state"
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

func readEnvFileValue(path string, wantKey string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for lineNo, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return "", fmt.Errorf("invalid env line %d", lineNo+1)
		}
		if strings.TrimSpace(key) != wantKey {
			continue
		}
		value = strings.TrimSpace(value)
		return strings.Trim(value, "\"'"), nil
	}
	return "", nil
}

func watchTurnConfig(cfg Config) {
	if cfg.ConfigPath == "" || cfg.TurnPool == nil {
		return
	}

	var lastMod time.Time
	if info, err := os.Stat(cfg.ConfigPath); err == nil {
		lastMod = info.ModTime()
	}

	ticker := time.NewTicker(turnConfigPollInterval)
	defer ticker.Stop()

	for range ticker.C {
		info, err := os.Stat(cfg.ConfigPath)
		if err != nil {
			if cfg.LogVerbose {
				log.Printf("TURN config watch stat failed: %v", err)
			}
			continue
		}
		modTime := info.ModTime()
		if modTime.Equal(lastMod) {
			continue
		}
		lastMod = modTime

		raw, err := readEnvFileValue(cfg.ConfigPath, "TURN_SERVERS")
		if err != nil {
			log.Printf("TURN config reload failed: %v", err)
			continue
		}
		servers, err := parseTurnServers(raw)
		if err != nil {
			log.Printf("TURN config reload ignored: %v", err)
			continue
		}
		if len(servers) == 0 {
			log.Printf("TURN config reload ignored: no TURN servers")
			continue
		}

		changed, currentChanged, currentAddr, added, removed := cfg.TurnPool.updateServers(servers)
		if !changed {
			continue
		}
		if cfg.TCPAllocs != nil {
			cfg.TCPAllocs.setAllowed(servers)
		}
		if cfg.UDPPrewarm != nil {
			cfg.UDPPrewarm.closeIfNotAllowed(servers)
		}
		if currentChanged {
			if err := writeRuntimeState(cfg.StatePath, currentAddr); err != nil {
				log.Printf("write runtime state failed: %v", err)
			}
		}
		log.Printf("TURN servers reloaded: %d total, +%d -%d", len(servers), added, removed)
		go prewarmTCPAllocation(cfg)
		go prewarmUDPAllocation(cfg)
	}
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

func parseTurnServers(turns string) ([]turnServerConfig, error) {
	seen := make(map[string]struct{})
	var servers []turnServerConfig
	for i, part := range strings.Split(turns, ",") {
		raw := strings.TrimSpace(part)
		if raw == "" {
			continue
		}
		server, err := parseTurnServerConfig(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid TURN server #%d %q: %w", i+1, raw, err)
		}
		key := server.String()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		servers = append(servers, server)
	}

	return servers, nil
}

func loadTurnServers(cfg Config) ([]turnServerConfig, error) {
	return parseTurnServers(cfg.Turns)
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

func newTurnPool(servers []turnServerConfig, cooldown time.Duration, statePath string) *turnPool {
	p := &turnPool{cooldown: cooldown, statePath: statePath}
	for _, server := range servers {
		p.servers = append(p.servers, turnServerState{Server: server})
	}
	return p
}

func (p *turnPool) updateServers(servers []turnServerConfig) (changed bool, currentChanged bool, currentAddr string, added int, removed int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	oldStates := make(map[string]turnServerState, len(p.servers))
	oldOrder := make([]string, 0, len(p.servers))
	for _, state := range p.servers {
		key := state.Server.String()
		oldStates[key] = state
		oldOrder = append(oldOrder, key)
	}

	newStates := make([]turnServerState, 0, len(servers))
	newKeys := make(map[string]struct{}, len(servers))
	for _, server := range servers {
		key := server.String()
		state, ok := oldStates[key]
		if !ok {
			added++
			state = turnServerState{Server: server}
		}
		state.Server = server
		newStates = append(newStates, state)
		newKeys[key] = struct{}{}
	}
	for key := range oldStates {
		if _, ok := newKeys[key]; !ok {
			removed++
		}
	}

	changed = len(oldOrder) != len(servers)
	if !changed {
		for i, server := range servers {
			if oldOrder[i] != server.String() {
				changed = true
				break
			}
		}
	}
	if !changed {
		return false, false, "", 0, 0
	}

	oldCurrent := p.current
	p.servers = newStates
	if len(newStates) == 0 {
		p.current = ""
		return true, oldCurrent != "", "", added, removed
	}
	if p.current == "" {
		p.current = newStates[0].Server.String()
		currentChanged = true
		currentAddr = newStates[0].Server.Addr
		return true, currentChanged, currentAddr, added, removed
	}
	if _, ok := newKeys[p.current]; ok {
		return true, false, "", added, removed
	}

	p.current = newStates[0].Server.String()
	currentChanged = oldCurrent != p.current
	currentAddr = newStates[0].Server.Addr
	return true, currentChanged, currentAddr, added, removed
}

func newTCPAllocationPool() *tcpAllocationPool {
	return &tcpAllocationPool{
		allocs:  make(map[string][]*tcpAllocation),
		allowed: make(map[string]struct{}),
	}
}

func newUDPPrewarmPool() *udpPrewarmPool {
	return &udpPrewarmPool{}
}

func (p *tcpAllocationPool) getOrCreate(cfg Config, turn turnServerConfig, peer string) (*tcpAllocation, error) {
	key := turn.String()

	p.mu.Lock()
	if !p.keyAllowedLocked(key) {
		p.mu.Unlock()
		return nil, errors.New("TURN server removed from pool")
	}
	p.pruneClosedLocked(key)
	for _, a := range p.allocs[key] {
		if a.tryReservePeer(peer) {
			p.mu.Unlock()
			return a, nil
		}
	}
	p.mu.Unlock()

	a, err := newReservedTCPAllocation(cfg, turn, peer)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	if !p.keyAllowedLocked(key) {
		p.mu.Unlock()
		a.close()
		return nil, errors.New("TURN server removed from pool")
	}
	p.pruneClosedLocked(key)
	for _, existing := range p.allocs[key] {
		if existing.tryReservePeer(peer) {
			p.mu.Unlock()
			a.close()
			return existing, nil
		}
	}
	p.allocs[key] = append(p.allocs[key], a)
	p.mu.Unlock()
	return a, nil
}

func (p *tcpAllocationPool) addIdle(cfg Config, turn turnServerConfig) error {
	if p == nil {
		return nil
	}
	key := turn.String()

	p.mu.Lock()
	if !p.keyAllowedLocked(key) {
		p.mu.Unlock()
		return nil
	}
	p.pruneClosedLocked(key)
	for _, a := range p.allocs[key] {
		if !a.isClosed() {
			p.mu.Unlock()
			return nil
		}
	}
	p.mu.Unlock()

	a, err := newTCPAllocation(cfg, turn)
	if err != nil {
		return err
	}

	p.mu.Lock()
	if !p.keyAllowedLocked(key) {
		p.mu.Unlock()
		a.close()
		return nil
	}
	p.pruneClosedLocked(key)
	for _, existing := range p.allocs[key] {
		if !existing.isClosed() {
			p.mu.Unlock()
			a.close()
			return nil
		}
	}
	p.allocs[key] = append(p.allocs[key], a)
	p.mu.Unlock()
	return nil
}

func prewarmTCPAllocation(cfg Config) {
	if cfg.TCPAllocs == nil || cfg.TurnPool == nil {
		return
	}
	candidates := cfg.TurnPool.candidates()
	if len(candidates) == 0 {
		return
	}
	turn := candidates[0]
	if err := cfg.TCPAllocs.addIdle(cfg, turn); err != nil {
		if cfg.LogVerbose {
			log.Printf("TCP allocation prewarm failed via %s: %v", turn.Addr, err)
		}
		return
	}
	if cfg.LogVerbose {
		log.Printf("TCP allocation prewarmed via %s", turn.Addr)
	}
}

func prewarmUDPAllocation(cfg Config) {
	if cfg.UDPPrewarm == nil || cfg.TurnPool == nil {
		return
	}
	candidates := cfg.TurnPool.candidates()
	if len(candidates) == 0 {
		return
	}
	turn := candidates[0]
	if !cfg.TurnPool.udpAllowed(turn) {
		return
	}
	if err := cfg.UDPPrewarm.add(cfg, turn); err != nil {
		if cfg.LogVerbose {
			log.Printf("UDP allocation prewarm failed via %s: %v", turn.Addr, err)
		}
		return
	}
	if cfg.LogVerbose {
		log.Printf("UDP allocation prewarmed via %s", turn.Addr)
	}
}

func prewarmDoH(cfg Config) {
	if _, err := resolveDoH("cloudflare.com", cfg); err != nil && cfg.LogVerbose {
		log.Printf("DoH prewarm failed: %v", err)
	}
}

func (p *udpPrewarmPool) add(cfg Config, turn turnServerConfig) error {
	if p == nil {
		return nil
	}
	key := turn.String()

	p.mu.Lock()
	if p.creating || (p.session != nil && !p.session.isClosed()) {
		p.mu.Unlock()
		return nil
	}
	p.session = nil
	p.creating = true
	p.mu.Unlock()

	s, err := newUDPSessionWithNetwork(cfg, nil, nil, turn, "udp")
	if err != nil {
		p.mu.Lock()
		p.creating = false
		p.mu.Unlock()
		return err
	}

	p.mu.Lock()
	p.creating = false
	if p.session != nil && !p.session.isClosed() {
		p.mu.Unlock()
		s.close()
		return nil
	}
	p.session = s
	p.turnKey = key
	p.network = "udp"
	p.created = time.Now()
	p.mu.Unlock()

	go p.expireIdle(cfg, s, allocationRefreshEvery)
	return nil
}

func (p *udpPrewarmPool) take(cfg Config, turn turnServerConfig, clientTCP net.Conn, localUDP *net.UDPConn) (*udpSession, string, bool) {
	if p == nil {
		return nil, "", false
	}
	key := turn.String()

	p.mu.Lock()
	s := p.session
	if s == nil || p.turnKey != key || p.network != "udp" || time.Since(p.created) >= allocationRefreshEvery || s.isClosed() {
		p.session = nil
		p.mu.Unlock()
		if s != nil {
			s.close()
		}
		go prewarmUDPAllocation(cfg)
		return nil, "", false
	}
	p.session = nil
	p.mu.Unlock()

	s.clientTCP = clientTCP
	s.localUDP = localUDP
	s.cfg = cfg
	go prewarmUDPAllocation(cfg)
	return s, turn.Addr + "/udp", true
}

func (p *udpPrewarmPool) expireIdle(cfg Config, s *udpSession, maxIdle time.Duration) {
	timer := time.NewTimer(maxIdle)
	defer timer.Stop()
	<-timer.C

	p.mu.Lock()
	if p.session == s {
		p.session = nil
		p.mu.Unlock()
		s.close()
		go prewarmUDPAllocation(cfg)
		return
	}
	p.mu.Unlock()
}

func (p *udpPrewarmPool) closeIfNotAllowed(servers []turnServerConfig) {
	if p == nil {
		return
	}
	allowed := turnServerKeySet(servers)

	p.mu.Lock()
	s := p.session
	if s == nil {
		p.mu.Unlock()
		return
	}
	if _, ok := allowed[p.turnKey]; ok {
		p.mu.Unlock()
		return
	}
	p.session = nil
	p.mu.Unlock()
	s.close()
}

func turnServerKeySet(servers []turnServerConfig) map[string]struct{} {
	set := make(map[string]struct{}, len(servers))
	for _, server := range servers {
		set[server.String()] = struct{}{}
	}
	return set
}

func (p *tcpAllocationPool) release(turn turnServerConfig, allocation *tcpAllocation, peer string) {
	if p == nil || allocation == nil {
		return
	}
	allocation.releasePeer(peer)

	key := turn.String()
	var closeIdle []*tcpAllocation
	p.mu.Lock()
	allowed := p.keyAllowedLocked(key)
	p.pruneClosedLocked(key)
	allocs := p.allocs[key]
	next := allocs[:0]
	keptIdle := false
	for _, a := range allocs {
		if a.isClosed() {
			continue
		}
		if a.hasActivePeers() {
			next = append(next, a)
			continue
		}
		if allowed && !keptIdle {
			keptIdle = true
			next = append(next, a)
			continue
		}
		closeIdle = append(closeIdle, a)
	}
	if len(next) == 0 {
		delete(p.allocs, key)
	} else {
		p.allocs[key] = next
	}
	p.mu.Unlock()

	for _, a := range closeIdle {
		a.close()
	}
}

func (p *tcpAllocationPool) setAllowed(servers []turnServerConfig) {
	if p == nil {
		return
	}
	allowed := turnServerKeySet(servers)
	var closeIdle []*tcpAllocation

	p.mu.Lock()
	p.allowed = allowed
	for key, allocs := range p.allocs {
		if _, ok := allowed[key]; ok {
			continue
		}
		next := allocs[:0]
		for _, a := range allocs {
			if a.isClosed() {
				continue
			}
			if a.hasActivePeers() {
				next = append(next, a)
				continue
			}
			closeIdle = append(closeIdle, a)
		}
		if len(next) == 0 {
			delete(p.allocs, key)
		} else {
			p.allocs[key] = next
		}
	}
	p.mu.Unlock()

	for _, a := range closeIdle {
		a.close()
	}
}

func (p *tcpAllocationPool) keyAllowedLocked(key string) bool {
	if len(p.allowed) == 0 {
		return true
	}
	_, ok := p.allowed[key]
	return ok
}

func (p *tcpAllocationPool) pruneClosedLocked(key string) {
	allocs := p.allocs[key]
	if len(allocs) == 0 {
		return
	}
	active := allocs[:0]
	for _, a := range allocs {
		if !a.isClosed() {
			active = append(active, a)
		}
	}
	if len(active) == 0 {
		delete(p.allocs, key)
		return
	}
	p.allocs[key] = active
}

func (p *tcpAllocationPool) invalidate(turn turnServerConfig, allocation *tcpAllocation) {
	if p == nil || allocation == nil {
		return
	}
	key := turn.String()
	p.mu.Lock()
	allocs := p.allocs[key]
	for i, a := range allocs {
		if a == allocation {
			copy(allocs[i:], allocs[i+1:])
			allocs = allocs[:len(allocs)-1]
			if len(allocs) == 0 {
				delete(p.allocs, key)
			} else {
				p.allocs[key] = allocs
			}
			p.mu.Unlock()
			allocation.close()
			return
		}
	}
	p.pruneClosedLocked(key)
	p.mu.Unlock()
	allocation.close()
}

func newReservedTCPAllocation(cfg Config, turn turnServerConfig, peer string) (*tcpAllocation, error) {
	a, err := newTCPAllocation(cfg, turn)
	if err != nil {
		return nil, err
	}
	if !a.tryReservePeer(peer) {
		a.close()
		return nil, errors.New("TCP allocation is not available")
	}
	return a, nil
}

func (p *turnPool) candidates() []turnServerConfig {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	var active []turnServerConfig
	var cooling []turnServerConfig
	for _, s := range p.servers {
		if now.Before(s.FailedUntil) {
			cooling = append(cooling, s.Server)
			continue
		}
		active = append(active, s.Server)
	}
	if len(active) > 0 {
		return preferCurrentFirst(active, p.current)
	}

	// If every server is cooling down, allow a full pass anyway so recovery is fast.
	return preferCurrentFirst(cooling, p.current)
}

func preferCurrentFirst(servers []turnServerConfig, current string) []turnServerConfig {
	if current == "" || len(servers) < 2 {
		return servers
	}
	for i, server := range servers {
		if server.String() != current {
			continue
		}
		if i == 0 {
			return servers
		}
		ordered := make([]turnServerConfig, 0, len(servers))
		ordered = append(ordered, server)
		ordered = append(ordered, servers[:i]...)
		ordered = append(ordered, servers[i+1:]...)
		return ordered
	}
	return servers
}

func (p *turnPool) markSuccess(server turnServerConfig) {
	p.mu.Lock()
	currentChanged := false
	for i := range p.servers {
		if p.servers[i].Server.String() == server.String() {
			p.servers[i].FailedUntil = time.Time{}
			p.servers[i].LastError = ""
			if p.current != server.String() {
				p.current = server.String()
				currentChanged = true
			}
			break
		}
	}
	statePath := p.statePath
	p.mu.Unlock()

	if currentChanged {
		if err := writeRuntimeState(statePath, server.Addr); err != nil {
			log.Printf("write runtime state failed: %v", err)
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

func (p *turnPool) udpAllowed(server turnServerConfig) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	for i := range p.servers {
		if p.servers[i].Server.String() == server.String() {
			return !now.Before(p.servers[i].UDPFailedUntil)
		}
	}
	return true
}

func (p *turnPool) contains(server turnServerConfig) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	key := server.String()
	for i := range p.servers {
		if p.servers[i].Server.String() == key {
			return true
		}
	}
	return false
}

func (p *turnPool) markUDPFailure(server turnServerConfig, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for i := range p.servers {
		if p.servers[i].Server.String() == server.String() {
			p.servers[i].UDPFailedUntil = time.Now().Add(p.cooldown)
			p.servers[i].LastError = "udp: " + err.Error()
			return
		}
	}
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

func writeRuntimeState(path string, currentAddr string) error {
	if path == "" {
		return nil
	}
	state := runtimeState{
		CurrentAddr: currentAddr,
		UpdatedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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

	dataConn, release, turnAddr, err := dialTurnTCP(cfg, ip, req.Port)
	if err != nil {
		log.Printf("TURN TCP failed %s:%d: %v", ip.String(), req.Port, err)
		_ = writeSocksReply(client, 0x05, "0.0.0.0", 0)
		return
	}
	if cfg.LogVerbose {
		log.Printf("TURN TCP selected %s for %s:%d", turnAddr, req.Host, req.Port)
	}
	defer release()
	defer dataConn.Close()

	if err := writeSocksReply(client, 0x00, "0.0.0.0", 0); err != nil {
		return
	}

	pipe(client, dataConn)
}

func pipe(a net.Conn, b net.Conn) {
	done := make(chan struct{}, 2)

	go func() {
		copyAndCloseWrite(a, b)
		done <- struct{}{}
	}()

	go func() {
		copyAndCloseWrite(b, a)
		done <- struct{}{}
	}()

	<-done
	<-done
	_ = a.Close()
	_ = b.Close()
}

func copyAndCloseWrite(dst net.Conn, src net.Conn) {
	bufp := proxyCopyBufferPool.Get().(*[]byte)
	_, err := io.CopyBuffer(dst, src, *bufp)
	proxyCopyBufferPool.Put(bufp)
	if err != nil {
		_ = dst.Close()
		_ = src.Close()
		return
	}
	closeWrite(dst)
}

func closeWrite(conn net.Conn) {
	type closeWriter interface {
		CloseWrite() error
	}
	if c, ok := conn.(closeWriter); ok {
		_ = c.CloseWrite()
		return
	}
	_ = conn.Close()
}

func resolveDoH(host string, cfg Config) (net.IP, error) {
	ip := net.ParseIP(host)
	if ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			return ip4, nil
		}
		return nil, errors.New("IPv6 is not supported")
	}

	queryHost := normalizeDNSHost(host)
	if queryHost == "" {
		return nil, errors.New("empty DNS host")
	}

	if v, ok := dnsCache.Load(queryHost); ok {
		e := v.(dnsEntry)
		if time.Now().Before(e.ExpireAt) {
			return e.IP, nil
		}
		dnsCache.Delete(queryHost)
	}

	return resolveDoHOnce(queryHost, cfg)
}

func resolveDoHOnce(queryHost string, cfg Config) (net.IP, error) {
	dnsLookupMu.Lock()
	if call := dnsLookups[queryHost]; call != nil {
		dnsLookupMu.Unlock()
		<-call.done
		return call.result.IP, call.result.Err
	}
	call := &dnsLookupCall{done: make(chan struct{})}
	dnsLookups[queryHost] = call
	dnsLookupMu.Unlock()

	ip, err := queryDoH(queryHost, cfg)
	call.result = dnsLookupResult{IP: ip, Err: err}

	dnsLookupMu.Lock()
	delete(dnsLookups, queryHost)
	close(call.done)
	dnsLookupMu.Unlock()

	return ip, err
}

func queryDoH(queryHost string, cfg Config) (net.IP, error) {
	u, err := buildDoHURL(cfg.DoH)
	if err != nil {
		return nil, err
	}
	query, queryID, err := buildDNSAQuery(queryHost)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequest("POST", u, bytes.NewReader(query))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Accept", "application/dns-message")
	httpReq.Header.Set("Content-Type", "application/dns-message")

	httpClient := cfg.DoHClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: cfg.Timeout}
	}
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("DoH HTTP status %s", resp.Status)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, err
	}
	ip, ttl, err := parseDNSAResponse(raw, queryID, queryHost, cfg.DNSTTL)
	if err != nil {
		return nil, err
	}
	dnsCache.Store(queryHost, dnsEntry{IP: ip, ExpireAt: time.Now().Add(ttl)})
	return ip, nil
}

func normalizeDNSHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
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

func buildDoHURL(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	normalizeWireDoHEndpoint(u)
	return u.String(), nil
}

func normalizeWireDoHEndpoint(u *url.URL) {
	if strings.EqualFold(u.Hostname(), "dns.google") && strings.TrimRight(u.EscapedPath(), "/") == "/resolve" {
		u.Path = "/dns-query"
		u.RawPath = ""
	}
}

func buildDNSAQuery(host string) ([]byte, uint16, error) {
	id := uint16(time.Now().UnixNano())
	msg := make([]byte, 12, 64)
	binary.BigEndian.PutUint16(msg[0:2], id)
	binary.BigEndian.PutUint16(msg[2:4], 0x0100)
	binary.BigEndian.PutUint16(msg[4:6], 1)
	name, err := encodeDNSName(host)
	if err != nil {
		return nil, 0, err
	}
	msg = append(msg, name...)
	msg = binary.BigEndian.AppendUint16(msg, 1)
	msg = binary.BigEndian.AppendUint16(msg, 1)
	return msg, id, nil
}

func encodeDNSName(host string) ([]byte, error) {
	host = strings.TrimSuffix(strings.TrimSpace(host), ".")
	if host == "" {
		return nil, errors.New("empty DNS host")
	}
	var out []byte
	for _, label := range strings.Split(host, ".") {
		if label == "" {
			return nil, fmt.Errorf("invalid DNS host %q", host)
		}
		if len(label) > 63 {
			return nil, fmt.Errorf("DNS label too long in %q", host)
		}
		out = append(out, byte(len(label)))
		out = append(out, label...)
	}
	if len(out) > 254 {
		return nil, fmt.Errorf("DNS host too long %q", host)
	}
	out = append(out, 0)
	return out, nil
}

func parseDNSAResponse(msg []byte, wantID uint16, host string, maxTTL time.Duration) (net.IP, time.Duration, error) {
	if len(msg) < 12 {
		return nil, 0, errors.New("short DNS response")
	}
	if gotID := binary.BigEndian.Uint16(msg[0:2]); gotID != wantID {
		return nil, 0, errors.New("DNS response ID mismatch")
	}
	if msg[3]&0x0f != 0 {
		return nil, 0, fmt.Errorf("DNS response code %d", msg[3]&0x0f)
	}
	questions := int(binary.BigEndian.Uint16(msg[4:6]))
	answers := int(binary.BigEndian.Uint16(msg[6:8]))
	offset := 12
	var err error
	for i := 0; i < questions; i++ {
		offset, err = skipDNSName(msg, offset)
		if err != nil {
			return nil, 0, err
		}
		if len(msg) < offset+4 {
			return nil, 0, errors.New("short DNS question")
		}
		offset += 4
	}

	for i := 0; i < answers; i++ {
		offset, err = skipDNSName(msg, offset)
		if err != nil {
			return nil, 0, err
		}
		if len(msg) < offset+10 {
			return nil, 0, errors.New("short DNS answer")
		}
		answerType := binary.BigEndian.Uint16(msg[offset : offset+2])
		answerClass := binary.BigEndian.Uint16(msg[offset+2 : offset+4])
		answerTTL := binary.BigEndian.Uint32(msg[offset+4 : offset+8])
		rdLen := int(binary.BigEndian.Uint16(msg[offset+8 : offset+10]))
		offset += 10
		if len(msg) < offset+rdLen {
			return nil, 0, errors.New("short DNS answer data")
		}
		if answerType == 1 && answerClass == 1 && rdLen == net.IPv4len {
			ip := net.IPv4(msg[offset], msg[offset+1], msg[offset+2], msg[offset+3])
			ttl := maxTTL
			if answerTTL > 0 {
				answerDuration := time.Duration(answerTTL) * time.Second
				if ttl <= 0 || answerDuration < ttl {
					ttl = answerDuration
				}
			}
			if ttl <= 0 {
				ttl = time.Minute
			}
			return ip, ttl, nil
		}
		offset += rdLen
	}

	return nil, 0, fmt.Errorf("no A record for %s", host)
}

func skipDNSName(msg []byte, offset int) (int, error) {
	for {
		if offset >= len(msg) {
			return 0, errors.New("short DNS name")
		}
		l := int(msg[offset])
		switch l & 0xc0 {
		case 0x00:
			offset++
			if l == 0 {
				return offset, nil
			}
			if offset+l > len(msg) {
				return 0, errors.New("short DNS label")
			}
			offset += l
		case 0xc0:
			if offset+2 > len(msg) {
				return 0, errors.New("short DNS compression pointer")
			}
			return offset + 2, nil
		default:
			return 0, errors.New("unsupported DNS name label")
		}
	}
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
	return readSTUNMessage(conn)
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

	dataConn, err := dialTCPKeepAlive(a.turn.Addr, shorterTimeout(a.cfg.Timeout, turnTCPDialTimeout))
	if err != nil {
		return nil, err
	}

	if err := a.bindDataConn(dataConn, connID); err != nil {
		dataConn.Close()
		return nil, err
	}

	return dataConn, nil
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
		return nil, turnPeerError(fmt.Errorf("connect error %d %s", c, strings.TrimRight(r, "\x00")))
	}
	connID, err := res.Get(AttrConnectionID)
	if err != nil || len(connID) == 0 {
		return nil, errors.New("missing CONNECTION-ID")
	}
	return connID, nil
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
				cfg.TurnPool.markSuccess(turn)
				return s, turnAddr, nil
			}
			var s *udpSession
			s, err = newUDPSessionWithNetwork(cfg, clientTCP, localUDP, turn, "udp")
			if err == nil {
				cfg.TurnPool.markSuccess(turn)
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
			cfg.TurnPool.markSuccess(turn)
			return s, turn.Addr + "/tcp", nil
		}
		errs = append(errs, fmt.Errorf("%s/tcp: %w", turn.Addr, tcpErr))
		cfg.TurnPool.markFailure(turn, tcpErr)
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
			_ = s.turnConn.close()
		}
	})
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

	s.writeMu.Lock()
	err := s.turnConn.writeMessage(req, timeout)
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

	if err := s.turnConn.writeMessage(req, timeout); err != nil {
		return err
	}
	res, err := s.turnConn.readMessage(timeout)
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

		if err := s.turnConn.writeMessage(req2, timeout); err != nil {
			return err
		}
		res2, err := s.turnConn.readMessage(timeout)
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

func (s *udpSession) refreshLoop() {
	ticker := time.NewTicker(allocationRefreshEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := s.refreshAllocation(); err != nil {
				select {
				case <-time.After(refreshRetryDelay):
				case <-s.closed:
					return
				}
				if retryErr := s.refreshAllocation(); retryErr != nil {
					s.cfg.TurnPool.markFailure(s.turn, retryErr)
					log.Printf("UDP allocation refresh failed via %s after retry: %v", s.turn.Addr, errors.Join(err, retryErr))
					s.close()
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
			s.close()
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
			s.close()
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
			s.close()
			return
		}
	}
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
	go s.finishPermission(key, txKey, ch)

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

	s.writeMu.Lock()
	err := s.turnConn.writeMessage(req, timeout)
	s.writeMu.Unlock()
	if err != nil {
		s.pendingMu.Lock()
		delete(s.pending, txKey)
		s.pendingMu.Unlock()
		return "", nil, err
	}

	return txKey, ch, nil
}

func (s *udpSession) finishPermission(key [4]byte, txKey string, ch chan *stun.Message) {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	defer func() {
		s.pendingMu.Lock()
		delete(s.pending, txKey)
		s.pendingMu.Unlock()

		s.permissionMu.Lock()
		delete(s.permPending, key)
		s.permissionMu.Unlock()
	}()

	select {
	case res := <-ch:
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
	case <-timer.C:
		if s.cfg.LogVerbose {
			log.Printf("CreatePermission timed out")
		}
	case <-s.closed:
		return
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

	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()

	select {
	case res := <-ch:
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
	case <-timer.C:
		if s.cfg.LogVerbose {
			log.Printf("CreatePermission retry timed out")
		}
	case <-s.closed:
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
