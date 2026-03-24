package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

type MCPServer struct {
	Name           string            `json:"name"`
	Type           string            `json:"type"`
	NetworkZone    string            `json:"networkZone"`
	URL            string            `json:"url"`
	Headers        map[string]string `json:"headers"`
	HealthCheckURL string            `json:"healthCheckUrl"`
	Status         string            `json:"status"`
}

type registryResponse struct {
	Servers   []MCPServer `json:"servers"`
	UpdatedAt string      `json:"updatedAt"`
}

type Cache struct {
	mu           sync.RWMutex
	servers      map[string]*MCPServer // name -> server
	httpClient   *http.Client
	baseURL      string
	syncInterval time.Duration
}

func NewCache(baseURL string, syncInterval time.Duration) *Cache {
	return &Cache{
		servers:      make(map[string]*MCPServer),
		httpClient:   &http.Client{Timeout: 10 * time.Second},
		baseURL:      baseURL,
		syncInterval: syncInterval,
	}
}

func (c *Cache) Start(ctx context.Context) {
	c.sync()
	ticker := time.NewTicker(c.syncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.sync()
		}
	}
}

func (c *Cache) Lookup(name string) (*MCPServer, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s, ok := c.servers[name]
	return s, ok
}

func (c *Cache) All() []*MCPServer {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make([]*MCPServer, 0, len(c.servers))
	for _, s := range c.servers {
		result = append(result, s)
	}
	return result
}

func (c *Cache) sync() {
	url := c.baseURL + "/api/internal/mcp-servers/registry"
	resp, err := c.httpClient.Get(url)
	if err != nil {
		slog.Warn("registry sync failed", "error", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		slog.Warn("registry sync non-200", "status", resp.StatusCode, "body", string(body))
		return
	}

	var result struct {
		Code int              `json:"code"`
		Data registryResponse `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		slog.Warn("registry sync decode error", "error", err)
		return
	}

	newMap := make(map[string]*MCPServer, len(result.Data.Servers))
	for i := range result.Data.Servers {
		s := &result.Data.Servers[i]
		newMap[s.Name] = s
	}

	c.mu.Lock()
	c.servers = newMap
	c.mu.Unlock()

	slog.Info("registry synced", "count", len(newMap))
}

func (c *Cache) RegisterStatic(servers []MCPServer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range servers {
		s := &servers[i]
		c.servers[s.Name] = s
	}
}

func (c *Cache) GetBaseURL() string {
	return c.baseURL
}

func (c *Cache) ProbeURL(name string) string {
	s, ok := c.Lookup(name)
	if !ok {
		return ""
	}
	if s.HealthCheckURL != "" {
		return s.HealthCheckURL
	}
	return s.URL
}

func (c *Cache) FormatStatus() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return fmt.Sprintf("registry: %d servers loaded", len(c.servers))
}
