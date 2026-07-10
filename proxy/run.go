package proxy

import (
	"errors"
	"flag"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"runtime/pprof"
	"strings"
	"sync"
	"syscall"
	"time"
)

func Run() {
	cfg := Config{}
	var cpuProfile string
	var memProfile string

	configPath := defaultConfigPath()
	configPath = preFlagValue("config", getenv("CONFIG_PATH", configPath))
	configPath = absPath(configPath)
	if err := loadEnvFile(configPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Fatalf("load env config failed: %v", err)
	}

	flag.StringVar(&cfg.Listen, "listen", getenv("LISTEN", "127.0.0.1:1080"), "SOCKS5 listen address")
	flag.StringVar(&cfg.Turns, "turns", getenv("TURN_SERVERS", ""), "comma-separated TURN server addresses")
	flag.DurationVar(&cfg.TurnCooldown, "turn-cooldown", 30*time.Second, "TURN server failure cooldown")
	flag.StringVar(&cfg.ConfigPath, "config", configPath, "env config file path")
	flag.StringVar(&cfg.DoH, "doh", getenv("DOH", "https://cloudflare-dns.com/dns-query"), "DoH endpoint")
	flag.StringVar(&cfg.StatePath, "state", getenv("STATE_PATH", ""), "runtime state file path")
	flag.DurationVar(&cfg.DNSTTL, "dns-ttl", 300*time.Second, "DNS cache TTL")
	flag.DurationVar(&cfg.Timeout, "timeout", 20*time.Second, "network timeout")
	flag.BoolVar(&cfg.LogVerbose, "v", false, "verbose log")
	flag.StringVar(&cpuProfile, "cpuprofile", "", "write CPU profile to file")
	flag.StringVar(&memProfile, "memprofile", "", "write heap profile to file on exit")
	flag.Parse()

	stopProfile := startProfiling(cpuProfile, memProfile)
	defer stopProfile()

	if cfg.Timeout <= 0 {
		log.Fatal("timeout must be greater than 0")
	}
	if cfg.DNSTTL <= 0 {
		log.Fatal("dns-ttl must be greater than 0")
	}
	if cfg.TurnCooldown <= 0 {
		log.Fatal("turn-cooldown must be greater than 0")
	}
	cfg.ConfigPath = absPath(cfg.ConfigPath)
	if cfg.StatePath == "" {
		cfg.StatePath = defaultStatePath(cfg.ConfigPath)
	}
	cfg.StatePath = absPath(cfg.StatePath)
	var err error
	cfg.TurnServers, err = loadTurnServers(cfg)
	if err != nil {
		log.Fatalf("load TURN config failed: %v", err)
	}
	if len(cfg.TurnServers) == 0 {
		log.Fatal("missing TURN servers, set TURN_SERVERS in config.env")
	}
	cfg.TurnPool = newTurnPool(cfg.TurnServers, cfg.TurnCooldown, cfg.StatePath)
	cfg.TCPAllocs = newTCPAllocationPool()
	cfg.TCPAllocs.setAllowed(cfg.TurnServers)
	cfg.UDPPrewarm = newUDPPrewarmPool()
	cfg.UDPSessions = newUDPSessionRegistry()
	cfg.TurnPool.markSuccess(initialTurnServer(cfg.TurnServers, readRuntimeState(cfg.StatePath)))
	dohURL, err := url.ParseRequestURI(cfg.DoH)
	if err != nil {
		log.Fatalf("invalid DoH endpoint: %v", err)
	}
	if dohURL.Scheme != "http" && dohURL.Scheme != "https" || dohURL.Host == "" {
		log.Fatal("DoH endpoint must be an http or https URL")
	}
	cfg.DoHClient = &http.Client{Timeout: cfg.Timeout}

	go prewarmDoH(cfg)
	log.Printf("TURN servers: %s", strings.Join(turnServerAddrs(cfg.TurnServers), ", "))
	log.Printf("TURN auth: per-server inline only")
	proxy := newProxyController(cfg)
	if err := proxy.start(); err != nil {
		log.Fatalf("SOCKS5 start failed: %v", err)
	}
	go prewarmTCPAllocation(cfg)
	go prewarmUDPAllocation(cfg)
	go watchTurnConfig(cfg)
	go cleanupDNSCache(cfg.DNSTTL)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	<-signals
	proxy.stop()
	cfg.UDPPrewarm.close()
	cfg.UDPSessions.closeAll()
}

func startProfiling(cpuPath string, memPath string) func() {
	var (
		once    sync.Once
		cpuFile *os.File
	)

	if cpuPath != "" {
		f, err := os.Create(cpuPath)
		if err != nil {
			log.Fatalf("create CPU profile failed: %v", err)
		}
		cpuFile = f
		if err := pprof.StartCPUProfile(cpuFile); err != nil {
			_ = cpuFile.Close()
			log.Fatalf("start CPU profile failed: %v", err)
		}
	}

	return func() {
		once.Do(func() {
			if cpuFile != nil {
				pprof.StopCPUProfile()
				_ = cpuFile.Close()
			}
			if memPath == "" {
				return
			}
			runtime.GC()
			f, err := os.Create(memPath)
			if err != nil {
				log.Printf("create heap profile failed: %v", err)
				return
			}
			if err := pprof.WriteHeapProfile(f); err != nil {
				log.Printf("write heap profile failed: %v", err)
			}
			_ = f.Close()
		})
	}
}
