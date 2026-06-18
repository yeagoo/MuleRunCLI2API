package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// MuleRun's OAuth/platform host. NOT the same as api.mulerun.com (the
// inference gateway, which only accepts muk- keys). These two endpoints
// live on the customer-facing web host:
//
//   POST /oauth2/token                       OAuth2 token endpoint
//                                            (ORY Hydra; same path the
//                                            mulerun login flow uses).
//   POST /api/platform/users/cli-token       JWT (Bearer) → returns the
//                                            account's stable muk- key.
//
// Both endpoints are observable in the mulerun CLI's traffic; we replicate
// the same calls here so a JWT cached by `mulerun login` is enough — the
// `mulerun` CLI binary doesn't need to be installed.
const (
	defaultOAuthBaseURL    = "https://mulerun.com"
	oauthClientID          = "mulerun-cli"
	cliTokenPath           = "/api/platform/users/cli-token"
	oauth2Path             = "/oauth2/token"
	defaultOAuthHTTPTimeout = 10 * time.Second
)

// Package-level HTTP client so the connection pool / TLS session cache is
// shared across exchange and refresh calls. No client-level Timeout — each
// call passes a context with its own deadline (see oauthHTTPTimeout()).
var oauthClient = &http.Client{}

// oauthBaseURL returns the OAuth/platform host. Read lazily from
// MULERUN_OAUTH_BASE_URL so a future config-loading layer that sets env
// vars after package init still takes effect. Tests override via the
// package-level testBaseURL var below.
func oauthBaseURL() string {
	if testBaseURL != "" {
		return testBaseURL
	}
	if v := strings.TrimSpace(os.Getenv("MULERUN_OAUTH_BASE_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return defaultOAuthBaseURL
}

// testBaseURL is set by oauth_test.go via withBaseURL() to point at a
// httptest.Server. Production reads it as the empty default.
var testBaseURL = ""

// oauthHTTPTimeout returns the per-call deadline for OAuth/platform HTTP
// requests. Configurable via CLI2API_OAUTH_TIMEOUT to keep cold-start work
// bounded under k8s/PaaS readiness probes that typically cap probes at
// 10-30s. Two calls (refresh + exchange) can chain, so set this to <half
// the orchestrator's startup ceiling.
func oauthHTTPTimeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv("CLI2API_OAUTH_TIMEOUT"))
	if raw == "" {
		return defaultOAuthHTTPTimeout
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return defaultOAuthHTTPTimeout
}

// Tokens is the subset of an OAuth2 token-endpoint response we use.
type Tokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int // seconds
}

// errOAuthTransient marks a refresh failure that is almost certainly the
// platform side (5xx, network blip) rather than a permanently-invalid
// refresh_token. Callers that wrap retry/cache logic check for it via
// errors.Is so they DON'T discard the on-disk refresh_token on a transient
// outage.
var errOAuthTransient = errors.New("oauth transient error")

// exchangeJWTForMUK posts the OAuth JWT to the platform's cli-token endpoint
// and returns the long-lived muk- API key the platform tracks for this
// account. The platform creates the key on first request and returns the
// same value on subsequent calls (so cli2api can call this every startup
// without minting duplicates).
func exchangeJWTForMUK(ctx context.Context, jwt string) (string, error) {
	if jwt == "" {
		return "", errors.New("empty JWT")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, oauthBaseURL()+cliTokenPath, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Content-Type", "application/json")

	resp, err := oauthClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("cli-token: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode/100 == 5 {
		return "", fmt.Errorf("cli-token: %w: HTTP %d: %s", errOAuthTransient, resp.StatusCode, truncate(body, 200))
	}
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("cli-token: HTTP %d: %s", resp.StatusCode, truncate(body, 200))
	}

	var payload struct {
		Code string `json:"code"`
		Data struct {
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("cli-token: parse response: %w (body: %s)", err, truncate(body, 200))
	}
	if payload.Data.Key == "" {
		return "", fmt.Errorf("cli-token: empty data.key (code=%s, body=%s)", payload.Code, truncate(body, 200))
	}
	if !strings.HasPrefix(payload.Data.Key, "muk-") {
		return "", fmt.Errorf("cli-token: response key has unexpected prefix: %.8s...", payload.Data.Key)
	}
	return payload.Data.Key, nil
}

// refreshOAuth exchanges a refresh_token for a new (access, refresh, ttl).
// The refresh_token is invalidated server-side as soon as this succeeds
// (ORY Hydra rotates refresh tokens), so callers MUST persist the new
// refresh_token or the next refresh will fail and force a re-login.
//
// A 5xx response is wrapped with errOAuthTransient — caller should NOT
// discard the cached refresh_token in that case, since the rotation
// almost certainly didn't happen server-side.
func refreshOAuth(ctx context.Context, refreshToken string) (Tokens, error) {
	if refreshToken == "" {
		return Tokens{}, errors.New("empty refresh token")
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", oauthClientID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, oauthBaseURL()+oauth2Path, strings.NewReader(form.Encode()))
	if err != nil {
		return Tokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := oauthClient.Do(req)
	if err != nil {
		// Network-level errors are transient by definition.
		return Tokens{}, fmt.Errorf("oauth2/token: %w: %v", errOAuthTransient, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))

	// Status check FIRST so a 5xx with a body that happens to parse as JSON
	// can't be confused with a permanent "invalid_grant" — the latter is a
	// 400 with explicit `error` field per OAuth2 spec.
	if resp.StatusCode/100 == 5 {
		return Tokens{}, fmt.Errorf("oauth2/token: %w: HTTP %d: %s", errOAuthTransient, resp.StatusCode, truncate(body, 200))
	}

	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return Tokens{}, fmt.Errorf("oauth2/token: parse: %w (status %d, body %s)", err, resp.StatusCode, truncate(body, 200))
	}
	if payload.Error != "" {
		return Tokens{}, fmt.Errorf("oauth2/token: %s: %s", payload.Error, payload.ErrorDesc)
	}
	if payload.AccessToken == "" {
		return Tokens{}, fmt.Errorf("oauth2/token: empty access_token (status %d, body %s)", resp.StatusCode, truncate(body, 200))
	}
	return Tokens{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		ExpiresIn:    payload.ExpiresIn,
	}, nil
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
