package proxy

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lyaurora/turnsocks/turncfg"
)

type turnServerState struct {
	Server            turnServerConfig
	FailedUntil       time.Time
	UDPFailedUntil    time.Time
	UDPTCPFailedUntil time.Time
	UDPCapacityUntil  time.Time
	LastError         string
}

type turnServerConfig struct {
	Addr         string
	Username     string
	Password     string
	ExplicitAuth bool
}

type turnPool struct {
	mu               sync.Mutex
	servers          []turnServerState
	cooldown         time.Duration
	statePath        string
	current          string
	persistedAddr    string
	lastFailureWrite time.Time
}

type fileStamp struct {
	modTime time.Time
	size    int64
	dev     uint64
	ino     uint64
	valid   bool
}

func statFileStamp(path string) (fileStamp, error) {
	info, err := os.Stat(path)
	if err != nil {
		return fileStamp{}, err
	}
	stamp := fileStamp{
		modTime: info.ModTime(),
		size:    info.Size(),
		valid:   true,
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		stamp.dev = uint64(st.Dev)
		stamp.ino = uint64(st.Ino)
	}
	return stamp, nil
}

func (s fileStamp) equal(other fileStamp) bool {
	return s.valid && other.valid && s.modTime.Equal(other.modTime) && s.size == other.size && s.dev == other.dev && s.ino == other.ino
}

// A TURN TCP allocation may carry multiple peers, but some servers reject
// concurrent CONNECT requests to the same peer with 446 Connection Already Exists.
func watchTurnConfig(cfg Config) {
	if cfg.ConfigPath == "" || cfg.TurnPool == nil {
		return
	}

	lastStamp, err := statFileStamp(cfg.ConfigPath)
	if err != nil && cfg.LogVerbose && !os.IsNotExist(err) {
		log.Printf("TURN config watch stat failed: %v", err)
	}

	ticker := time.NewTicker(turnConfigPollInterval)
	defer ticker.Stop()

	for range ticker.C {
		stamp, err := statFileStamp(cfg.ConfigPath)
		if err != nil {
			if cfg.LogVerbose && !os.IsNotExist(err) {
				log.Printf("TURN config watch stat failed: %v", err)
			}
			continue
		}
		if stamp.equal(lastStamp) {
			continue
		}
		lastStamp = stamp

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

		changed, added, removed := cfg.TurnPool.updateServers(servers)
		if !changed {
			continue
		}
		if cfg.TCPAllocs != nil {
			cfg.TCPAllocs.setAllowed(servers)
		}
		if cfg.UDPPrewarm != nil {
			cfg.UDPPrewarm.closeIfNotAllowed(servers)
		}
		log.Printf("TURN servers reloaded: %d total, +%d -%d", len(servers), added, removed)
		go prewarmTCPAllocation(cfg)
		go prewarmUDPAllocation(cfg)
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
	parsed, err := turncfg.ParseServer(raw)
	if err != nil {
		return turnServerConfig{}, err
	}
	return turnServerConfig{
		Addr:         parsed.Addr,
		Username:     parsed.Username,
		Password:     parsed.Password,
		ExplicitAuth: parsed.HasAuth,
	}, nil
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
	state := readRuntimeState(statePath)
	p.persistedAddr = state.CurrentAddr
	if len(servers) > 0 {
		initial := initialTurnServer(servers, state)
		p.current = initial.String()
		p.writeCurrentLocked(initial.Addr, "启动选择节点")
	}
	return p
}

func (p *turnPool) updateServers(servers []turnServerConfig) (changed bool, added int, removed int) {
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
		return false, 0, 0
	}

	p.servers = newStates
	if len(newStates) == 0 {
		p.current = ""
		p.writeCurrentLocked("", "节点列表已清空")
		return true, added, removed
	}
	if p.current == "" {
		p.current = newStates[0].Server.String()
		p.writeCurrentLocked(newStates[0].Server.Addr, "新增首个节点")
		return true, added, removed
	}
	if _, ok := newKeys[p.current]; ok {
		return true, added, removed
	}

	p.current = newStates[0].Server.String()
	p.writeCurrentLocked(newStates[0].Server.Addr, "当前节点已移除，改用列表中的首个节点")
	return true, added, removed
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
	defer p.mu.Unlock()
	for i := range p.servers {
		if p.servers[i].Server.String() == server.String() {
			p.servers[i].FailedUntil = time.Time{}
			p.servers[i].LastError = ""
			if p.current != server.String() {
				reason := "新连接使用其他可用节点"
				for _, state := range p.servers {
					if state.Server.String() == p.current && state.LastError != "" &&
						(time.Now().Before(state.FailedUntil) || time.Now().Before(state.UDPFailedUntil)) {
						reason = "原节点连接失败，自动切换：" + state.LastError
						break
					}
				}
				p.current = server.String()
				p.writeCurrentLocked(server.Addr, reason)
			}
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
			p.servers[i].LastError = p.cleanError(err)
			p.recordFailureLocked(server.Addr, "TURN TCP", err)
			return
		}
	}
}

func (p *turnPool) markUDPSuccess(server turnServerConfig) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for i := range p.servers {
		if p.servers[i].Server.String() == server.String() {
			p.servers[i].UDPFailedUntil = time.Time{}
			p.servers[i].UDPTCPFailedUntil = time.Time{}
			p.servers[i].UDPCapacityUntil = time.Time{}
			if strings.HasPrefix(p.servers[i].LastError, "udp: ") {
				p.servers[i].LastError = ""
			}
			return
		}
	}
}

func (p *turnPool) markUDPTCPSuccess(server turnServerConfig) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for i := range p.servers {
		if p.servers[i].Server.String() == server.String() {
			p.servers[i].UDPTCPFailedUntil = time.Time{}
			p.servers[i].UDPCapacityUntil = time.Time{}
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

func (p *turnPool) udpTCPAllowed(server turnServerConfig) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	for i := range p.servers {
		if p.servers[i].Server.String() == server.String() {
			return !now.Before(p.servers[i].UDPTCPFailedUntil)
		}
	}
	return true
}

func (p *turnPool) udpPrewarmAllowed(server turnServerConfig) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	for i := range p.servers {
		if p.servers[i].Server.String() == server.String() {
			return !now.Before(p.servers[i].UDPFailedUntil) && !now.Before(p.servers[i].UDPCapacityUntil)
		}
	}
	return true
}

func (p *turnPool) udpCapacityCooling(server turnServerConfig) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	for i := range p.servers {
		if p.servers[i].Server.String() == server.String() {
			return now.Before(p.servers[i].UDPCapacityUntil)
		}
	}
	return false
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
			p.servers[i].LastError = "udp: " + p.cleanError(err)
			p.recordFailureLocked(server.Addr, "TURN UDP", err)
			return
		}
	}
}

func (p *turnPool) markUDPCapacityFailure(server turnServerConfig, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	retryCooldown := shorterTimeout(p.cooldown, udpCapacityCooldown)
	for i := range p.servers {
		if p.servers[i].Server.String() == server.String() {
			p.servers[i].UDPFailedUntil = now.Add(retryCooldown)
			p.servers[i].UDPTCPFailedUntil = now.Add(retryCooldown)
			p.servers[i].UDPCapacityUntil = now.Add(p.cooldown)
			p.servers[i].LastError = "udp: " + p.cleanError(err)
			p.recordFailureLocked(server.Addr, "TURN UDP", err)
			return
		}
	}
}

func (p *turnPool) markUDPTCPFailure(server turnServerConfig, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for i := range p.servers {
		if p.servers[i].Server.String() == server.String() {
			p.servers[i].UDPTCPFailedUntil = time.Now().Add(p.cooldown)
			p.recordFailureLocked(server.Addr, "TURN UDP（TCP 传输）", err)
			return
		}
	}
}
