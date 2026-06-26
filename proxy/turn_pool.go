package proxy

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

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
