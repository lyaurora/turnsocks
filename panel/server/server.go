package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/signal"
	"path/filepath"
	"syscall"
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
	authStore, err := newPanelAuthStore(cfgPath)
	if err != nil {
		return fmt.Errorf("load panel auth failed: %w", err)
	}
	a := &app{
		configPath: cfgPath,
		statePath:  turncfg.AbsPath(stPath),
		testPath:   turncfg.AbsPath(filepath.Join(filepath.Dir(cfgPath), "turnsocks.tests.json")),
		checkPath:  turncfg.AbsPath(filepath.Join(filepath.Dir(cfgPath), "turnsocks.checks.json")),
		ui:         opts.UI,
		authStore:  authStore,
	}

	if auth := authStore.current(); auth.enabled() {
		fmt.Printf("turnsocks panel auth enabled for user %s\n", auth.username)
	}

	baseCtx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	server := &http.Server{
		Addr:              listen,
		Handler:           a.handler(authStore),
		ReadHeaderTimeout: 5 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return baseCtx },
	}

	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("panel failed: %w", err)
	}
	defer listener.Close()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(listener)
	}()

	fmt.Printf("turnsocks panel listening on http://%s\n", listen)
	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("panel failed: %w", err)
		}
		return nil
	case <-baseCtx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("panel failed: %w", err)
		}
		return nil
	}
}

func (panel *app) handler(authStore *panelAuthStore) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/assets/", panel.assetHandler())
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
	return http.NewCrossOriginProtection().Handler(authStore.wrap(mux))
}

func DefaultConfigPath() string {
	return turncfg.DefaultConfigPath()
}
