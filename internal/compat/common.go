package compat

import (
	"encoding/json"
	"net/http"
	"time"

	"linkwork/mcp-gateway/internal/dns"
)

var zonedTransports map[dns.NetworkZone]*http.Transport

func SetZonedTransports(transports map[dns.NetworkZone]*http.Transport) {
	zonedTransports = transports
}

func newHTTPClient(timeout time.Duration, zone string) *http.Client {
	c := &http.Client{Timeout: timeout}
	if zonedTransports != nil {
		nz := dns.NetworkZone(zone)
		if t, ok := zonedTransports[nz]; ok {
			c.Transport = t
		} else if t, ok := zonedTransports[dns.ZoneExternal]; ok {
			c.Transport = t
		}
	}
	return c
}

func writeJSON(w http.ResponseWriter, statusCode int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(v)
}

func HandleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "UP"})
}
