package proxy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lyaurora/turnsocks/turncfg"
)

func TestEnvPrecedence(test *testing.T) {
	const quotedServers = "'user:pa\\ss\"'@turn.example:3478"
	for _, scenario := range []struct {
		name, inherited, encodedFirst, wantStartup, wantReload string
	}{
		{"environment wins", "inherited", "'first'", "inherited", "first"},
		{"first value wins", "", "'first'", "first", "first"},
		{"empty value allows next", "", "''", "second", ""},
		{"quoted value", "", turncfg.EncodeEnvValue(quotedServers), quotedServers, quotedServers},
	} {
		test.Run(scenario.name, func(test *testing.T) {
			path := filepath.Join(test.TempDir(), "config.env")
			content := " # comment=ignored\n\n TURN_SERVERS = " + scenario.encodedFirst + "\nTURN_SERVERS=second\n"
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
			if got, err := readEnvFileValue(path, "TURN_SERVERS"); err != nil || got != scenario.wantReload {
				test.Fatalf("reload value = %q, %v; want first value %q", got, err, scenario.wantReload)
			}
		})
	}
}
