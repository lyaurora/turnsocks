package main

import (
	"embed"
	"flag"
	"fmt"
	"os"

	"github.com/lyaurora/turnsocks/panel/server"
)

//go:embed ui/dist
var panelUI embed.FS

func main() {
	listen := flag.String("listen", server.DefaultPanelListen, "panel listen address")
	configPath := flag.String("config", server.DefaultConfigPath(), "turnsocks config.env path")
	statePath := flag.String("state", "", "turnsocks runtime state path")
	flag.Parse()

	if err := server.Run(server.Options{
		Listen:     *listen,
		ConfigPath: *configPath,
		StatePath:  *statePath,
		UI:         panelUI,
	}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
