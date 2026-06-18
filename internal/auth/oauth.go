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
	defaultOAuthBaseURL = "https://mulerun.com"
	oauthClientID       = "mulerun-cli"

	cliTokenPath = "/api/platform/users/cli-token"
	oauth2Path   = "/oauth2/token"

	oauthHTTPTimeout = 15 * time.Second
)

// oauthBaseURL is the OAuth/platform host. Package-level var so tests can
// point it at httptest.Server. Production reads MULERUN_OAUTH_BASE_URL only
// when the test override is empty.
var oauthBaseURL = func() string {
	if v := strings.TrimSpace(envGet("MULERUN_OAUTH_BASE_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return defaultOAuthBaseURL
}()

// envGet is a small indirection so credentials_test can swap it without an
// import cycle. Production hits os.Getenv via the package-level alias.
var envGet = os.Getenv

// Tokens is the subset of an OAuth2 token-endpoint response we use.
type Tokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int // seconds
}

// exchangeJWTForMUK posts the OAuth JWT to the platform's cli-token endpoint
// and returns the long-lived muk- API key the platform tracks for this
// account. The platform creates the key on first request and returns the
// same value on subsequent calls (so cli2api can call this every startup
// without minting duplicates).
func exchangeJWTForMUK(ctx context.Context, jwt string) (string, error) {
	if jwt == "" {
		return "", errors.New("empty JWT")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, oauthBaseURL+cliTokenPath, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: oauthHTTPTimeout}).Do(req)
	if err != nil {
		return "", fmt.Errorf("cli-token: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
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
func refreshOAuth(ctx context.Context, refreshToken string) (Tokens, error) {
	if refreshToken == "" {
		return Tokens{}, errors.New("empty refresh token")
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", oauthClientID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, oauthBaseURL+oauth2Path, strings.NewReader(form.Encode()))
	if err != nil {
		return Tokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := (&http.Client{Timeout: oauthHTTPTimeout}).Do(req)
	if err != nil {
		return Tokens{}, fmt.Errorf("oauth2/token: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))

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
