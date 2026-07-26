package executor

import (
	"context"
	"strings"
	"time"

	codexauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

func (e *CodexExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("codex executor: refresh called")
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil {
		return nil, statusErr{code: 500, msg: "codex executor: auth is nil"}
	}
	var refreshToken string
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["refresh_token"].(string); ok && v != "" {
			refreshToken = v
		}
	}
	if refreshToken == "" {
		return auth, nil
	}
	svc := codexauth.NewCodexAuthWithProxyURL(e.cfg, auth.ProxyURL)
	td, err := svc.RefreshTokensWithRetry(ctx, refreshToken, 3)
	if err != nil {
		return nil, err
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["id_token"] = td.IDToken
	auth.Metadata["access_token"] = td.AccessToken
	if td.RefreshToken != "" {
		auth.Metadata["refresh_token"] = td.RefreshToken
	}
	if td.AccountID != "" {
		auth.Metadata["account_id"] = td.AccountID
	}
	auth.Metadata["email"] = td.Email
	// Use unified key in files
	auth.Metadata["expired"] = td.Expire
	auth.Metadata["type"] = "codex"
	now := time.Now().Format(time.RFC3339)
	auth.Metadata["last_refresh"] = now
	return auth, nil
}

func codexCreds(a *cliproxyauth.Auth) (apiKey, baseURL string) {
	return codexCredsWithConfig(nil, a)
}

// codexCredsWithConfig resolves the Codex credential and its upstream base URL.
// Base URL precedence (high -> low): auth attributes, per-auth metadata (OAuth
// only), then the global codex-base-url config (OAuth only). The OAuth
// classification stays on attributes plus the resolved token so this never calls
// Auth.AuthKind, which reads Auth.Metadata outside the credential lock.
func codexCredsWithConfig(cfg *config.Config, a *cliproxyauth.Auth) (apiKey, baseURL string) {
	if a == nil {
		return "", ""
	}
	if a.Attributes != nil {
		apiKey = a.Attributes["api_key"]
		baseURL = strings.TrimSpace(a.Attributes["base_url"])
	}
	if apiKey == "" && a.Metadata != nil {
		if v, ok := a.Metadata["access_token"].(string); ok {
			apiKey = v
		}
	}
	if baseURL == "" && codexAuthInheritsBaseURL(a, apiKey) {
		baseURL = cliproxyauth.BaseURLFromMetadata(a.Metadata)
		if baseURL == "" && cfg != nil {
			baseURL = cfg.CodexBaseURL
		}
	}
	return apiKey, normalizeCodexBaseURL(baseURL)
}

// codexAuthInheritsBaseURL reports whether the credential is an OAuth login,
// the only kind that may inherit a base URL from metadata or global config. A
// configured API key that set no base-url intentionally targets the default
// endpoint and must never be redirected by a global setting.
func codexAuthInheritsBaseURL(a *cliproxyauth.Auth, resolvedAPIKey string) bool {
	switch cliproxyauth.AuthKindFromAttributes(a) {
	case cliproxyauth.AuthKindOAuth:
		return true
	case cliproxyauth.AuthKindAPIKey:
		return false
	}
	// No attribute verdict: the credential is OAuth exactly when its token came
	// from metadata rather than from a configured api_key attribute.
	return strings.TrimSpace(resolvedAPIKey) != ""
}

// normalizeCodexBaseURL trims surrounding whitespace and any trailing slash so
// configured and resolved base URLs compare equal and never produce "//" paths.
func normalizeCodexBaseURL(baseURL string) string {
	return strings.TrimRight(strings.TrimSpace(baseURL), "/")
}

func (e *CodexExecutor) resolveCodexConfig(auth *cliproxyauth.Auth) *config.CodexKey {
	if auth == nil || e.cfg == nil {
		return nil
	}
	var attrKey, attrBase string
	if auth.Attributes != nil {
		attrKey = strings.TrimSpace(auth.Attributes["api_key"])
		attrBase = normalizeCodexBaseURL(auth.Attributes["base_url"])
	}
	for i := range e.cfg.CodexKey {
		entry := &e.cfg.CodexKey[i]
		cfgKey := strings.TrimSpace(entry.APIKey)
		cfgBase := normalizeCodexBaseURL(entry.BaseURL)
		if attrKey != "" && attrBase != "" {
			if strings.EqualFold(cfgKey, attrKey) && strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
			continue
		}
		if attrKey != "" && strings.EqualFold(cfgKey, attrKey) {
			if cfgBase == "" || strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
		}
		if attrKey == "" && attrBase != "" && strings.EqualFold(cfgBase, attrBase) {
			return entry
		}
	}
	if attrKey != "" {
		for i := range e.cfg.CodexKey {
			entry := &e.cfg.CodexKey[i]
			if strings.EqualFold(strings.TrimSpace(entry.APIKey), attrKey) {
				return entry
			}
		}
	}
	return nil
}
