package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

var errQuota = errors.New("quota unavailable")

func normalizeProvider(p string) string {
	p = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(p), "_", "-"))
	switch p {
	case "anthropic":
		return "claude"
	case "x-ai", "grok":
		return "xai"
	case "kimi-ai", "kimi-com":
		return "kimi"
	}
	return p
}

func supported(c credential) bool {
	switch c.Provider {
	case "codex", "claude", "antigravity", "devin", "meta", "kimi", "xai":
		return true
	}
	return c.SupportsQuota || c.PluginID != ""
}

// Only these adapters choose upstream destinations. Public request parameters
// are never forwarded to CLIProxyAPI's general-purpose api-call endpoint.
func (e *exporter) apiCall(ctx context.Context, c credential, method, target string, headers map[string]string, data any) (object, error) {
	input := map[string]any{"auth_index": c.Index, "method": method, "url": target, "header": headers}
	if data != nil {
		encoded, err := json.Marshal(data)
		if err != nil {
			return nil, err
		}
		input["data"] = string(encoded)
	}
	var response struct {
		Status int    `json:"status_code"`
		Body   string `json:"body"`
	}
	if err := e.request(ctx, "POST", "/v8/management/requests/api-call", input, &response); err != nil {
		return nil, err
	}
	if response.Status < 200 || response.Status >= 300 {
		return nil, errQuota
	}
	var result object
	if err := json.Unmarshal([]byte(response.Body), &result); err != nil || result == nil {
		return nil, errQuota
	}
	return result, nil
}

func (e *exporter) download(ctx context.Context, c credential) (object, error) {
	if c.Name == "" {
		return nil, errQuota
	}
	var data object
	err := e.request(ctx, "GET", "/v8/management/credentials/download?name="+url.QueryEscape(c.Name), nil, &data)
	return data, err
}

func (e *exporter) fetchQuota(ctx context.Context, c credential) (quotaData, error) {
	if c.Index == "" || !supported(c) {
		return quotaData{}, errQuota
	}
	h := map[string]string{"Authorization": "Bearer $TOKEN$", "Accept": "application/json", "Content-Type": "application/json"}
	var data object
	var err error
	switch c.Provider {
	case "codex":
		h["User-Agent"] = "codex-tui/0.149.1"
		if id := str(c.IDToken["chatgpt_account_id"]); id != "" {
			h["Chatgpt-Account-Id"] = id
		}
		data, err = e.apiCall(ctx, c, "GET", "https://chatgpt.com/backend-api/wham/usage", h, nil)
		if err == nil {
			h["OpenAI-Beta"], h["Originator"] = "codex-1", "Codex Desktop"
			// A reset-count failure must not discard the account's quota windows.
			if resets, resetErr := e.apiCall(ctx, c, "GET", "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits", h, nil); resetErr == nil {
				data["rate_limit_reset_credits"] = resets
			}
		}
	case "claude":
		h["anthropic-beta"] = "oauth-2025-04-20"
		h["User-Agent"] = "claude-cli/2.1.280 (external, cli)"
		data, err = e.apiCall(ctx, c, "GET", "https://api.anthropic.com/api/oauth/usage", h, nil)
		if err == nil {
			if resets, resetErr := e.apiCall(ctx, c, "GET", "https://api.anthropic.com/api/oauth/usage?cedar_ember=1&skip_spend=1", h, nil); resetErr == nil {
				data["cedar_ember"] = resets["cedar_ember"]
			}
		}
	case "antigravity":
		project := c.ProjectID
		if project == "" {
			file, downloadErr := e.download(ctx, c)
			if downloadErr != nil {
				return quotaData{}, downloadErr
			}
			project = str(field(file, "project_id", "projectId"))
		}
		if project == "" {
			return quotaData{}, errQuota
		}
		h["User-Agent"] = "antigravity/hub/2.23.0 darwin/arm64"
		for _, host := range []string{"daily-cloudcode-pa.googleapis.com", "daily-cloudcode-pa.sandbox.googleapis.com", "cloudcode-pa.googleapis.com"} {
			data, err = e.apiCall(ctx, c, "POST", "https://"+host+"/v1internal:retrieveUserQuotaSummary", h, object{"project": project})
			if err == nil {
				if q, parseErr := parseQuota(c.Provider, data); parseErr == nil {
					return q, nil
				}
			}
			if ctx.Err() != nil {
				break
			}
		}
		return quotaData{}, errQuota
	case "devin":
		// The UI uses the Codeium seat-management quota RPC, not an inference call.
		delete(h, "Authorization")
		h["Connect-Protocol-Version"] = "1"
		data, err = e.apiCall(ctx, c, "POST", "https://server.codeium.com/exa.seat_management_pb.SeatManagementService/GetUserStatus", h, object{"metadata": object{"ideName": "chisel", "ideVersion": "3000.10.21", "apiKey": "$TOKEN$", "locale": "en", "os": "darwin", "extensionVersion": "3000.10.21", "clientName": "chisel"}})
	case "meta":
		file, downloadErr := e.download(ctx, c)
		if downloadErr != nil {
			return quotaData{}, downloadErr
		}
		token := str(file["dca_token"])
		h["Authorization"], h["x-api-version"] = "Bearer "+token, "1.0.0"
		data, err = e.apiCall(ctx, c, "POST", "https://api.meta.ai/muse-code/key", h, object{})
	case "kimi":
		file, downloadErr := e.download(ctx, c)
		if downloadErr != nil {
			return quotaData{}, downloadErr
		}
		host := "api.kimi.com"
		domain := strings.ToLower(str(file["domain"]))
		base, _ := url.Parse(str(field(file, "base_url", "base-url")))
		switch domain {
		case "ai", "kimi.ai", "api.kimi.ai":
			host = "api.kimi.ai"
		case "":
			if base != nil && base.Hostname() == "api.kimi.ai" {
				host = "api.kimi.ai"
			}
			if str(file["type"]) == "kimi-ai" || strings.Contains(c.Name, "kimi-ai") {
				host = "api.kimi.ai"
			}
		}
		data, err = e.apiCall(ctx, c, "GET", "https://"+host+"/coding/v1/usages", h, nil)
	case "xai":
		h["x-xai-token-auth"], h["x-grok-client-version"] = "xai-grok-cli", "1.0.46"
		h["User-Agent"] = "grok-pager/1.0.46 grok-shell/1.0.46 (macos; aarch64)"
		// Paid API accounts may only have a chat-health check in the UI. Never
		// generate billable inference traffic merely to scrape quota metrics.
		for _, suffix := range []string{"?format=credits", ""} {
			data, err = e.apiCall(ctx, c, "GET", "https://cli-chat-proxy.grok.com/v1/billing"+suffix, h, nil)
			if err == nil {
				if q, parseErr := parseQuota(c.Provider, data); parseErr == nil {
					return q, nil
				}
			}
		}
		return quotaData{}, errQuota
	default:
		plugin := c.PluginID
		if plugin == "" {
			var list struct {
				Plugins []object `json:"plugins"`
			}
			if err := e.request(ctx, "GET", "/v8/management/plugins", nil, &list); err != nil {
				return quotaData{}, err
			}
			provider := c.QuotaProvider
			if provider == "" {
				provider = c.Provider
			}
			var candidates []string
			for _, p := range list.Plugins {
				if p["registered"] != true || p["supports_quota"] != true {
					continue
				}
				id := str(p["id"])
				candidates = append(candidates, id)
				if provider == str(p["quota_provider"]) || provider == str(p["oauth_provider"]) || provider == id {
					plugin = id
					break
				}
			}
			if plugin == "" && len(candidates) == 1 {
				plugin = candidates[0]
			}
		}
		if plugin == "" {
			return quotaData{}, errQuota
		}
		err = e.request(ctx, "POST", "/v8/management/plugins/"+url.PathEscape(plugin)+"/quota", object{"auth_index": c.Index}, &data)
	}
	if err != nil {
		return quotaData{}, err
	}
	return parseQuota(c.Provider, data)
}
