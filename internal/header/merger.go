package header

import "net/http"

// Merge merges headers into the request with the following priority (highest to lowest):
// 1. systemHeaders (from MCP server registration, injected by Gateway, cannot be overridden)
// 2. userHeaders (user personal credentials)
// 3. globalHeaders (X-Task-Id, X-User-Id from the incoming request)
func Merge(req *http.Request, systemHeaders, userHeaders, globalHeaders map[string]string) {
	for k, v := range globalHeaders {
		req.Header.Set(k, v)
	}

	for k, v := range userHeaders {
		req.Header.Set(k, v)
	}

	for k, v := range systemHeaders {
		req.Header.Set(k, v)
	}
}
