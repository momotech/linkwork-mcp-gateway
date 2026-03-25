package proxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"linkwork/mcp-gateway/internal/dns"
	"linkwork/mcp-gateway/internal/header"
	"linkwork/mcp-gateway/internal/registry"
	"linkwork/mcp-gateway/internal/task"
	"linkwork/mcp-gateway/internal/usage"
	"linkwork/mcp-gateway/internal/user"
)

const (
	defaultWatermarkProduct   = "LinkWork"
	defaultWatermarkOwner     = "momotech"
	defaultWatermarkRepoURL   = "https://github.com/momotech/LinkWork"
	defaultWatermarkPolicyURL = "https://github.com/momotech/LinkWork/blob/master/TRADEMARK_POLICY.md"
)

type Handler struct {
	registry       *registry.Cache
	taskValidator  *task.Validator
	userConfig     *user.ConfigCache
	usageRecorder  *usage.Recorder
	dnsResolver    *dns.ZonedResolver
	transports     map[dns.NetworkZone]*http.Transport
	sseTimeout     time.Duration
	maxRequestBody int64
}

func NewHandler(
	reg *registry.Cache,
	tv *task.Validator,
	uc *user.ConfigCache,
	ur *usage.Recorder,
	dr *dns.ZonedResolver,
	sseTimeout time.Duration,
	maxRequestBody int64,
) *Handler {
	h := &Handler{
		registry:       reg,
		taskValidator:  tv,
		userConfig:     uc,
		usageRecorder:  ur,
		dnsResolver:    dr,
		sseTimeout:     sseTimeout,
		maxRequestBody: maxRequestBody,
	}

	h.transports = map[dns.NetworkZone]*http.Transport{
		dns.ZoneInternal: h.buildTransport(dns.ZoneInternal),
		dns.ZoneOffice:   h.buildTransport(dns.ZoneOffice),
		dns.ZoneExternal: h.buildTransport(dns.ZoneExternal),
	}

	return h
}

func (h *Handler) buildTransport(zone dns.NetworkZone) *http.Transport {
	return &http.Transport{
		DialContext:           h.dnsResolver.DialContext(zone),
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	mcpName := extractMCPName(r.URL.Path)
	if mcpName == "" {
		http.Error(w, `{"error":"missing mcp name in path"}`, http.StatusBadRequest)
		return
	}

	if h.maxRequestBody > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, h.maxRequestBody)
	}

	taskID := r.Header.Get("X-Task-Id")
	userID := r.Header.Get("X-User-Id")

	if taskID != "" {
		valid, _ := h.taskValidator.Validate(taskID)
		if !valid {
			http.Error(w, `{"error":"invalid task id"}`, http.StatusForbidden)
			return
		}
	}

	server, ok := h.registry.Lookup(mcpName)
	if !ok {
		http.Error(w, fmt.Sprintf(`{"error":"unknown mcp server: %s"}`, mcpName), http.StatusNotFound)
		return
	}

	upstreamURL := server.URL
	if upstreamURL == "" {
		http.Error(w, `{"error":"mcp server has no url configured"}`, http.StatusBadGateway)
		return
	}

	var userHeaders map[string]string
	var userParams map[string]string
	if uc := h.userConfig.Get(mcpName, userID); uc != nil {
		userHeaders = uc.Headers
		userParams = uc.URLParams
	}

	finalURL := buildFinalURL(upstreamURL, userParams)

	reqBody, reqBytes := countingReader(r.Body)
	proxyReq, err := http.NewRequestWithContext(r.Context(), r.Method, finalURL, reqBody)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"create proxy request: %s"}`, err), http.StatusInternalServerError)
		return
	}

	copyRequestHeaders(r, proxyReq)

	globalHeaders := map[string]string{
		"X-Task-Id": taskID,
		"X-User-Id": userID,
	}
	for key, value := range buildWatermarkHeaders(taskID, userID) {
		globalHeaders[key] = value
	}
	header.Merge(proxyReq, server.Headers, userHeaders, globalHeaders)

	zone := dns.NetworkZone(server.NetworkZone)
	transport := h.transports[zone]
	if transport == nil {
		transport = h.transports[dns.ZoneExternal]
	}

	resp, err := transport.RoundTrip(proxyReq)
	if err != nil {
		slog.Error("proxy request failed", "mcp", mcpName, "url", finalURL, "error", err)
		http.Error(w, fmt.Sprintf(`{"error":"upstream error: %s"}`, err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	respBody, respBytes := countingReader(resp.Body)
	resp.Body = io.NopCloser(respBody)

	if isSSE(resp) {
		h.streamSSE(w, resp)
		go h.usageRecorder.RecordWithBytes(taskID, userID, mcpName, *reqBytes, *respBytes)
		return
	}

	copyResponseHeaders(w, resp)
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)

	go h.usageRecorder.RecordWithBytes(taskID, userID, mcpName, *reqBytes, *respBytes)
}

func (h *Handler) streamSSE(w http.ResponseWriter, resp *http.Response) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		copyResponseHeaders(w, resp)
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
		return
	}

	copyResponseHeaders(w, resp)
	w.WriteHeader(resp.StatusCode)
	flusher.Flush()

	timeout := h.sseTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	deadline := time.After(timeout)

	buf := make([]byte, 4096)
	dataCh := make(chan []byte)
	errCh := make(chan error, 1)

	go func() {
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				dataCh <- chunk
			}
			if err != nil {
				errCh <- err
				return
			}
		}
	}()

	for {
		select {
		case chunk := <-dataCh:
			w.Write(chunk)
			flusher.Flush()
		case <-errCh:
			return
		case <-deadline:
			slog.Warn("SSE stream timeout reached, closing connection", "timeout", timeout)
			return
		}
	}
}

type byteCounter struct {
	reader io.Reader
	count  *int64
}

func (bc *byteCounter) Read(p []byte) (int, error) {
	n, err := bc.reader.Read(p)
	*bc.count += int64(n)
	return n, err
}

func countingReader(r io.Reader) (io.Reader, *int64) {
	var count int64
	return &byteCounter{reader: r, count: &count}, &count
}

func extractMCPName(path string) string {
	// /proxy/{mcpName}/mcp
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) >= 3 && parts[0] == "proxy" && parts[2] == "mcp" {
		return parts[1]
	}
	return ""
}

func buildFinalURL(baseURL string, params map[string]string) string {
	if len(params) == 0 {
		return baseURL
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return baseURL
	}
	q := u.Query()
	for k, v := range params {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func buildWatermarkHeaders(taskID, userID string) map[string]string {
	product := strings.TrimSpace(os.Getenv("LINKWORK_WATERMARK_NAME"))
	if product == "" {
		product = defaultWatermarkProduct
	}
	owner := strings.TrimSpace(os.Getenv("LINKWORK_WATERMARK_OWNER"))
	if owner == "" {
		owner = defaultWatermarkOwner
	}
	repoURL := strings.TrimSpace(os.Getenv("LINKWORK_WATERMARK_REPO_URL"))
	if repoURL == "" {
		repoURL = defaultWatermarkRepoURL
	}
	policyURL := strings.TrimSpace(os.Getenv("LINKWORK_WATERMARK_POLICY_URL"))
	if policyURL == "" {
		policyURL = defaultWatermarkPolicyURL
	}

	rawID := fmt.Sprintf("%s|%s|%s", product, taskID, userID)
	idDigest := sha256.Sum256([]byte(rawID))
	watermarkID := fmt.Sprintf("lw-gw-%x", idDigest[:8])

	headers := map[string]string{
		"X-LinkWork-Product":   product,
		"X-LinkWork-Owner":     owner,
		"X-LinkWork-Repo":      repoURL,
		"X-LinkWork-Policy":    policyURL,
		"X-LinkWork-Watermark": watermarkID,
	}

	secret := strings.TrimSpace(os.Getenv("LINKWORK_WATERMARK_SECRET"))
	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(fmt.Sprintf("%s|%s|%s", watermarkID, taskID, userID)))
		signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
		headers["X-LinkWork-Signature"] = signature
	}
	return headers
}

func copyRequestHeaders(src, dst *http.Request) {
	for k, vv := range src.Header {
		lk := strings.ToLower(k)
		if lk == "host" || lk == "connection" || lk == "transfer-encoding" {
			continue
		}
		for _, v := range vv {
			dst.Header.Add(k, v)
		}
	}
	if dst.Header.Get("Content-Type") == "" && src.Header.Get("Content-Type") != "" {
		dst.Header.Set("Content-Type", src.Header.Get("Content-Type"))
	}
}

func copyResponseHeaders(w http.ResponseWriter, resp *http.Response) {
	for k, vv := range resp.Header {
		lk := strings.ToLower(k)
		if lk == "transfer-encoding" || lk == "connection" {
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
}

func isSSE(resp *http.Response) bool {
	ct := resp.Header.Get("Content-Type")
	return strings.Contains(ct, "text/event-stream")
}

// UpgradeToHTTPS upgrades HTTP URLs to HTTPS for external (non-private) hosts.
func UpgradeToHTTPS(rawURL string) string {
	if rawURL == "" || !strings.HasPrefix(rawURL, "http://") {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	host := u.Hostname()
	if isPrivateHost(host) {
		return rawURL
	}
	return "https://" + rawURL[7:]
}

func isPrivateHost(host string) bool {
	if host == "localhost" || host == "127.0.0.1" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		if strings.HasSuffix(host, ".svc") || strings.Contains(host, ".svc.") ||
			strings.HasSuffix(host, ".internal") {
			return true
		}
		return false
	}
	return ip.IsPrivate() || ip.IsLoopback()
}
