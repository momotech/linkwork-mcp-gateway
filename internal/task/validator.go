package task

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

type validateResult struct {
	Valid  bool
	UserID string
	Expiry time.Time
}

type Validator struct {
	mu         sync.RWMutex
	cache      map[string]*validateResult
	httpClient *http.Client
	baseURL    string
	ttl        time.Duration
}

func NewValidator(baseURL string, ttl time.Duration) *Validator {
	return &Validator{
		cache:      make(map[string]*validateResult),
		httpClient: &http.Client{Timeout: 5 * time.Second},
		baseURL:    baseURL,
		ttl:        ttl,
	}
}

func (v *Validator) Validate(taskID string) (bool, string) {
	if taskID == "" {
		return false, ""
	}

	v.mu.RLock()
	if cached, ok := v.cache[taskID]; ok && time.Now().Before(cached.Expiry) {
		v.mu.RUnlock()
		return cached.Valid, cached.UserID
	}
	v.mu.RUnlock()

	valid, userID := v.remoteValidate(taskID)

	v.mu.Lock()
	v.cache[taskID] = &validateResult{
		Valid:  valid,
		UserID: userID,
		Expiry: time.Now().Add(v.ttl),
	}
	v.mu.Unlock()

	return valid, userID
}

func (v *Validator) remoteValidate(taskID string) (bool, string) {
	url := v.baseURL + "/api/internal/tasks/" + taskID + "/validate"
	resp, err := v.httpClient.Get(url)
	if err != nil {
		slog.Warn("task validate request failed, degrading to allow", "taskId", taskID, "error", err)
		return true, ""
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return false, ""
	}
	if resp.StatusCode != http.StatusOK {
		slog.Warn("task validate non-200, degrading to allow", "taskId", taskID, "status", resp.StatusCode)
		return true, ""
	}

	var result struct {
		Code int `json:"code"`
		Data struct {
			TaskID string `json:"taskId"`
			Valid  bool   `json:"valid"`
			UserID string `json:"userId"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		slog.Warn("task validate decode error, degrading to allow", "taskId", taskID, "error", err)
		return true, ""
	}

	return result.Data.Valid, result.Data.UserID
}

func (v *Validator) CleanExpired() {
	v.mu.Lock()
	defer v.mu.Unlock()
	now := time.Now()
	for k, val := range v.cache {
		if now.After(val.Expiry) {
			delete(v.cache, k)
		}
	}
}
