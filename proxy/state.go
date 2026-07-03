package proxy

import (
	"encoding/json"
	"os"
	"strings"
	"time"
)

type runtimeState struct {
	CurrentAddr string `json:"current_addr"`
	UpdatedAt   string `json:"updated_at"`
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

func readRuntimeState(path string) runtimeState {
	if path == "" {
		return runtimeState{}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return runtimeState{}
	}
	var state runtimeState
	if err := json.Unmarshal(raw, &state); err != nil {
		return runtimeState{}
	}
	state.CurrentAddr = strings.TrimSpace(state.CurrentAddr)
	return state
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
