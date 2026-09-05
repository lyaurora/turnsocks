package proxy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lyaurora/turnsocks/turncfg"
)

func TestEnvPrecedence(test *testing.T) {
	for _, scenario := range []struct {
		name, inherited, first, wantStartup string
	}{
		{"environment wins", "inherited", "first", "inherited"},
		{"first value wins", "", "first", "first"},
		{"empty value allows next", "", "", "second"},
	} {
		test.Run(scenario.name, func(test *testing.T) {
			path := filepath.Join(test.TempDir(), "config.env")
			content := " # comment=ignored\n\n TURN_SERVERS = '" + scenario.first + "'\nTURN_SERVERS=second\n"
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				test.Fatal(err)
			}
			test.Setenv("TURN_SERVERS", scenario.inherited)
			if err := loadEnvFile(path); err != nil {
				test.Fatal(err)
			}
			if got := os.Getenv("TURN_SERVERS"); got != scenario.wantStartup {
				test.Fatalf("startup value = %q, want %q", got, scenario.wantStartup)
			}
			if got, err := readEnvFileValue(path, "TURN_SERVERS"); err != nil || got != scenario.first {
				test.Fatalf("reload value = %q, %v; want first value %q", got, err, scenario.first)
			}
		})
	}
}

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
