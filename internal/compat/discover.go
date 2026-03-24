package compat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"linkwork/mcp-gateway/internal/proxy"
)

type DiscoverRequest struct {
	URL         string            `json:"url"`
	Headers     map[string]string `json:"headers"`
	NetworkZone string            `json:"networkZone"`
}

type DiscoverResponse struct {
	Success         bool      `json:"success"`
	Error           string    `json:"error,omitempty"`
	ServerName      string    `json:"serverName,omitempty"`
	ServerVersion   string    `json:"serverVersion,omitempty"`
	ProtocolVersion string    `json:"protocolVersion,omitempty"`
	Tools           []MCPTool `json:"tools,omitempty"`
}

type MCPTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	InputSchema map[string]interface{} `json:"inputSchema,omitempty"`
}

func HandleDiscover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req DiscoverRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, DiscoverResponse{Success: false, Error: "invalid request body"})
		return
	}

	serverURL := proxy.UpgradeToHTTPS(req.URL)
	if serverURL == "" {
		writeJSON(w, http.StatusOK, DiscoverResponse{Success: false, Error: "No URL provided"})
		return
	}

	slog.Info("starting MCP discovery", "url", serverURL)

	client := newHTTPClient(35*time.Second, req.NetworkZone)

	initResp, sessionID, err := sendInitialize(client, serverURL, req.Headers)
	if err != nil {
		writeJSON(w, http.StatusOK, DiscoverResponse{Success: false, Error: truncate(err.Error(), 500)})
		return
	}

	var serverName, serverVersion, protocolVersion string
	if initResult := extractJSONRPCResult(initResp); initResult != nil {
		if si, ok := initResult["serverInfo"].(map[string]interface{}); ok {
			serverName, _ = si["name"].(string)
			serverVersion, _ = si["version"].(string)
		}
		protocolVersion, _ = initResult["protocolVersion"].(string)
	}

	sendInitializedNotification(client, serverURL, req.Headers, sessionID)

	toolsResp, err := sendToolsList(client, serverURL, req.Headers, sessionID)
	if err != nil {
		writeJSON(w, http.StatusOK, DiscoverResponse{
			Success: false, Error: truncate(err.Error(), 500),
			ServerName: serverName, ServerVersion: serverVersion, ProtocolVersion: protocolVersion,
		})
		return
	}

	toolsResult := extractJSONRPCResult(toolsResp)
	if toolsResult == nil {
		writeJSON(w, http.StatusOK, DiscoverResponse{
			Success: false, Error: "Empty response from tools/list",
			ServerName: serverName, ServerVersion: serverVersion, ProtocolVersion: protocolVersion,
		})
		return
	}

	tools := parseTools(toolsResult)
	slog.Info("MCP discovery completed", "url", serverURL, "tools", len(tools))

	writeJSON(w, http.StatusOK, DiscoverResponse{
		Success: true, Tools: tools,
		ServerName: serverName, ServerVersion: serverVersion, ProtocolVersion: protocolVersion,
	})
}

func ExportedSendInitialize(client *http.Client, serverURL string, headers map[string]string) (string, string, error) {
	return sendInitialize(client, serverURL, headers)
}

func ExportedSendInitializedNotification(client *http.Client, serverURL string, headers map[string]string, sessionID string) {
	sendInitializedNotification(client, serverURL, headers, sessionID)
}

func ExportedSendToolsList(client *http.Client, serverURL string, headers map[string]string, sessionID string) (string, error) {
	return sendToolsList(client, serverURL, headers, sessionID)
}

func ExportedExtractJSONRPCResult(body string) map[string]interface{} {
	return extractJSONRPCResult(body)
}

func ExportedParseTools(result map[string]interface{}) []MCPTool {
	return parseTools(result)
}

func sendInitialize(client *http.Client, serverURL string, headers map[string]string) (string, string, error) {
	body := map[string]interface{}{
		"jsonrpc": "2.0", "method": "initialize", "id": 1,
		"params": map[string]interface{}{
			"protocolVersion": "2025-03-26",
			"capabilities":    map[string]interface{}{},
			"clientInfo":      map[string]interface{}{"name": "moai-robot", "version": "1.0.0"},
		},
	}
	data, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, serverURL, bytes.NewReader(data))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return "", "", fmt.Errorf("HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	sessionID := resp.Header.Get("Mcp-Session-Id")
	return string(respBody), sessionID, nil
}

func sendInitializedNotification(client *http.Client, serverURL string, headers map[string]string, sessionID string) {
	body := map[string]interface{}{"jsonrpc": "2.0", "method": "notifications/initialized"}
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, serverURL, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}

func sendToolsList(client *http.Client, serverURL string, headers map[string]string, sessionID string) (string, error) {
	body := map[string]interface{}{"jsonrpc": "2.0", "method": "tools/list", "id": 2}
	data, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, serverURL, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	return string(respBody), nil
}

func extractJSONRPCResult(body string) map[string]interface{} {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil
	}

	jsonBody := body
	if strings.HasPrefix(body, "event:") || strings.HasPrefix(body, "data:") {
		jsonBody = extractJSONFromSSE(body)
	}
	if jsonBody == "" {
		return nil
	}

	var rpcResp map[string]interface{}
	if err := json.Unmarshal([]byte(jsonBody), &rpcResp); err != nil {
		return nil
	}

	if errObj, ok := rpcResp["error"].(map[string]interface{}); ok {
		msg, _ := errObj["message"].(string)
		slog.Warn("JSON-RPC error", "message", msg)
		return nil
	}

	result, _ := rpcResp["result"].(map[string]interface{})
	return result
}

func extractJSONFromSSE(sseBody string) string {
	nextDataIsMessage := false
	for _, line := range strings.Split(sseBody, "\n") {
		line = strings.TrimSpace(line)
		if line == "event: message" {
			nextDataIsMessage = true
			continue
		}
		if nextDataIsMessage && strings.HasPrefix(line, "data: ") {
			return strings.TrimPrefix(line, "data: ")
		}
		if !nextDataIsMessage && strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if strings.HasPrefix(data, "{") && strings.Contains(data, `"jsonrpc"`) {
				return data
			}
		}
		if line == "" {
			nextDataIsMessage = false
		}
	}
	return ""
}

func parseTools(result map[string]interface{}) []MCPTool {
	toolsRaw, ok := result["tools"].([]interface{})
	if !ok {
		return nil
	}
	tools := make([]MCPTool, 0, len(toolsRaw))
	for _, t := range toolsRaw {
		tm, ok := t.(map[string]interface{})
		if !ok {
			continue
		}
		tool := MCPTool{
			Name:        stringVal(tm, "name"),
			Description: stringVal(tm, "description"),
		}
		if is, ok := tm["inputSchema"].(map[string]interface{}); ok {
			tool.InputSchema = is
		}
		tools = append(tools, tool)
	}
	return tools
}

func stringVal(m map[string]interface{}, key string) string {
	v, _ := m[key].(string)
	return v
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
