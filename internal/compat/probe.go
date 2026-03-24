package compat

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"linkwork/mcp-gateway/internal/proxy"
)

type ProbeRequest struct {
	Type           string            `json:"type"`
	URL            string            `json:"url"`
	Headers        map[string]string `json:"headers"`
	HealthCheckURL string            `json:"healthCheckUrl"`
	NetworkZone    string            `json:"networkZone"`
}

type ProbeResponse struct {
	Status    string `json:"status"`
	LatencyMs int    `json:"latencyMs"`
	Message   string `json:"message"`
	ProbeURL  string `json:"probeUrl,omitempty"`
}

const mcpInitializeBody = `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"health-checker","version":"1.0"}}}`

func HandleProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ProbeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ProbeResponse{Status: "offline", Message: "invalid request body"})
		return
	}

	probeURL := req.HealthCheckURL
	if probeURL == "" {
		probeURL = req.URL
	}
	probeURL = proxy.UpgradeToHTTPS(probeURL)
	usePlainHealthCheck := strings.TrimSpace(req.HealthCheckURL) != ""

	if probeURL == "" {
		writeJSON(w, http.StatusOK, ProbeResponse{Status: "offline", LatencyMs: 0, Message: "No probe URL provided"})
		return
	}

	client := newHTTPClient(10*time.Second, req.NetworkZone)
	start := time.Now()

	var resp *http.Response
	var err error

	if usePlainHealthCheck {
		httpReq, reqErr := http.NewRequest(http.MethodGet, probeURL, nil)
		if reqErr != nil {
			writeJSON(w, http.StatusOK, ProbeResponse{Status: "offline", LatencyMs: int(time.Since(start).Milliseconds()), Message: reqErr.Error(), ProbeURL: probeURL})
			return
		}
		for k, v := range req.Headers {
			httpReq.Header.Set(k, v)
		}
		resp, err = client.Do(httpReq)
	} else if strings.EqualFold(req.Type, "http") {
		httpReq, reqErr := http.NewRequest(http.MethodPost, probeURL, strings.NewReader(mcpInitializeBody))
		if reqErr != nil {
			writeJSON(w, http.StatusOK, ProbeResponse{Status: "offline", LatencyMs: int(time.Since(start).Milliseconds()), Message: reqErr.Error(), ProbeURL: probeURL})
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "application/json, text/event-stream")
		for k, v := range req.Headers {
			httpReq.Header.Set(k, v)
		}
		resp, err = client.Do(httpReq)
	} else {
		httpReq, reqErr := http.NewRequest(http.MethodHead, probeURL, nil)
		if reqErr != nil {
			writeJSON(w, http.StatusOK, ProbeResponse{Status: "offline", LatencyMs: int(time.Since(start).Milliseconds()), Message: reqErr.Error(), ProbeURL: probeURL})
			return
		}
		for k, v := range req.Headers {
			httpReq.Header.Set(k, v)
		}
		resp, err = client.Do(httpReq)
	}

	latency := int(time.Since(start).Milliseconds())

	if err != nil {
		msg := err.Error()
		if len(msg) > 250 {
			msg = msg[:250]
		}
		slog.Warn("probe failed", "url", probeURL, "error", msg)
		writeJSON(w, http.StatusOK, ProbeResponse{Status: "offline", LatencyMs: latency, Message: msg, ProbeURL: probeURL})
		return
	}
	defer resp.Body.Close()
	io.ReadAll(io.LimitReader(resp.Body, 1024))

	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		status := "online"
		if latency >= 2000 {
			status = "degraded"
		}
		writeJSON(w, http.StatusOK, ProbeResponse{
			Status:    status,
			LatencyMs: latency,
			Message:   http.StatusText(resp.StatusCode) + " (" + time.Duration(latency).String() + ")",
			ProbeURL:  probeURL,
		})
		return
	}

	writeJSON(w, http.StatusOK, ProbeResponse{
		Status:    "offline",
		LatencyMs: latency,
		Message:   "HTTP " + http.StatusText(resp.StatusCode),
		ProbeURL:  probeURL,
	})
}
