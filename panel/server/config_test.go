package server

import (
	"path/filepath"
	"testing"
)

func TestWriteRuntimeState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "turnsocks.state")
	if err := writeRuntimeState(path, "turn.example.com:3478"); err != nil {
		t.Fatal(err)
	}

	state := readRuntimeState(path)
	if state.CurrentAddr != "turn.example.com:3478" {
		t.Fatalf("got current addr %q", state.CurrentAddr)
	}
	if state.UpdatedAt == "" {
		t.Fatal("missing updated_at")
	}
}
