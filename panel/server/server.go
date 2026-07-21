package server

import (
	"errors"
	"fmt"
	"net/http"
	"time"
)

func Run(opts Options) error {
	listen := opts.Listen
	if listen == "" {
		listen = DefaultPanelListen
	}

	cfgPath := absPath(opts.ConfigPath)
	stPath := opts.StatePath
	if stPath == "" {
		stPath = defaultStatePath(cfgPath)
	}
	a := &app{
		configPath: cfgPath,
		statePath:  absPath(stPath),
		testPath:   absPath(defaultTestResultsPath(cfgPath)),
		ui:         opts.UI,
	}
	authStore, err := newPanelAuthStore(cfgPath)
	if err != nil {
		return fmt.Errorf("load panel auth failed: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/assets/", a.handleUIAsset)
	mux.HandleFunc("/", a.handleIndex)
	mux.HandleFunc("/login", authStore.handleLogin)
	mux.HandleFunc("/logout", authStore.handleLogout)
	mux.HandleFunc("/api/state", a.handleState)
	mux.HandleFunc("/api/servers/add", a.handleAddServer)
	mux.HandleFunc("/api/servers/select", a.handleSelectServer)
	mux.HandleFunc("/api/servers/delete", a.handleDeleteServer)
	mux.HandleFunc("/api/servers/note", a.handleUpdateServerNote)
	mux.HandleFunc("/api/servers/test", a.handleServerTest)
	mux.HandleFunc("/api/config/update", a.handleUpdateConfig)
	mux.HandleFunc("/api/restart", a.handleRestart)

	handler := http.Handler(mux)
	handler = authStore.wrap(handler)
	if auth := authStore.current(); auth.enabled() {
		fmt.Printf("turnsocks panel auth enabled for user %s\n", auth.username)
	}

	server := &http.Server{
		Addr:              listen,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	fmt.Printf("turnsocks panel listening on http://%s\n", listen)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("panel failed: %w", err)
	}
	return nil
}

func DefaultConfigPath() string {
	return defaultConfigPath()
}
