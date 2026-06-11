package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultPanelListen = "127.0.0.1:10808"
	defaultProxyListen = "127.0.0.1:1080"
	defaultDoH         = "https://cloudflare-dns.com/dns-query"
	serviceName        = "turnsocks"
)

type app struct {
	configPath string
	statePath  string
	configMu   sync.Mutex
}

type proxyConfig struct {
	Listen  string
	Servers []string
	DoH     string
}

type serverInfo struct {
	Raw      string `json:"raw"`
	Addr     string `json:"addr"`
	Username string `json:"username,omitempty"`
	HasAuth  bool   `json:"hasAuth"`
	Current  bool   `json:"current"`
	Default  bool   `json:"default"`
}

type serviceInfo struct {
	Active bool   `json:"active"`
	PID    string `json:"pid,omitempty"`
}

type stateResponse struct {
	Listen  string       `json:"listen"`
	DoH     string       `json:"doh"`
	Servers []serverInfo `json:"servers"`
	Service serviceInfo  `json:"service"`
}

type serverRequest struct {
	Server string `json:"server"`
}

type apiResponse struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

type runtimeState struct {
	CurrentAddr string `json:"current_addr"`
	UpdatedAt   string `json:"updated_at"`
}

func main() {
	listen := flag.String("listen", defaultPanelListen, "panel listen address")
	configPath := flag.String("config", defaultConfigPath(), "turnsocks config.env path")
	statePath := flag.String("state", "", "turnsocks runtime state path")
	flag.Parse()

	cfgPath := absPath(*configPath)
	stPath := *statePath
	if stPath == "" {
		stPath = defaultStatePath(cfgPath)
	}
	a := &app{configPath: cfgPath, statePath: absPath(stPath)}
	mux := http.NewServeMux()
	mux.HandleFunc("/", a.handleIndex)
	mux.HandleFunc("/api/state", a.handleState)
	mux.HandleFunc("/api/servers/add", a.handleAddServer)
	mux.HandleFunc("/api/servers/select", a.handleSelectServer)
	mux.HandleFunc("/api/servers/delete", a.handleDeleteServer)
	mux.HandleFunc("/api/restart", a.handleRestart)

	server := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	fmt.Printf("turnsocks panel listening on http://%s\n", *listen)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "panel failed: %v\n", err)
		os.Exit(1)
	}
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

func (a *app) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(indexHTML))
}

func (a *app) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	a.configMu.Lock()
	cfg, err := readProxyConfig(a.configPath)
	a.configMu.Unlock()
	if err != nil {
		writeAPIError(w, err)
		return
	}
	state := readRuntimeState(a.statePath)
	writeJSON(w, stateResponse{
		Listen:  cfg.Listen,
		DoH:     cfg.DoH,
		Servers: buildServerInfo(cfg.Servers, state.CurrentAddr),
		Service: readServiceInfo(),
	})
}

func (a *app) handleAddServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	req, err := readServerRequest(r)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	server, err := normalizeServer(req.Server)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	a.configMu.Lock()
	defer a.configMu.Unlock()

	cfg, err := readProxyConfig(a.configPath)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if containsServer(cfg.Servers, server) {
		writeJSON(w, apiResponse{OK: true, Message: "节点已存在"})
		return
	}
	cfg.Servers = append(cfg.Servers, server)
	if err := writeProxyConfig(a.configPath, cfg); err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, apiResponse{OK: true, Message: "已添加节点"})
}

func (a *app) handleSelectServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	req, err := readServerRequest(r)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	a.configMu.Lock()
	defer a.configMu.Unlock()

	cfg, err := readProxyConfig(a.configPath)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	servers, ok := moveServerFirst(cfg.Servers, req.Server)
	if !ok {
		writeAPIError(w, errors.New("节点不存在"))
		return
	}
	cfg.Servers = servers
	if err := writeProxyConfig(a.configPath, cfg); err != nil {
		writeAPIError(w, err)
		return
	}
	if err := restartTurnsocks(); err != nil {
		writeAPIError(w, fmt.Errorf("已保存，但重启失败：%w", err))
		return
	}
	writeJSON(w, apiResponse{OK: true, Message: "已切换并重启"})
}

func (a *app) handleDeleteServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	req, err := readServerRequest(r)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	a.configMu.Lock()
	defer a.configMu.Unlock()

	cfg, err := readProxyConfig(a.configPath)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	servers, removed, wasCurrent := removeServer(cfg.Servers, req.Server)
	if !removed {
		writeAPIError(w, errors.New("节点不存在"))
		return
	}
	if len(servers) == 0 {
		writeAPIError(w, errors.New("至少保留一个 TURN 节点"))
		return
	}
	cfg.Servers = servers
	if err := writeProxyConfig(a.configPath, cfg); err != nil {
		writeAPIError(w, err)
		return
	}
	if err := restartTurnsocks(); err != nil {
		writeAPIError(w, fmt.Errorf("已删除，但重启失败：%w", err))
		return
	}
	if wasCurrent {
		writeJSON(w, apiResponse{OK: true, Message: "已删除当前节点并重启"})
		return
	}
	writeJSON(w, apiResponse{OK: true, Message: "已删除节点并重启"})
}

func (a *app) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if err := restartTurnsocks(); err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, apiResponse{OK: true, Message: "已重启代理"})
}

func readServerRequest(r *http.Request) (serverRequest, error) {
	defer r.Body.Close()
	var req serverRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4*1024)).Decode(&req); err != nil {
		return req, errors.New("请求格式错误")
	}
	req.Server = strings.TrimSpace(req.Server)
	if req.Server == "" {
		return req, errors.New("节点不能为空")
	}
	return req, nil
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

	data := "# 本地 SOCKS5 监听地址\n" +
		"LISTEN=" + cfg.Listen + "\n\n" +
		"# TURN 节点，多个用英文逗号分隔，支持 user:pass@host:port\n" +
		"TURN_SERVERS=" + strings.Join(cfg.Servers, ",") + "\n\n" +
		"# DoH DNS\n" +
		"DOH=" + cfg.DoH + "\n"

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(data), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return serverInfo{}, errors.New("节点不能为空")
	}
	info := serverInfo{Raw: raw}
	addr := raw
	if at := strings.LastIndex(raw, "@"); at >= 0 {
		cred := raw[:at]
		addr = raw[at+1:]
		user, _, ok := strings.Cut(cred, ":")
		if !ok || user == "" {
			return serverInfo{}, errors.New("鉴权格式应为 user:pass@host:port")
		}
		info.Username = user
		info.HasAuth = true
	}
	addr = strings.TrimSpace(addr)
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" || port == "" {
		return serverInfo{}, errors.New("节点格式应为 host:port")
	}
	portNum, err := strconv.Atoi(port)
	if err != nil || portNum <= 0 || portNum > 65535 {
		return serverInfo{}, errors.New("端口必须是 1-65535")
	}
	info.Addr = addr
	if info.HasAuth {
		cred := raw[:strings.LastIndex(raw, "@")]
		info.Raw = cred + "@" + addr
	} else {
		info.Raw = addr
	}
	return info, nil
}

func buildServerInfo(servers []string, currentAddr string) []serverInfo {
	infos := make([]serverInfo, 0, len(servers))
	for i, server := range servers {
		info, err := parseServer(server)
		if err != nil {
			info = serverInfo{Raw: server, Addr: server}
		}
		info.Default = i == 0
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

func readServiceInfo() serviceInfo {
	active := strings.TrimSpace(commandOutput(2*time.Second, "systemctl", "is-active", serviceName)) == "active"
	pid := strings.TrimSpace(commandOutput(2*time.Second, "pgrep", "-x", serviceName))
	if idx := strings.IndexByte(pid, '\n'); idx >= 0 {
		pid = pid[:idx]
	}
	return serviceInfo{Active: active, PID: pid}
}

func restartTurnsocks() error {
	if err := runCommand(10*time.Second, "sudo", "-n", "systemctl", "restart", serviceName); err != nil {
		return fmt.Errorf("重启 %s 失败：%w", serviceName, err)
	}
	return waitTurnsocksReady(8 * time.Second)
}

func waitTurnsocksReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.TrimSpace(commandOutput(2*time.Second, "systemctl", "is-active", serviceName)) == "active" {
			return nil
		}
		if strings.TrimSpace(commandOutput(2*time.Second, "pgrep", "-x", serviceName)) != "" {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("%s 未恢复运行", serviceName)
}

func runCommand(timeout time.Duration, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.Run()
}

func commandOutput(timeout time.Duration, name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

func writeMethodNotAllowed(w http.ResponseWriter) {
	w.Header().Set("Allow", "GET, POST")
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func writeAPIError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	writeJSON(w, apiResponse{OK: false, Message: err.Error()})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

const indexHTML = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>turnsocks 面板</title>
  <style>
    :root {
      color-scheme: light;
      --background: 42 20% 95%;
      --foreground: 195 16% 16%;
      --card: 40 29% 98%;
      --muted: 42 17% 90%;
      --muted-foreground: 200 11% 38%;
      --primary: 184 27% 25%;
      --primary-foreground: 42 20% 95%;
      --accent: 48 21% 88%;
      --border: 36 15% 78%;
      --input: 36 15% 82%;
      --ring: 184 27% 25%;
      --warn: 35 82% 44%;
      --danger: 5 62% 48%;
      --ok: 159 31% 31%;
      --radius: .95rem;
    }
    html.dark {
      color-scheme: dark;
      --background: 200 17% 10%;
      --foreground: 42 20% 92%;
      --card: 195 16% 12%;
      --muted: 196 12% 17%;
      --muted-foreground: 42 12% 68%;
      --primary: 167 29% 62%;
      --primary-foreground: 196 18% 10%;
      --accent: 196 12% 17%;
      --border: 196 10% 24%;
      --input: 196 10% 24%;
      --ring: 167 29% 62%;
      --warn: 38 78% 56%;
      --danger: 6 73% 63%;
      --ok: 159 39% 62%;
    }
    * { box-sizing: border-box; }
    html, body { min-height: 100%; }
    body {
      margin: 0;
      background-color: hsl(var(--background));
      color: hsl(var(--foreground));
      font-family: system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", "Noto Sans CJK SC", "Noto Sans SC", "PingFang SC", "Microsoft YaHei", sans-serif;
      -webkit-font-smoothing: antialiased;
      background-image:
        radial-gradient(circle at top left, rgba(24, 92, 87, .08), transparent 32%),
        linear-gradient(rgba(67, 73, 61, .06) 1px, transparent 1px),
        linear-gradient(90deg, rgba(67, 73, 61, .06) 1px, transparent 1px);
      background-size: auto, 26px 26px, 26px 26px;
      background-attachment: fixed;
      font-feature-settings: 'rlig' 1, 'calt' 1;
      font-synthesis: none;
      text-rendering: optimizeLegibility;
    }
    body::before {
      content: '';
      position: fixed;
      inset: 0;
      pointer-events: none;
      opacity: .08;
      background-image:
        linear-gradient(rgba(255,255,255,.16), rgba(255,255,255,.04)),
        radial-gradient(circle at 20% 10%, rgba(0,0,0,.08) .5px, transparent .75px);
      background-size: 100% 100%, 8px 8px;
      mix-blend-mode: multiply;
    }
    button, input { font: inherit; }
    strong { font-weight: 600; }
    .app {
      width: min(1180px, calc(100% - 28px));
      margin: 0 auto;
      padding: 24px 0 40px;
      position: relative;
      z-index: 1;
    }
    .top {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 14px;
      margin-bottom: 18px;
    }
    .brand {
      margin: 0;
      font-family: system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", "Noto Sans CJK SC", "Noto Sans SC", "PingFang SC", "Microsoft YaHei", sans-serif;
      font-size: clamp(30px, 5vw, 48px);
      line-height: 1;
      font-weight: 700;
      letter-spacing: -.03em;
    }
    .top-actions { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; justify-content: flex-end; }
    .top-actions .btn {
      height: 30px;
      min-height: 30px;
      padding: 0 11px;
      font-size: 12px;
      font-family: ui-monospace, "SFMono-Regular", "Cascadia Mono", "Noto Sans Mono CJK SC", Consolas, Menlo, monospace;
      letter-spacing: .04em;
    }
    .shell-window {
      overflow: hidden;
      border-radius: 1.35rem;
      border: 1px solid hsl(var(--border));
      background: hsl(var(--card) / .92);
      box-shadow: 0 0 0 1px rgba(255,255,255,.35), 0 24px 60px rgba(57,63,51,.08);
    }
    .shell-bar {
      min-height: 44px;
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 12px;
      border-bottom: 1px solid hsl(var(--border));
      background: hsl(var(--muted) / .70);
      padding: 10px 16px;
    }
    .shell-dots { display: flex; align-items: center; gap: 6px; }
    .shell-dot { width: 10px; height: 10px; border-radius: 999px; border: 1px solid rgba(0,0,0,.1); }
    .shell-dot-rose { background: rgba(195,102,90,.7); }
    .shell-dot-amber { background: rgba(199,151,66,.72); }
    .shell-dot-mint { background: rgba(84,143,118,.75); }
    .shell-chip, .status-pill, .field-label {
      font-family: ui-monospace, "SFMono-Regular", "Cascadia Mono", "Noto Sans Mono CJK SC", Consolas, Menlo, monospace;
      font-size: 11px;
      text-transform: uppercase;
      letter-spacing: .12em;
    }
    .shell-chip {
      min-height: 30px;
      display: inline-flex;
      align-items: center;
      gap: 7px;
      border-radius: 999px;
      border: 1px solid hsl(var(--border));
      background: hsl(var(--card) / .9);
      padding: 0 11px;
      color: hsl(var(--muted-foreground));
      white-space: nowrap;
    }
    .status-pill {
      min-height: 30px;
      display: inline-flex;
      align-items: center;
      gap: 7px;
      border-radius: 999px;
      border: 1px solid hsl(var(--border));
      background: hsl(var(--muted) / .70);
      padding: 0 11px;
      color: hsl(var(--muted-foreground));
      white-space: nowrap;
    }
    .status-pill-ok { border-color: hsl(var(--ok) / .30); background: hsl(var(--ok) / .10); color: hsl(var(--ok)); }
    .status-pill-warn { border-color: hsl(var(--warn) / .30); background: hsl(var(--warn) / .10); color: hsl(var(--warn)); }
    .status-pill-danger { border-color: hsl(var(--danger) / .30); background: hsl(var(--danger) / .10); color: hsl(var(--danger)); }
    .dot { width: 8px; height: 8px; border-radius: 999px; background: currentColor; }
    .panel-body { padding: 18px; }
    .dashboard {
      display: grid;
      grid-template-columns: minmax(0, 1.2fr) minmax(320px, .8fr);
      gap: 18px;
      align-items: start;
    }
    .current-card {
      min-height: 178px;
      position: relative;
      overflow: hidden;
      border-radius: 1.35rem;
      border: 1px solid hsl(var(--border));
      background: hsl(var(--card) / .90);
      box-shadow: 0 24px 60px rgba(57,63,51,.08);
      padding: 22px 24px;
      display: flex;
      flex-direction: column;
      justify-content: center;
    }
    .current-card::before {
      content: '';
      position: absolute;
      inset: 0;
      pointer-events: none;
      background:
        linear-gradient(120deg, hsl(var(--primary) / .08), transparent 42%),
        repeating-linear-gradient(90deg, transparent 0 21px, hsl(var(--border) / .35) 22px);
      opacity: .65;
    }
    .current-card > * { position: relative; }
    .field-label { color: hsl(var(--muted-foreground)); letter-spacing: .22em; }
    .current-title {
      margin: 12px 0 0;
      font-family: system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", "Noto Sans CJK SC", "Noto Sans SC", "PingFang SC", "Microsoft YaHei", sans-serif;
      font-size: clamp(26px, 3.4vw, 42px);
      line-height: 1.12;
      font-weight: 500;
      letter-spacing: 0;
      overflow-wrap: anywhere;
    }
    .current-meta { margin-top: 14px; display: flex; flex-wrap: wrap; gap: 8px; color: hsl(var(--muted-foreground)); }
    .side-stack { display: grid; gap: 18px; }
    .form { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 10px; }
    input {
      width: 100%;
      min-height: 42px;
      border-radius: calc(var(--radius) - 2px);
      border: 1px solid hsl(var(--input));
      background: hsl(var(--card) / .85);
      color: hsl(var(--foreground));
      outline: none;
      padding: 0 14px;
      font-family: ui-monospace, "SFMono-Regular", "Cascadia Mono", "Noto Sans Mono CJK SC", Consolas, Menlo, monospace;
      font-size: 13px;
    }
    input:focus { border-color: hsl(var(--ring)); box-shadow: 0 0 0 3px hsl(var(--ring) / .16); }
    .btn {
      height: 34px;
      min-height: 34px;
      display: inline-flex;
      align-items: center;
      justify-content: center;
      gap: 7px;
      border-radius: 999px;
      border: 1px solid hsl(var(--border));
      background: hsl(var(--card) / .85);
      color: hsl(var(--foreground));
      padding: 0 13px;
      cursor: pointer;
      font-size: 13px;
      font-weight: 500;
      text-decoration: none;
      white-space: nowrap;
    }
    .btn:hover { border-color: hsl(var(--foreground) / .22); background: hsl(var(--accent) / .70); }
    .btn.primary { border-color: hsl(var(--primary)); background: hsl(var(--primary)); color: hsl(var(--primary-foreground)); }
    .btn.secondary { background: hsl(var(--muted) / .70); color: hsl(var(--muted-foreground)); }
    .btn.danger { color: hsl(var(--danger)); }
    .btn:disabled { opacity: .55; cursor: wait; }
    .kv-grid { display: grid; gap: 10px; }
    .kv {
      border-radius: 1rem;
      border: 1px solid hsl(var(--border));
      background: hsl(var(--muted) / .45);
      padding: 14px;
    }
    .kv-value { margin-top: 8px; font-family: ui-monospace, "SFMono-Regular", "Cascadia Mono", "Noto Sans Mono CJK SC", Consolas, Menlo, monospace; font-size: 14px; line-height: 1.55; overflow-wrap: anywhere; }
    .nodes-card { margin-top: 18px; }
    .nodes { display: grid; gap: 12px; }
    .node-row {
      border-radius: 1rem;
      border: 1px solid hsl(var(--border));
      background: hsl(var(--card) / .80);
      padding: 16px;
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      gap: 14px;
      align-items: center;
    }
    .node-row.current { border-color: hsl(var(--ok) / .35); background: hsl(var(--ok) / .06); }
    .addr { font-family: ui-monospace, "SFMono-Regular", "Cascadia Mono", "Noto Sans Mono CJK SC", Consolas, Menlo, monospace; font-size: 14px; font-weight: 400; line-height: 1.55; overflow-wrap: anywhere; }
    .meta { margin-top: 10px; display: flex; flex-wrap: wrap; gap: 7px; }
    .actions { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 8px; }
    .theme-toggle {
      display: inline-flex;
      align-items: center;
      border-radius: 999px;
      border: 1px solid hsl(var(--border));
      background: hsl(var(--muted) / .8);
      padding: 4px;
      box-shadow: inset 0 1px 0 rgba(255,255,255,.35);
    }
    .theme-toggle button {
      border: 0;
      border-radius: 999px;
      background: transparent;
      color: hsl(var(--muted-foreground));
      padding: 6px 9px;
      cursor: pointer;
      font-family: ui-monospace, "SFMono-Regular", "Cascadia Mono", "Noto Sans Mono CJK SC", Consolas, Menlo, monospace;
      font-size: 11px;
      white-space: nowrap;
    }
    .theme-toggle button.active { background: hsl(var(--card)); color: hsl(var(--foreground)); box-shadow: 0 1px 3px rgba(0,0,0,.08); }
    .toast {
      position: fixed;
      left: 50%;
      bottom: 18px;
      z-index: 50;
      max-width: min(560px, calc(100% - 28px));
      transform: translateX(-50%);
      border-radius: 1rem;
      border: 1px solid hsl(var(--border));
      background: hsl(var(--card) / .96);
      box-shadow: 0 24px 60px rgba(57,63,51,.16);
      color: hsl(var(--foreground));
      padding: 12px 16px;
      opacity: 0;
      pointer-events: none;
      transition: opacity .18s ease, bottom .18s ease;
    }
    .toast.show { opacity: 1; bottom: 28px; }
    @media (max-width: 860px) {
      .app { width: min(100% - 20px, 1180px); padding-top: 16px; }
      .top { align-items: flex-start; flex-direction: column; }
      .top-actions { justify-content: flex-start; }
      .dashboard { grid-template-columns: 1fr; }
      .current-card { min-height: 168px; padding: 20px; }
      .node-row { grid-template-columns: 1fr; }
      .actions { justify-content: stretch; }
      .actions .btn, .form .btn { flex: 1; }
    }
    @media (max-width: 560px) {
      .form { grid-template-columns: 1fr; }
      .shell-bar { align-items: flex-start; flex-direction: column; }
      .top-actions { width: 100%; }
      .theme-toggle { width: 100%; display: grid; grid-template-columns: repeat(3, 1fr); }
      .theme-toggle button { justify-content: center; }
    }
  </style>
</head>
<body>
  <main class="app">
    <header class="top">
      <h1 class="brand">turnsocks</h1>
      <div class="top-actions">
        <span class="status-pill" id="servicePill"><span class="dot"></span><span id="serviceText">loading</span></span>
        <button class="btn secondary" id="restartBtn" type="button">重启代理</button>
        <div class="theme-toggle" role="group" aria-label="主题切换">
          <button id="themeLight" type="button">浅色</button>
          <button id="themeSystem" type="button">跟随</button>
          <button id="themeDark" type="button">深色</button>
        </div>
      </div>
    </header>

    <section class="dashboard">
      <div class="current-card">
        <div class="field-label">current turn</div>
        <div class="current-title" id="currentValue">-</div>
        <div class="current-meta">
          <span class="shell-chip" id="currentAuth">-</span>
          <span class="shell-chip" id="listenValue">-</span>
          <span class="shell-chip" id="countValue">-</span>
        </div>
      </div>

      <div class="side-stack">
        <section class="shell-window">
          <div class="shell-bar">
            <div class="shell-dots">
              <span class="shell-dot shell-dot-rose"></span>
              <span class="shell-dot shell-dot-amber"></span>
              <span class="shell-dot shell-dot-mint"></span>
            </div>
            <span class="shell-chip">add</span>
          </div>
          <div class="panel-body">
            <div class="field-label">添加 TURN 节点</div>
            <div class="form" style="margin-top:10px">
              <input id="serverInput" placeholder="host:port 或 user:pass@host:port" autocomplete="off">
              <button class="btn primary" id="addBtn" type="button">添加</button>
            </div>
          </div>
        </section>

        <section class="shell-window">
          <div class="shell-bar">
            <strong>概览</strong>
            <span class="shell-chip" id="pidValue">-</span>
          </div>
          <div class="panel-body kv-grid">
            <div class="kv"><div class="field-label">DoH</div><div class="kv-value" id="dohValue">-</div></div>
            <div class="kv"><div class="field-label">当前时间</div><div class="kv-value" id="timeValue">-</div></div>
          </div>
        </section>
      </div>
    </section>

    <section class="shell-window nodes-card">
      <div class="shell-bar">
        <strong>节点池</strong>
        <span class="shell-chip">第一个为默认节点</span>
      </div>
      <div class="panel-body">
        <div id="nodes" class="nodes"></div>
      </div>
    </section>
  </main>
  <div id="toast" class="toast"></div>

  <script>
    const $ = (id) => document.getElementById(id);
    let busy = false;

    function prefersDark() { return window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches; }
    function applyTheme(mode) {
      const realMode = mode === 'system' ? (prefersDark() ? 'dark' : 'light') : mode;
      document.documentElement.classList.toggle('dark', realMode === 'dark');
      localStorage.setItem('turnsocks-theme', mode);
      $('themeLight').classList.toggle('active', mode === 'light');
      $('themeSystem').classList.toggle('active', mode === 'system');
      $('themeDark').classList.toggle('active', mode === 'dark');
    }
    function initTheme() {
      applyTheme(localStorage.getItem('turnsocks-theme') || 'system');
      $('themeLight').addEventListener('click', () => applyTheme('light'));
      $('themeSystem').addEventListener('click', () => applyTheme('system'));
      $('themeDark').addEventListener('click', () => applyTheme('dark'));
      if (window.matchMedia) window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
        if ((localStorage.getItem('turnsocks-theme') || 'system') === 'system') applyTheme('system');
      });
    }
    function updateClock() {
      $('timeValue').textContent = new Date().toLocaleString('zh-CN', { hour12: false });
    }
    function toast(message) {
      const el = $('toast');
      el.textContent = message;
      el.classList.add('show');
      clearTimeout(window.__toastTimer);
      window.__toastTimer = setTimeout(() => el.classList.remove('show'), 2200);
    }
    async function api(path, body) {
      const res = await fetch(path, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body || {}) });
      const data = await res.json().catch(() => ({ ok: false, message: '请求失败' }));
      if (!res.ok || data.ok === false) throw new Error(data.message || '操作失败');
      return data;
    }
    function escapeHTML(s) { return String(s).replace(/[&<>"]/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c])); }
    function escapeAttr(s) { return escapeHTML(s).replace(/'/g, '&#39;'); }
    function displayHost(node) {
      if (!node) return '-';
      const addr = node.addr || node.raw || '';
      if (addr.startsWith('[')) {
        const end = addr.indexOf(']');
        if (end > 0) return addr.slice(1, end);
      }
      const idx = addr.lastIndexOf(':');
      return idx > 0 ? addr.slice(0, idx) : addr;
    }
    function nodeHTML(node) {
      if (!node) return '<div class="addr">暂无节点</div>';
      const status = node.current ? '<span class="status-pill status-pill-ok">运行中</span>' : (node.default ? '<span class="status-pill">默认</span>' : '<span class="status-pill">备用</span>');
      const auth = node.hasAuth ? '鉴权：' + escapeHTML(node.username || '已配置') : '无鉴权';
      const selectBtn = node.default ? '' : '<button class="btn primary" data-action="select" data-server="' + escapeAttr(node.raw) + '">设为默认</button>';
      return '<div><div class="addr">' + escapeHTML(node.raw) + '</div><div class="meta">' + status + '<span class="shell-chip">' + escapeHTML(auth) + '</span></div></div><div class="actions">' + selectBtn + '<button class="btn danger" data-action="delete" data-server="' + escapeAttr(node.raw) + '">删除</button></div>';
    }
    async function refresh() {
      const res = await fetch('/api/state');
      const data = await res.json();
      const current = data.servers.find(n => n.current) || data.servers[0];
      const active = !!data.service.active;
      $('servicePill').className = 'status-pill ' + (active ? 'status-pill-warn' : 'status-pill-danger');
      $('serviceText').textContent = active ? 'running' : 'stopped';
      $('currentValue').textContent = displayHost(current);
      $('currentAuth').textContent = current ? (current.hasAuth ? '已配置鉴权' : '无鉴权') : '-';
      $('listenValue').textContent = data.listen || '-';
      $('countValue').textContent = data.servers.length + ' 节点';
      $('pidValue').textContent = data.service.pid ? 'pid ' + data.service.pid : 'no pid';
      $('dohValue').textContent = data.doh || '-';
      $('nodes').innerHTML = data.servers.map(n => '<div class="node-row ' + (n.current ? 'current' : '') + '">' + nodeHTML(n) + '</div>').join('') || '<div class="node-row"><div class="addr">暂无节点</div></div>';
    }
    function sleep(ms) { return new Promise(resolve => setTimeout(resolve, ms)); }
    function errorMessage(err) { return err && err.message === 'Failed to fetch' ? '连接面板失败' : (err.message || '操作失败'); }
    async function refreshWithRetry() {
      for (let i = 0; i < 4; i++) {
        try { await refresh(); return; }
        catch (err) { if (i === 3) throw err; }
        await sleep(450 * (i + 1));
      }
    }
    async function run(fn) {
      if (busy) return;
      busy = true;
      document.querySelectorAll('button').forEach(b => b.disabled = true);
      try {
        const res = await fn();
        toast(res.message || '完成');
        refreshWithRetry().catch(() => setTimeout(() => refresh().catch(() => {}), 2500));
      }
      catch (err) { toast(errorMessage(err)); }
      finally { busy = false; document.querySelectorAll('button').forEach(b => b.disabled = false); }
    }
    $('addBtn').addEventListener('click', () => run(async () => {
      const input = $('serverInput');
      const server = input.value.trim();
      const res = await api('/api/servers/add', { server });
      input.value = '';
      return res;
    }));
    $('restartBtn').addEventListener('click', () => run(() => api('/api/restart')));
    $('serverInput').addEventListener('keydown', e => { if (e.key === 'Enter') $('addBtn').click(); });
    $('nodes').addEventListener('click', e => {
      const btn = e.target.closest('button[data-action]');
      if (!btn) return;
      const server = btn.dataset.server;
      if (btn.dataset.action === 'select') run(() => api('/api/servers/select', { server }));
      if (btn.dataset.action === 'delete') run(() => api('/api/servers/delete', { server }));
    });
    initTheme();
    updateClock();
    setInterval(updateClock, 1000);
    refresh().catch(err => toast(err.message || '读取失败'));
    setInterval(() => { if (!busy) refresh().catch(() => {}); }, 5000);
  </script>
</body>
</html>`
