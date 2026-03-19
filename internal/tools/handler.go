package tools

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"linkwork/mcp-gateway/internal/compat"
	"linkwork/mcp-gateway/internal/registry"
)

type cachedTools struct {
	result    *compat.DiscoverResponse
	expiresAt time.Time
}

type Handler struct {
	registry *registry.Cache
	mu       sync.RWMutex
	cache    map[string]*cachedTools
	ttl      time.Duration
}

func NewHandler(reg *registry.Cache) *Handler {
	return &Handler{
		registry: reg,
		cache:    make(map[string]*cachedTools),
		ttl:      5 * time.Minute,
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 {
		http.Error(w, `{"error":"missing mcp name"}`, http.StatusBadRequest)
		return
	}
	mcpName := parts[1]

	h.mu.RLock()
	if ct, ok := h.cache[mcpName]; ok && time.Now().Before(ct.expiresAt) {
		h.mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ct.result)
		return
	}
	h.mu.RUnlock()

	server, ok := h.registry.Lookup(mcpName)
	if !ok {
		http.Error(w, `{"error":"unknown mcp server"}`, http.StatusNotFound)
		return
	}

	result := discoverTools(server)

	h.mu.Lock()
	h.cache[mcpName] = &cachedTools{result: result, expiresAt: time.Now().Add(h.ttl)}
	h.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func discoverTools(server *registry.MCPServer) *compat.DiscoverResponse {
	client := &http.Client{Timeout: 35 * time.Second}

	serverURL := server.URL
	if serverURL == "" {
		return &compat.DiscoverResponse{Success: false, Error: "no URL configured"}
	}

	initResp, sessionID, err := compat.ExportedSendInitialize(client, serverURL, server.Headers)
	if err != nil {
		return &compat.DiscoverResponse{Success: false, Error: err.Error()}
	}

	var serverName, serverVersion, protocolVersion string
	if initResult := compat.ExportedExtractJSONRPCResult(initResp); initResult != nil {
		if si, ok := initResult["serverInfo"].(map[string]interface{}); ok {
			serverName, _ = si["name"].(string)
			serverVersion, _ = si["version"].(string)
		}
		protocolVersion, _ = initResult["protocolVersion"].(string)
	}

	compat.ExportedSendInitializedNotification(client, serverURL, server.Headers, sessionID)

	toolsResp, err := compat.ExportedSendToolsList(client, serverURL, server.Headers, sessionID)
	if err != nil {
		return &compat.DiscoverResponse{Success: false, Error: err.Error(), ServerName: serverName, ServerVersion: serverVersion, ProtocolVersion: protocolVersion}
	}

	toolsResult := compat.ExportedExtractJSONRPCResult(toolsResp)
	if toolsResult == nil {
		return &compat.DiscoverResponse{Success: false, Error: "empty tools response", ServerName: serverName, ServerVersion: serverVersion, ProtocolVersion: protocolVersion}
	}

	tools := compat.ExportedParseTools(toolsResult)
	return &compat.DiscoverResponse{Success: true, Tools: tools, ServerName: serverName, ServerVersion: serverVersion, ProtocolVersion: protocolVersion}
}
