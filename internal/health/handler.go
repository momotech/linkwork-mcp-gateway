package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"linkwork/mcp-gateway/internal/dns"
	"linkwork/mcp-gateway/internal/proxy"
	"linkwork/mcp-gateway/internal/registry"
)

type Status struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	LatencyMs int    `json:"latencyMs"`
	Message   string `json:"message"`
	CheckedAt string `json:"checkedAt"`
}

type Checker struct {
	registry   *registry.Cache
	resolver   *dns.ZonedResolver
	mu         sync.RWMutex
	statuses   map[string]*Status
	transports map[dns.NetworkZone]*http.Transport
}

const mcpInitBody = `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"health-checker","version":"1.0"}}}`

func NewChecker(reg *registry.Cache, resolver *dns.ZonedResolver) *Checker {
	c := &Checker{
		registry: reg,
		resolver: resolver,
		statuses: make(map[string]*Status),
	}
	c.transports = map[dns.NetworkZone]*http.Transport{
		dns.ZoneInternal: {DialContext: resolver.DialContext(dns.ZoneInternal), ResponseHeaderTimeout: 10 * time.Second},
		dns.ZoneOffice:   {DialContext: resolver.DialContext(dns.ZoneOffice), ResponseHeaderTimeout: 10 * time.Second},
		dns.ZoneExternal: {DialContext: resolver.DialContext(dns.ZoneExternal), ResponseHeaderTimeout: 10 * time.Second},
	}
	return c
}

func (c *Checker) Start(ctx context.Context, interval time.Duration) {
	c.checkAll()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.checkAll()
		}
	}
}

func (c *Checker) checkAll() {
	servers := c.registry.All()
	for _, s := range servers {
		st := c.probe(s)
		c.mu.Lock()
		c.statuses[s.Name] = st
		c.mu.Unlock()
	}
	if len(servers) > 0 {
		slog.Debug("health check cycle completed", "count", len(servers))
	}
}

func (c *Checker) probe(s *registry.MCPServer) *Status {
	probeURL := s.HealthCheckURL
	if probeURL == "" {
		probeURL = s.URL
	}
	probeURL = proxy.UpgradeToHTTPS(probeURL)
	usePlainHealthCheck := strings.TrimSpace(s.HealthCheckURL) != ""

	if probeURL == "" {
		return &Status{Name: s.Name, Status: "offline", Message: "no probe URL", CheckedAt: nowStr()}
	}

	zone := dns.NetworkZone(s.NetworkZone)
	transport := c.transports[zone]
	if transport == nil {
		transport = c.transports[dns.ZoneExternal]
	}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}

	start := time.Now()
	var resp *http.Response
	var err error

	if usePlainHealthCheck {
		req, _ := http.NewRequest(http.MethodGet, probeURL, nil)
		for k, v := range s.Headers {
			req.Header.Set(k, v)
		}
		resp, err = client.Do(req)
	} else if strings.EqualFold(s.Type, "http") {
		req, _ := http.NewRequest(http.MethodPost, probeURL, strings.NewReader(mcpInitBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		for k, v := range s.Headers {
			req.Header.Set(k, v)
		}
		resp, err = client.Do(req)
	} else {
		req, _ := http.NewRequest(http.MethodHead, probeURL, nil)
		for k, v := range s.Headers {
			req.Header.Set(k, v)
		}
		resp, err = client.Do(req)
	}

	latency := int(time.Since(start).Milliseconds())

	if err != nil {
		msg := err.Error()
		if len(msg) > 250 {
			msg = msg[:250]
		}
		return &Status{Name: s.Name, Status: "offline", LatencyMs: latency, Message: msg, CheckedAt: nowStr()}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		status := "online"
		if latency >= 2000 {
			status = "degraded"
		}
		return &Status{Name: s.Name, Status: status, LatencyMs: latency, Message: http.StatusText(resp.StatusCode), CheckedAt: nowStr()}
	}

	return &Status{Name: s.Name, Status: "offline", LatencyMs: latency, Message: "HTTP " + http.StatusText(resp.StatusCode), CheckedAt: nowStr()}
}

func (c *Checker) GetStatus(name string) *Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.statuses[name]
}

func (c *Checker) HandleHealth(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 {
		http.Error(w, `{"error":"missing mcp name"}`, http.StatusBadRequest)
		return
	}
	mcpName := parts[1]
	st := c.GetStatus(mcpName)
	if st == nil {
		http.Error(w, `{"error":"unknown mcp server"}`, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(st)
}

func nowStr() string {
	return time.Now().Format(time.RFC3339)
}
