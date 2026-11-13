package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

const cacheTTL = time.Minute

type credential struct {
	Index         string `json:"auth_index"`
	Name          string `json:"name"`
	Provider      string `json:"provider"`
	Type          string `json:"type"`
	Disabled      bool   `json:"disabled"`
	ProjectID     string `json:"project_id"`
	IDToken       object `json:"id_token"`
	PluginID      string `json:"quota_plugin_id"`
	QuotaProvider string `json:"quota_provider"`
	SupportsQuota bool   `json:"supports_quota"`
}

type quota struct {
	Group, Window string
	Remaining     *float64
	Reset         int64
}

type summary struct {
	Name, Unit string
	Value      float64
}

type quotaData struct {
	Quotas []quota
	Values []summary
}

type accountResult struct {
	Provider, Account string
	quotaData
	Success bool
}

type exporter struct {
	base, key string
	client    *http.Client
	mu        sync.Mutex
	updated   time.Time
	body      string
	status    int
}

func newExporter(base, key string) *exporter {
	return &exporter{
		base: base,
		key:  key,
		client: &http.Client{
			Timeout: 8 * time.Second,
			// Never forward the management credential to a redirect target.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func (e *exporter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// One refresh at a time, including failed refreshes. A disconnected scraper
	// must not cancel the shared fetch or force other scrapers to retry it.
	e.mu.Lock()
	if time.Since(e.updated) >= cacheTTL {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		e.body, e.status = e.collect(ctx)
		cancel()
		e.updated = time.Now()
	}
	body, status := e.body, e.status
	e.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func (e *exporter) request(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, e.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+e.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New("management request failed")
	}
	return json.NewDecoder(resp.Body).Decode(output)
}

func (e *exporter) alias(c credential) string {
	mac := hmac.New(sha256.New, []byte(e.key))
	_, _ = io.WriteString(mac, c.Provider+":"+c.Index)
	return fmt.Sprintf("%s-%x", c.Provider, mac.Sum(nil)[:8])
}

func (e *exporter) collect(ctx context.Context) (string, int) {
	var response struct {
		Files *[]credential `json:"files"`
	}
	if err := e.request(ctx, "GET", "/v8/management/credentials", nil, &response); err != nil || response.Files == nil {
		return "# management API unavailable\n", http.StatusServiceUnavailable
	}
	var accounts []credential
	for _, c := range *response.Files {
		if c.Provider == "" {
			c.Provider = c.Type
		}
		c.Provider = normalizeProvider(c.Provider)
		if c.Disabled {
			continue
		}
		accounts = append(accounts, c)
	}
	results := make([]accountResult, len(accounts))
	var wg sync.WaitGroup
	// Bound concurrent provider requests to four; the handler coalesces scrapes.
	limit := make(chan struct{}, 4)
	for i, c := range accounts {
		limit <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-limit }()
			q, err := e.fetchQuota(ctx, c)
			results[i] = accountResult{c.Provider, e.alias(c), q, err == nil}
		}()
	}
	wg.Wait()
	return renderMetrics(results), http.StatusOK
}
