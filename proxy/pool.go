package proxy

import (
	"errors"
	"log"
	"net"
	"sync"
	"time"
)

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
