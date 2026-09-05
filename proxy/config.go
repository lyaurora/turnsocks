package proxy

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/lyaurora/turnsocks/turncfg"
)

type Config struct {
	Listen       string
	Turns        string
	TurnServers  []turnServerConfig
	TurnPool     *turnPool
	TurnCooldown time.Duration
	ConfigPath   string
	DoH          string
	DoHClient    *http.Client
	StatePath    string
	DNSTTL       time.Duration
	Timeout      time.Duration
	LogVerbose   bool
	TCPAllocs    *tcpAllocationPool
	UDPPrewarm   *udpPrewarmPool
	UDPSessions  *udpSessionRegistry
}

func getenv(k, def string) string {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	return v
}

func preFlagValue(name, def string) string {
	longPrefix := "--" + name + "="
	shortPrefix := "-" + name + "="
	for i, arg := range os.Args[1:] {
		if arg == "--"+name || arg == "-"+name {
			if i+2 < len(os.Args) {
				return os.Args[i+2]
			}
			return def
		}
		if strings.HasPrefix(arg, longPrefix) {
			return strings.TrimPrefix(arg, longPrefix)
		}
		if strings.HasPrefix(arg, shortPrefix) {
			return strings.TrimPrefix(arg, shortPrefix)
		}
	}
	return def
}

func loadEnvFile(path string) error {
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for lineNo, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := turncfg.ParseEnvLine(line)
		if !ok {
			return fmt.Errorf("invalid env line %d", lineNo+1)
		}
		if key == "" {
			return fmt.Errorf("empty env key on line %d", lineNo+1)
		}
		if os.Getenv(key) != "" {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return nil
}

func readEnvFileValue(path string, wantKey string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for lineNo, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := turncfg.ParseEnvLine(line)
		if !ok {
			return "", fmt.Errorf("invalid env line %d", lineNo+1)
		}
		if key != wantKey {
			continue
		}
		return value, nil
	}
	return "", nil
}
