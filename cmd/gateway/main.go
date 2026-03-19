package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"linkwork/mcp-gateway/internal/compat"
	"linkwork/mcp-gateway/internal/config"
	"linkwork/mcp-gateway/internal/dns"
	"linkwork/mcp-gateway/internal/health"
	"linkwork/mcp-gateway/internal/proxy"
	"linkwork/mcp-gateway/internal/registry"
	"linkwork/mcp-gateway/internal/task"
	"linkwork/mcp-gateway/internal/tools"
	"linkwork/mcp-gateway/internal/usage"
	"linkwork/mcp-gateway/internal/user"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})
	defer rdb.Close()

	if err := rdb.Ping(ctx).Err(); err != nil {
		slog.Warn("redis connection failed, usage recording will be impaired", "error", err)
	} else {
		slog.Info("redis connected", "addr", cfg.Redis.Addr)
	}

	dnsResolver := dns.NewZonedResolver(dns.ResolverConfig{
		InternalDNS:  cfg.DNS.Internal,
		OfficeDNS:    cfg.DNS.Office,
		VirtualHosts: cfg.DNS.VirtualHosts,
	})

	regCache := registry.NewCache(cfg.WebService.BaseURL, cfg.WebService.RegistrySyncInterval)
	go regCache.Start(ctx)

	taskValidator := task.NewValidator(cfg.WebService.BaseURL, cfg.WebService.TaskValidateTTL)

	userConfigCache := user.NewConfigCache(cfg.WebService.BaseURL)

	usageRecorder := usage.NewRecorder(rdb)
	go usageRecorder.Start(ctx)

	buildTransport := func(zone dns.NetworkZone) *http.Transport {
		return &http.Transport{
			DialContext:         dnsResolver.DialContext(zone),
			MaxIdleConns:        50,
			MaxIdleConnsPerHost: 5,
			IdleConnTimeout:     90 * time.Second,
		}
	}
	compat.SetZonedTransports(map[dns.NetworkZone]*http.Transport{
		dns.ZoneInternal: buildTransport(dns.ZoneInternal),
		dns.ZoneOffice:   buildTransport(dns.ZoneOffice),
		dns.ZoneExternal: buildTransport(dns.ZoneExternal),
	})

	healthChecker := health.NewChecker(regCache, dnsResolver)
	go healthChecker.Start(ctx, 30*time.Second)

	proxyHandler := proxy.NewHandler(regCache, taskValidator, userConfigCache, usageRecorder, dnsResolver, cfg.Proxy.SSETimeout, cfg.Proxy.MaxRequestBody)
	toolsHandler := tools.NewHandler(regCache)

	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				taskValidator.CleanExpired()
			}
		}
	}()

	mux := http.NewServeMux()

	// Gateway core: MCP protocol proxy
	mux.HandleFunc("/proxy/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		if strings.HasSuffix(path, "/mcp") && strings.Count(path, "/") >= 3 {
			proxyHandler.ServeHTTP(w, r)
			return
		}

		// Compat: existing mcp-proxy-service API
		switch {
		case path == "/proxy/probe":
			compat.HandleProbe(w, r)
		case path == "/proxy/discover":
			compat.HandleDiscover(w, r)
		case path == "/proxy/health":
			compat.HandleHealth(w, r)
		default:
			http.NotFound(w, r)
		}
	})

	// Per-MCP health check
	mux.HandleFunc("/health/", healthChecker.HandleHealth)

	// Per-MCP tools discovery (cached)
	mux.Handle("/tools/", toolsHandler)

	// Gateway self-health
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"UP","registry":"%s"}`, regCache.FormatStatus())
	})

	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	server := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: cfg.Proxy.SSETimeout + 10*time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		slog.Info("MCP Gateway starting", "addr", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	server.Shutdown(shutdownCtx)
	cancel()
	slog.Info("MCP Gateway stopped")
}
