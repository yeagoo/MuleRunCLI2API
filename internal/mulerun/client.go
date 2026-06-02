package mulerun

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type AuthScheme int

const (
	// AuthBearer attaches `Authorization: Bearer <token>` — used by OpenAI-shaped
	// chat completions and every vendor under /vendors/*.
	AuthBearer AuthScheme = iota
	// AuthAPIKey attaches `X-API-Key: <token>` — required by /v1/messages.
	AuthAPIKey
)

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func New(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP: &http.Client{
			Timeout: 0, // never time the long-lived SSE / poll loops out at the client level
		},
	}
}

// Do issues an arbitrary HTTP request against the mulerun base URL, injecting
// the configured auth header (overriding whatever the caller set).
func (c *Client) Do(ctx context.Context, method, path string, body io.Reader, scheme AuthScheme, extraHeaders http.Header) (*http.Response, error) {
	u, err := url.JoinPath(c.BaseURL, path)
	if err != nil {
		return nil, fmt.Errorf("join url: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}

	for k, vv := range extraHeaders {
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}
	c.injectAuth(req, scheme)
	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	return c.HTTP.Do(req)
}

func (c *Client) injectAuth(req *http.Request, scheme AuthScheme) {
	req.Header.Del("Authorization")
	req.Header.Del("X-API-Key")
	req.Header.Del("Api-Key")
	switch scheme {
	case AuthAPIKey:
		req.Header.Set("X-API-Key", c.Token)
	default:
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
}

// PostJSON sends a JSON payload and decodes the response into out, returning
// the HTTP status code and any decode error. Caller decides what to do based
// on status.
func (c *Client) PostJSON(ctx context.Context, path string, in any, out any, scheme AuthScheme) (int, error) {
	buf, err := json.Marshal(in)
	if err != nil {
		return 0, fmt.Errorf("marshal request: %w", err)
	}
	resp, err := c.Do(ctx, http.MethodPost, path, bytes.NewReader(buf), scheme, nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil && err != io.EOF {
		return resp.StatusCode, fmt.Errorf("decode response: %w", err)
	}
	return resp.StatusCode, nil
}

// GetJSON fetches a JSON endpoint.
func (c *Client) GetJSON(ctx context.Context, path string, out any, scheme AuthScheme) (int, error) {
	resp, err := c.Do(ctx, http.MethodGet, path, nil, scheme, nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil && err != io.EOF {
		return resp.StatusCode, fmt.Errorf("decode response: %w", err)
	}
	return resp.StatusCode, nil
}

