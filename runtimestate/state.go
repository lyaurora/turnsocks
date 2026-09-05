// Package runtimestate shares the proxy's status file with the panel.
package runtimestate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type State struct {
	CurrentAddr string   `json:"current_addr"`
	UpdatedAt   string   `json:"updated_at"`
	LastFailure *Failure `json:"last_failure,omitempty"`
	LastSwitch  *Switch  `json:"last_switch,omitempty"`
}

type Failure struct {
	At      string `json:"at"`
	Addr    string `json:"addr,omitempty"`
	Stage   string `json:"stage"`
	Message string `json:"message"`
}

type Switch struct {
	At     string `json:"at"`
	From   string `json:"from,omitempty"`
	To     string `json:"to"`
	Reason string `json:"reason"`
}

func Read(path string) State {
	raw, err := os.ReadFile(path)
	if err != nil {
		return State{}
	}
	var state State
	if json.Unmarshal(raw, &state) != nil {
		return State{}
	}
	state.CurrentAddr = strings.TrimSpace(state.CurrentAddr)
	return state
}

func (s *State) Select(addr, reason string) {
	if s.CurrentAddr == addr {
		return
	}
	s.LastSwitch = &Switch{
		At: time.Now().UTC().Format(time.RFC3339), From: s.CurrentAddr, To: addr, Reason: reason,
	}
	s.CurrentAddr = addr
}

// Update serializes read-modify-write across the panel and proxy processes.
// The lock has its own inode because the state file is replaced by rename.
func Update(path string, update func(*State)) error {
	if path == "" {
		return nil
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	state := Read(path)
	update(&state)
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
