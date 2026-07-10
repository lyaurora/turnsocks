package proxy

import (
	"log"
	"net"
	"sync"
)

type proxyController struct {
	mu      sync.Mutex
	cfg     Config
	ln      net.Listener
	running bool
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

func (p *proxyController) stop() {
	p.mu.Lock()
	ln := p.ln
	p.ln = nil
	p.running = false
	p.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
}
