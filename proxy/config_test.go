package proxy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lyaurora/turnsocks/turncfg"
)

func TestEnvQuotesMatchStartupAndReload(t *testing.T) {
	const servers = "'user:pa\\ss\"'@turn.example:3478"
	path := filepath.Join(t.TempDir(), "config.env")
	if err := os.WriteFile(path, []byte("TURN_SERVERS="+turncfg.EncodeEnvValue(servers)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TURN_SERVERS", "")
	if err := loadEnvFile(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("TURN_SERVERS"); got != servers {
		t.Fatalf("startup value = %q, want %q", got, servers)
	}
	got, err := readEnvFileValue(path, "TURN_SERVERS")
	if err != nil || got != servers {
		t.Fatalf("reload value = %q, err = %v; want %q", got, err, servers)
	}
}
