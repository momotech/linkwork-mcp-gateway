package user

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

type Config struct {
	UserID      string            `json:"userId"`
	McpServerID int64             `json:"mcpServerId"`
	Headers     map[string]string `json:"headers"`
	URLParams   map[string]string `json:"urlParams"`
}

type cacheEntry struct {
	config *Config
	expiry time.Time
}

type ConfigCache struct {
	mu         sync.RWMutex
	cache      map[string]*cacheEntry // "mcpName:userId" -> entry
	httpClient *http.Client
	baseURL    string
	ttl        time.Duration
	maxSize    int
}

func NewConfigCache(baseURL string) *ConfigCache {
	return &ConfigCache{
		cache:      make(map[string]*cacheEntry),
		httpClient: &http.Client{Timeout: 5 * time.Second},
		baseURL:    baseURL,
		ttl:        5 * time.Minute,
		maxSize:    10000,
	}
}

func (cc *ConfigCache) Get(mcpName, userID string) *Config {
	if userID == "" {
		return nil
	}
	key := mcpName + ":" + userID

	cc.mu.RLock()
	if e, ok := cc.cache[key]; ok && time.Now().Before(e.expiry) {
		cc.mu.RUnlock()
		return e.config
	}
	cc.mu.RUnlock()

	cfg := cc.fetch(mcpName, userID)

	cc.mu.Lock()
	if len(cc.cache) >= cc.maxSize {
		cc.evictOldest()
	}
	cc.cache[key] = &cacheEntry{config: cfg, expiry: time.Now().Add(cc.ttl)}
	cc.mu.Unlock()

	return cfg
}

func (cc *ConfigCache) fetch(mcpName, userID string) *Config {
	url := cc.baseURL + "/api/internal/mcp-user-configs?mcpName=" + mcpName + "&userId=" + userID
	resp, err := cc.httpClient.Get(url)
	if err != nil {
		slog.Debug("user config fetch failed", "mcp", mcpName, "userId", userID, "error", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var result struct {
		Code int     `json:"code"`
		Data *Config `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil
	}
	return result.Data
}

func (cc *ConfigCache) evictOldest() {
	var oldestKey string
	var oldestTime time.Time
	first := true
	for k, v := range cc.cache {
		if first || v.expiry.Before(oldestTime) {
			oldestKey = k
			oldestTime = v.expiry
			first = false
		}
	}
	if oldestKey != "" {
		delete(cc.cache, oldestKey)
	}
}
