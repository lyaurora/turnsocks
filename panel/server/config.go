package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lyaurora/turnsocks/turncfg"
)

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

func defaultTestResultsPath(configPath string) string {
	if configPath != "" {
		return filepath.Join(filepath.Dir(configPath), "turnsocks.tests.json")
	}
	return "turnsocks.tests.json"
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

func readProxyConfig(path string) (proxyConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return proxyConfig{}, err
	}
	cfg := proxyConfig{Listen: defaultProxyListen, DoH: defaultDoH}
	for lineNo, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return proxyConfig{}, fmt.Errorf("config.env 第 %d 行格式错误", lineNo+1)
		}
		value = strings.Trim(strings.TrimSpace(value), "\"'")
		switch strings.TrimSpace(key) {
		case "LISTEN":
			if value != "" {
				cfg.Listen = value
			}
		case "TURN_SERVERS":
			cfg.Servers = splitServers(value)
		case "DOH":
			if value != "" {
				cfg.DoH = value
			}
		case "PANEL_USERNAME":
			cfg.PanelUsername = value
		case "PANEL_PASSWORD":
			cfg.PanelPassword = value
		}
	}
	return cfg, nil
}

func writeProxyConfig(path string, cfg proxyConfig) error {
	if cfg.Listen == "" {
		cfg.Listen = defaultProxyListen
	}
	if cfg.DoH == "" {
		cfg.DoH = defaultDoH
	}
	if len(cfg.Servers) == 0 {
		return errors.New("至少保留一个 TURN 节点")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	data := updateProxyConfigText(string(raw), cfg)

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(data), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func updateProxyConfigText(raw string, cfg proxyConfig) string {
	values := map[string]string{
		"LISTEN":         cfg.Listen,
		"TURN_SERVERS":   strings.Join(cfg.Servers, ","),
		"DOH":            cfg.DoH,
		"PANEL_USERNAME": cfg.PanelUsername,
		"PANEL_PASSWORD": cfg.PanelPassword,
	}
	required := []string{"LISTEN", "TURN_SERVERS", "DOH"}
	authKeys := []string{"PANEL_USERNAME", "PANEL_PASSWORD"}

	content := strings.ReplaceAll(raw, "\r\n", "\n")
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	written := make(map[string]bool, len(values))
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, _, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value, managed := values[key]
		if !managed {
			continue
		}
		if written[key] {
			lines[i] = ""
			continue
		}
		lines[i] = key + "=" + value
		written[key] = true
	}

	for _, key := range required {
		if !written[key] {
			lines = appendConfigLine(lines, key+"="+values[key])
			written[key] = true
		}
	}
	if cfg.PanelUsername != "" || cfg.PanelPassword != "" {
		for _, key := range authKeys {
			if !written[key] {
				lines = appendConfigLine(lines, key+"="+values[key])
			}
		}
	}

	return strings.Join(lines, "\n") + "\n"
}

func appendConfigLine(lines []string, line string) []string {
	if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
		lines = append(lines, "")
	}
	return append(lines, line)
}

func splitServers(raw string) []string {
	seen := make(map[string]struct{})
	var servers []string
	for _, part := range strings.Split(raw, ",") {
		server, err := normalizeServer(part)
		if err != nil {
			continue
		}
		if _, ok := seen[server]; ok {
			continue
		}
		seen[server] = struct{}{}
		servers = append(servers, server)
	}
	return servers
}

func normalizeServer(raw string) (string, error) {
	info, err := parseServer(raw)
	if err != nil {
		return "", err
	}
	return info.Raw, nil
}

func parseServer(raw string) (serverInfo, error) {
	server, err := turncfg.ParseServer(raw)
	if err != nil {
		return serverInfo{}, err
	}
	return serverInfo{
		Raw:      server.Raw,
		Addr:     server.Addr,
		Username: server.Username,
		HasAuth:  server.HasAuth,
	}, nil
}

func buildServerInfo(servers []string, currentAddr string, tests map[string]serverTestResponse) []serverInfo {
	infos := make([]serverInfo, 0, len(servers))
	for i, server := range servers {
		info, err := parseServer(server)
		if err != nil {
			info = serverInfo{Raw: server, Addr: server}
		}
		info.Default = i == 0
		if test, ok := tests[info.Raw]; ok {
			t := test
			info.Test = &t
		}
		infos = append(infos, info)
	}

	currentIndex := -1
	for i, info := range infos {
		if currentAddr != "" && info.Addr == currentAddr {
			currentIndex = i
			break
		}
	}
	if currentIndex < 0 && len(infos) > 0 {
		currentIndex = 0
	}
	if currentIndex >= 0 {
		infos[currentIndex].Current = true
	}
	return infos
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

	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func containsServer(servers []string, server string) bool {
	for _, s := range servers {
		if s == server {
			return true
		}
	}
	return false
}

func moveServerFirst(servers []string, server string) ([]string, bool) {
	normalized, err := normalizeServer(server)
	if err != nil {
		return nil, false
	}
	result := []string{normalized}
	found := false
	for _, s := range servers {
		if s == normalized {
			found = true
			continue
		}
		result = append(result, s)
	}
	return result, found
}

func removeServer(servers []string, server string) ([]string, bool, bool) {
	normalized, err := normalizeServer(server)
	if err != nil {
		return nil, false, false
	}
	result := make([]string, 0, len(servers))
	removed := false
	wasCurrent := false
	for i, s := range servers {
		if s == normalized {
			removed = true
			wasCurrent = i == 0
			continue
		}
		result = append(result, s)
	}
	return result, removed, wasCurrent
}
