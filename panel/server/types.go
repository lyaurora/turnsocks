package server

import (
	"io/fs"
	"sync"

	"github.com/lyaurora/turnsocks/panel/probe"
	"github.com/lyaurora/turnsocks/runtimestate"
)

const (
	DefaultPanelListen = "127.0.0.1:10808"
	defaultProxyListen = "127.0.0.1:1080"
	defaultDoH         = "https://cloudflare-dns.com/dns-query"
	serviceName        = "turnsocks"
	panelSessionCookie = "turnsocks_panel_session"
	panelSessionMaxAge = 60 * 60 * 24 * 30
)

type Options struct {
	Listen     string
	ConfigPath string
	StatePath  string
	UI         fs.FS
}

type app struct {
	configPath string
	statePath  string
	testPath   string
	checkPath  string
	ui         fs.FS
	configMu   sync.Mutex
	testMu     sync.Mutex
	probeMu    sync.Mutex
}

type serverTestResponse = probe.Result

type proxyConfig struct {
	Listen        string
	Servers       []string
	ServerNotes   map[string]string
	DoH           string
	PanelUsername string
	PanelPassword string
}

type serverInfo struct {
	Raw      string              `json:"raw"`
	Addr     string              `json:"addr"`
	Note     string              `json:"note,omitempty"`
	Username string              `json:"username,omitempty"`
	HasAuth  bool                `json:"hasAuth"`
	Current  bool                `json:"current"`
	Default  bool                `json:"default"`
	Test     *serverTestResponse `json:"test,omitempty"`
	Check    *serverTestResponse `json:"check,omitempty"`
}

type serviceInfo struct {
	Active bool   `json:"active"`
	PID    string `json:"pid,omitempty"`
}

type stateResponse struct {
	Listen           string       `json:"listen"`
	DoH              string       `json:"doh"`
	PanelUsername    string       `json:"panelUsername"`
	PanelAuthEnabled bool         `json:"panelAuthEnabled"`
	Servers          []serverInfo `json:"servers"`
	Service          serviceInfo  `json:"service"`
	Runtime          runtimeState `json:"runtime"`
}

type serverRequest struct {
	Server string     `json:"server"`
	Note   string     `json:"note,omitempty"`
	Mode   probe.Mode `json:"mode,omitempty"`
}

type configRequest struct {
	Listen           string `json:"listen"`
	DoH              string `json:"doh"`
	PanelAuthEnabled bool   `json:"panelAuthEnabled"`
	PanelUsername    string `json:"panelUsername"`
	PanelPassword    string `json:"panelPassword"`
}

type apiResponse struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

type runtimeState = runtimestate.State
