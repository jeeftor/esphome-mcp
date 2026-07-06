// Package dashboard implements an HTTP/WebSocket client for the ESPHome
// dashboard API (the server exposed on port 6052 by `esphome dashboard` and
// the Home Assistant ESPHome addon).
package dashboard

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// Client is an ESPHome dashboard API client.
//
// Auth modes (mutually exclusive):
//   - Basic auth (standalone ESPHome with password set)
//   - HA addon ingress (sends X-HA-Ingress: YES; works when port 6052 is
//     mapped and the addon is reachable directly)
//   - HA addon login (POSTs HA credentials to /login, captures the session
//     cookie; works when HA Supervisor auth is enabled)
type Client struct {
	BaseURL  string
	Username string
	Password string
	Ingress  bool // send X-HA-Ingress: YES (HA addon with mapped port)
	HTTP     *http.Client
	Dialer   *websocket.Dialer
}

// New returns a dashboard client targeting the given base URL (e.g.
// "http://homeassistant.local:6052").
func New(baseURL, username, password string) *Client {
	baseURL = strings.TrimRight(baseURL, "/")
	jar, _ := cookiejar.New(nil)
	return &Client{
		BaseURL:  baseURL,
		Username: username,
		Password: password,
		HTTP:     &http.Client{Timeout: 120 * time.Second, Jar: jar},
		Dialer: &websocket.Dialer{
			HandshakeTimeout: 15 * time.Second,
		},
	}
}

// ConfiguredDevice is a device present in the dashboard config directory.
type ConfiguredDevice struct {
	Name               string   `json:"name"`
	FriendlyName       string   `json:"friendly_name"`
	Configuration      string   `json:"configuration"`
	LoadedIntegrations []string `json:"loaded_integrations"`
	DeployedVersion    *string  `json:"deployed_version"`
	CurrentVersion     *string  `json:"current_version"`
	Path               string   `json:"path"`
	Comment            *string  `json:"comment"`
	Address            *string  `json:"address"`
	WebPort            *int     `json:"web_port"`
	TargetPlatform     *string  `json:"target_platform"`
}

// ImportableDevice is a discovered device that can be adopted.
type ImportableDevice struct {
	Name              string `json:"name"`
	FriendlyName      string `json:"friendly_name"`
	PackageImportURL  string `json:"package_import_url"`
	ProjectName       string `json:"project_name"`
	ProjectVersion    string `json:"project_version"`
	Network           string `json:"network"`
	Ignored           bool   `json:"ignored"`
}

// DeviceListResponse is the payload returned by GET /devices.
type DeviceListResponse struct {
	Configured []ConfiguredDevice `json:"configured"`
	Importable []ImportableDevice `json:"importable"`
}

// authHeader returns the Basic auth header value, or "" if no password is set
// and ingress mode is not active. In HA addon mode, Basic auth is not used —
// the ingress header or cookie-based login handles auth instead.
func (c *Client) authHeader() string {
	if c.Ingress || c.Password == "" {
		return ""
	}
	user := c.Username
	creds := user + ":" + c.Password
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(creds))
}

// applyAuth sets auth headers on an HTTP request. When Ingress is true, the
// X-HA-Ingress header is sent (bypasses HA addon auth for direct port 6052
// access). Otherwise Basic auth is used if a password is configured. The
// cookie jar handles session cookies from Login() automatically.
//
// For POST requests in standalone-with-password mode, the XSRF token from the
// cookie jar is added as the X-XSRF-TOKEN header (Tornado requires this when
// xsrf_cookies is enabled).
func (c *Client) applyAuth(req *http.Request) {
	if c.Ingress {
		req.Header.Set("X-HA-Ingress", "YES")
	}
	if h := c.authHeader(); h != "" {
		req.Header.Set("Authorization", h)
	}
	// XSRF protection is enabled when the standalone dashboard has a password
	// set (Tornado sets xsrf_cookies: settings.using_password). POST requests
	// must include the XSRF token from the cookie. We fetch it from the cookie
	// jar for the request's host.
	if req.Method == http.MethodPost && c.Password != "" && !c.Ingress {
		if xsrf := c.xsrfToken(req.URL); xsrf != "" {
			req.Header.Set("X-XSRF-TOKEN", xsrf)
		}
	}
}

// xsrfToken extracts the _xsrf cookie value for the given URL from the cookie
// jar. Returns "" if no XSRF cookie is present (e.g., the dashboard doesn't
// use XSRF, or we haven't fetched a page yet).
func (c *Client) xsrfToken(u *url.URL) string {
	if c.HTTP.Jar == nil {
		return ""
	}
	for _, cookie := range c.HTTP.Jar.Cookies(u) {
		if cookie.Name == "_xsrf" {
			return cookie.Value
		}
	}
	return ""
}

// ensureXSRFCookie fetches the dashboard root page to prime the XSRF cookie
// in the cookie jar. This is needed before POST requests when the standalone
// dashboard has a password set (Tornado sets the _xsrf cookie on page load).
// Called automatically by SaveConfig if no XSRF cookie is present yet.
func (c *Client) ensureXSRFCookie(ctx context.Context) error {
	if c.Password == "" || c.Ingress {
		return nil // XSRF only applies to standalone-with-password
	}
	if c.xsrfToken(&url.URL{Scheme: "http", Host: "placeholder"}) != "" {
		return nil // already have the cookie
	}
	req, err := c.newRequest(ctx, http.MethodGet, "/", nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	c.applyAuth(req)
	return req, nil
}

// Login authenticates against the standalone ESPHome dashboard's /login
// endpoint (native password auth). The resulting session cookie is stored in
// the client's cookie jar and sent automatically on subsequent requests.
//
// This is NOT needed when using Basic auth (password is set and not in HA
// addon mode) — Basic auth is sent on every request automatically.
//
// This does NOT work for HA addon auth: the HA addon's /login calls
// http://supervisor/auth with SUPERVISOR_TOKEN, which is only available
// inside the addon container. External clients must use the ingress header
// bypass instead (set Client.Ingress = true).
func (c *Client) Login(ctx context.Context) error {
	form := url.Values{}
	form.Set("username", c.Username)
	form.Set("password", c.Password)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/login", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.applyAuth(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("login failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

// ListDevices returns all configured and importable devices from the dashboard.
func (c *Client) ListDevices(ctx context.Context) (*DeviceListResponse, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/devices", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list devices: %s", resp.Status)
	}
	var out DeviceListResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	return &out, nil
}

// Ping returns a map of configuration filename -> online (true/false).
func (c *Client) Ping(ctx context.Context) (map[string]bool, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/ping", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ping: %s", resp.Status)
	}
	out := make(map[string]bool)
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("ping: %w", err)
	}
	return out, nil
}

// GetConfig returns the raw YAML configuration for the named device.
func (c *Client) GetConfig(ctx context.Context, configuration string) (string, error) {
	configuration = ensureYAMLExt(configuration)
	req, err := c.newRequest(ctx, http.MethodGet, "/edit?configuration="+url.QueryEscape(configuration), nil)
	if err != nil {
		return "", err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("configuration %q not found", configuration)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("get config: %s", resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// SaveConfig writes the YAML configuration for the named device.
func (c *Client) SaveConfig(ctx context.Context, configuration, yaml string) error {
	configuration = ensureYAMLExt(configuration)
	// Prime the XSRF cookie for standalone-with-password mode (Tornado
	// requires the _xsrf token on POST requests when xsrf_cookies is enabled).
	if err := c.ensureXSRFCookie(ctx); err != nil {
		return fmt.Errorf("prime xsrf cookie: %w", err)
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/edit?configuration="+url.QueryEscape(configuration), bytes.NewReader([]byte(yaml)))
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("save config: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

// GetInfo returns the device storage JSON (build info, loaded integrations, etc.).
func (c *Client) GetInfo(ctx context.Context, configuration string) (json.RawMessage, error) {
	configuration = ensureYAMLExt(configuration)
	req, err := c.newRequest(ctx, http.MethodGet, "/info?configuration="+url.QueryEscape(configuration), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("configuration %q not found", configuration)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get info: %s", resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// GetJSONConfig returns the fully parsed YAML configuration as JSON, with
// secrets resolved (runs `esphome config --show-secrets` on the server side).
// This is the endpoint to use for extracting the API encryption key
// (api.encryption.key) needed by the native API client.
func (c *Client) GetJSONConfig(ctx context.Context, configuration string) (json.RawMessage, error) {
	configuration = ensureYAMLExt(configuration)
	req, err := c.newRequest(ctx, http.MethodGet, "/json-config?configuration="+url.QueryEscape(configuration), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("configuration %q not found", configuration)
	}
	if resp.StatusCode == http.StatusUnprocessableEntity {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("config validation failed: %s", strings.TrimSpace(string(body)))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get json-config: %s", resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// GetEncryptionKey extracts the API encryption PSK (base64) from a device's
// parsed configuration. It calls /json-config and navigates to
// api.encryption.key. Returns ("", nil) if the device has no encryption
// configured.
func (c *Client) GetEncryptionKey(ctx context.Context, configuration string) (string, error) {
	raw, err := c.GetJSONConfig(ctx, configuration)
	if err != nil {
		return "", err
	}
	var cfg struct {
		API struct {
			Encryption struct {
				Key string `json:"key"`
			} `json:"encryption"`
		} `json:"api"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return "", fmt.Errorf("parse json-config: %w", err)
	}
	return cfg.API.Encryption.Key, nil
}

// ensureYAMLExt makes sure a configuration name ends with .yaml or .yml.
// ESPHome dashboard endpoints require a full filename.
func ensureYAMLExt(name string) string {
	if strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml") {
		return name
	}
	return name + ".yaml"
}
