package proxy

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
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
}

func getenv(k, def string) string {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	return v
}

func defaultConfigPath() string {
	exe, err := os.Executable()
	if err == nil && exe != "" {
		return filepath.Join(filepath.Dir(exe), "config.env")
	}
	return "config.env"
}

func defaultStatePath(configPath string) string {
	if configPath != "" {
		return filepath.Join(filepath.Dir(configPath), "turnsocks.state")
	}
	return "turnsocks.state"
}

func absPath(path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
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
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("invalid env line %d", lineNo+1)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return fmt.Errorf("empty env key on line %d", lineNo+1)
		}
		if os.Getenv(key) != "" {
			continue
		}
		value = strings.TrimSpace(value)
		value = strings.Trim(value, "\"'")
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
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return "", fmt.Errorf("invalid env line %d", lineNo+1)
		}
		if strings.TrimSpace(key) != wantKey {
			continue
		}
		value = strings.TrimSpace(value)
		return strings.Trim(value, "\"'"), nil
	}
	return "", nil
}

func validateTurnAddr(addr string) error {
	if addr == "" {
		return errors.New("TURN server address is empty")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("TURN server must be host:port: %w", err)
	}
	if host == "" || port == "" {
		return errors.New("TURN server must include host and port")
	}
	return nil
}
