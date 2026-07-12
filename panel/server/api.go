package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

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
	tests := a.readServerTests()
	writeJSON(w, stateResponse{
		Listen:           cfg.Listen,
		DoH:              cfg.DoH,
		PanelUsername:    cfg.PanelUsername,
		PanelAuthEnabled: cfg.PanelUsername != "" && cfg.PanelPassword != "",
		Servers:          buildServerInfo(cfg.Servers, state.CurrentAddr, tests),
		Service:          readServiceInfo(),
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
	writeJSON(w, apiResponse{OK: true, Message: "节点已添加"})
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
	cfg, err := readProxyConfig(a.configPath)
	if err != nil {
		a.configMu.Unlock()
		writeAPIError(w, err)
		return
	}
	servers, ok := moveServerFirst(cfg.Servers, req.Server)
	if !ok {
		a.configMu.Unlock()
		writeAPIError(w, errors.New("节点不存在"))
		return
	}
	selected, err := parseServer(servers[0])
	if err != nil {
		a.configMu.Unlock()
		writeAPIError(w, err)
		return
	}
	cfg.Servers = servers
	if err := writeProxyConfig(a.configPath, cfg); err != nil {
		a.configMu.Unlock()
		writeAPIError(w, err)
		return
	}
	if err := writeRuntimeState(a.statePath, selected.Addr); err != nil {
		a.configMu.Unlock()
		writeAPIError(w, fmt.Errorf("节点顺序已保存，但状态写入失败：%w", err))
		return
	}
	a.configMu.Unlock()
	if err := restartTurnsocks(cfg.Listen); err != nil {
		writeAPIError(w, fmt.Errorf("节点顺序已保存，但代理重启失败：%w", err))
		return
	}
	writeJSON(w, apiResponse{OK: true, Message: "节点已切换，代理已重启"})
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
	a.deleteServerTest(req.Server)
	if wasCurrent {
		writeJSON(w, apiResponse{OK: true, Message: "当前节点已删除，新连接将使用其他节点"})
		return
	}
	writeJSON(w, apiResponse{OK: true, Message: "节点已删除"})
}

func (a *app) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
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
	if err := restartTurnsocks(cfg.Listen); err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, apiResponse{OK: true, Message: "代理已重启"})
}

func (a *app) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	req, err := readConfigRequest(r)
	if err != nil {
		writeAPIError(w, err)
		return
	}

	a.configMu.Lock()
	cfg, err := readProxyConfig(a.configPath)
	a.configMu.Unlock()
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if cfg.DoH != req.DoH {
		if err := checkDoHEndpoint(req.DoH); err != nil {
			writeAPIError(w, fmt.Errorf("DoH 不可用：%w", err))
			return
		}
	}

	a.configMu.Lock()
	cfg, err = readProxyConfig(a.configPath)
	if err != nil {
		a.configMu.Unlock()
		writeAPIError(w, err)
		return
	}
	restartNeeded := cfg.Listen != req.Listen || cfg.DoH != req.DoH
	cfg.Listen = req.Listen
	cfg.DoH = req.DoH
	if req.PanelAuthEnabled {
		cfg.PanelUsername = req.PanelUsername
		if req.PanelPassword != "" {
			cfg.PanelPassword = req.PanelPassword
		}
		if cfg.PanelPassword == "" {
			a.configMu.Unlock()
			writeAPIError(w, errors.New("启用面板登录时必须设置密码"))
			return
		}
	} else {
		cfg.PanelUsername = ""
		cfg.PanelPassword = ""
	}

	if err := writeProxyConfig(a.configPath, cfg); err != nil {
		a.configMu.Unlock()
		writeAPIError(w, err)
		return
	}
	a.configMu.Unlock()
	if restartNeeded {
		if err := restartTurnsocks(cfg.Listen); err != nil {
			writeAPIError(w, fmt.Errorf("配置已保存，但代理重启失败：%w", err))
			return
		}
		writeJSON(w, apiResponse{OK: true, Message: "配置已保存，代理已重启"})
		return
	}
	writeJSON(w, apiResponse{OK: true, Message: "配置已保存"})
}

func (a *app) handleServerTest(w http.ResponseWriter, r *http.Request) {
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
	cfg, err := readProxyConfig(a.configPath)
	a.configMu.Unlock()
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if !containsServer(cfg.Servers, server) {
		writeAPIError(w, errors.New("节点不存在"))
		return
	}

	info, err := parseServer(server)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	result := a.testServer(r.Context(), server, info, cfg.DoH)
	result.TestedAt = time.Now().Format(time.RFC3339)
	if err := a.saveServerTest(server, result); err != nil {
		result.Message += "，但保存失败：" + err.Error()
	}
	writeJSON(w, result)
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

func readConfigRequest(r *http.Request) (configRequest, error) {
	defer r.Body.Close()
	var req configRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 8*1024)).Decode(&req); err != nil {
		return req, errors.New("请求格式错误")
	}
	req.Listen = strings.TrimSpace(req.Listen)
	req.DoH = strings.TrimSpace(req.DoH)
	req.PanelUsername = strings.TrimSpace(req.PanelUsername)
	req.PanelPassword = strings.TrimSpace(req.PanelPassword)
	if err := validateListenAddr(req.Listen); err != nil {
		return req, err
	}
	normalizedDoH, err := normalizeDoHURL(req.DoH)
	if err != nil {
		return req, err
	}
	req.DoH = normalizedDoH
	for name, value := range map[string]string{
		"LISTEN":         req.Listen,
		"DOH":            req.DoH,
		"PANEL_USERNAME": req.PanelUsername,
		"PANEL_PASSWORD": req.PanelPassword,
	} {
		if strings.ContainsAny(value, "\r\n") {
			return req, fmt.Errorf("%s 不能包含换行", name)
		}
	}
	if req.PanelAuthEnabled && req.PanelUsername == "" {
		return req, errors.New("启用面板登录时必须设置用户名")
	}
	return req, nil
}

func validateListenAddr(addr string) error {
	if addr == "" {
		return errors.New("SOCKS5 监听地址不能为空")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return errors.New("SOCKS5 监听地址应为 host:port")
	}
	if host != "" && net.ParseIP(host) == nil {
		if _, err := net.LookupPort("tcp", port); err != nil {
			return errors.New("SOCKS5 监听端口无效")
		}
	}
	portNum, err := strconv.Atoi(port)
	if err != nil || portNum <= 0 || portNum > 65535 {
		return errors.New("SOCKS5 监听端口必须是 1-65535")
	}
	return nil
}

func validateDoHURL(raw string) error {
	if raw == "" {
		return errors.New("DoH 不能为空")
	}
	u, err := url.ParseRequestURI(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("DoH 应为 http/https URL")
	}
	return nil
}

func normalizeDoHURL(raw string) (string, error) {
	if err := validateDoHURL(raw); err != nil {
		return "", err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if strings.EqualFold(u.Hostname(), "dns.google") && strings.TrimRight(u.EscapedPath(), "/") == "/resolve" {
		u.Path = "/dns-query"
		u.RawPath = ""
	}
	return u.String(), nil
}
