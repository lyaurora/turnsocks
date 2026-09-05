package proxy

import (
	"errors"
	"log"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/lyaurora/turnsocks/runtimestate"
)

type runtimeState = runtimestate.State

func readRuntimeState(path string) runtimeState {
	return runtimestate.Read(path)
}

func initialTurnServer(servers []turnServerConfig, state runtimeState) turnServerConfig {
	if len(servers) == 0 {
		return turnServerConfig{}
	}
	for _, server := range servers {
		if server.Addr == state.CurrentAddr {
			return server
		}
	}
	return servers[0]
}

// The caller holds p.mu so events cannot be written out of order.
func (p *turnPool) writeCurrentLocked(addr, reason string) {
	selected := false
	if err := runtimestate.Update(p.statePath, func(state *runtimeState) {
		// A panel selection is awaiting restart; do not replace it with an older connection's choice.
		if state.CurrentAddr != "" && state.CurrentAddr != p.persistedAddr {
			return
		}
		state.Select(addr, reason)
		selected = true
	}); err != nil {
		log.Printf("write runtime state failed: %v", err)
	} else if selected {
		p.persistedAddr = addr
	}
}

func (p *turnPool) recordFailure(addr, stage string, err error) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.recordFailureLocked(addr, stage, err)
}

func (p *turnPool) recordFailureLocked(addr, stage string, err error) {
	if err == nil || p.statePath == "" {
		return
	}
	// ponytail: record at most one failure per second; add history only if incident tracing needs it.
	now := time.Now()
	if now.Sub(p.lastFailureWrite) < time.Second {
		return
	}
	p.lastFailureWrite = now
	failure := &runtimestate.Failure{
		At: now.UTC().Format(time.RFC3339), Addr: addr, Stage: stage, Message: p.cleanError(err),
	}
	if err := runtimestate.Update(p.statePath, func(state *runtimeState) {
		state.LastFailure = failure
	}); err != nil {
		log.Printf("write runtime failure failed: %v", err)
	}
}

func (p *turnPool) cleanError(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err // A DoH URL can contain private query parameters.
	}
	message := err.Error()
	for _, state := range p.servers {
		s := state.Server
		if s.ExplicitAuth {
			message = strings.ReplaceAll(message, s.String(), s.Addr)
			message = strings.ReplaceAll(message, s.Username+":"+s.Password, "[已隐藏凭据]")
			if s.Password != "" {
				message = strings.ReplaceAll(message, s.Password, "[已隐藏密码]")
			}
		}
	}
	message = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, message)
	runes := []rune(message)
	if len(runes) > 400 {
		message = string(runes[:400]) + "…"
	}
	return strings.TrimSpace(message)
}
