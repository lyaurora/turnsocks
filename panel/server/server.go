package server

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/lyaurora/turnsocks/turncfg"
)

func Run(opts Options) error {
	listen := opts.Listen
	if listen == "" {
		listen = DefaultPanelListen
	}

	cfgPath := turncfg.AbsPath(opts.ConfigPath)
	stPath := opts.StatePath
	if stPath == "" {
		stPath = filepath.Join(filepath.Dir(cfgPath), "turnsocks.state")
	}
	a := &app{
		configPath: cfgPath,
		statePath:  turncfg.AbsPath(stPath),
		testPath:   turncfg.AbsPath(filepath.Join(filepath.Dir(cfgPath), "turnsocks.tests.json")),
		checkPath:  filepath.Join(filepath.Dir(cfgPath), "turnsocks.checks.json"),
		ui:         opts.UI,
	}
	authStore, err := newPanelAuthStore(cfgPath)
	if err != nil {
		return fmt.Errorf("load panel auth failed: %w", err)
	}

	if auth := authStore.current(); auth.enabled() {
		fmt.Printf("turnsocks panel auth enabled for user %s\n", auth.username)
	}

	server := &http.Server{
		Addr:              listen,
		Handler:           a.handler(authStore),
		ReadHeaderTimeout: 5 * time.Second,
	}

	fmt.Printf("turnsocks panel listening on http://%s\n", listen)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("panel failed: %w", err)
	}
	return nil
}

func (panel *app) handler(authStore *panelAuthStore) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/assets/", panel.handleUIAsset)
	mux.HandleFunc("/{$}", panel.handleIndex)
	mux.HandleFunc("/login", authStore.handleLogin)
	mux.HandleFunc("/logout", authStore.handleLogout)
	mux.HandleFunc("GET /api/state", panel.handleState)
	mux.HandleFunc("POST /api/servers/add", panel.handleAddServer)
	mux.HandleFunc("POST /api/servers/select", panel.handleSelectServer)
	mux.HandleFunc("POST /api/servers/delete", panel.handleDeleteServer)
	mux.HandleFunc("POST /api/servers/note", panel.handleUpdateServerNote)
	mux.HandleFunc("POST /api/servers/test", panel.handleServerTest)
	mux.HandleFunc("POST /api/config/update", panel.handleUpdateConfig)
	mux.HandleFunc("POST /api/restart", panel.handleRestart)
	return authStore.wrap(mux)
}

func DefaultConfigPath() string {
	return turncfg.DefaultConfigPath()
}
