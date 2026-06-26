package proxy

import (
	"encoding/json"
	"os"
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
