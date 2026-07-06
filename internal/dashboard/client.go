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
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// Client is an ESPHome dashboard API client.
type Client struct {
	BaseURL  string
	Username string
	Password string
	HTTP     *http.Client
	Dialer   *websocket.Dialer
}

// New returns a dashboard client targeting the given base URL (e.g.
// "http://homeassistant.local:6052").
func New(baseURL, username, password string) *Client {
	baseURL = strings.TrimRight(baseURL, "/")
	return &Client{
		BaseURL:  baseURL,
		Username: username,
		Password: password,
		HTTP:     &http.Client{Timeout: 120 * time.Second},
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

// authHeader returns the Basic auth header value, or "" if no password is set.
func (c *Client) authHeader() string {
	if c.Password == "" {
		return ""
	}
	user := c.Username
	creds := user + ":" + c.Password
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(creds))
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	if h := c.authHeader(); h != "" {
		req.Header.Set("Authorization", h)
	}
	return req, nil
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

// ensureYAMLExt makes sure a configuration name ends with .yaml or .yml.
// ESPHome dashboard endpoints require a full filename.
func ensureYAMLExt(name string) string {
	if strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml") {
		return name
	}
	return name + ".yaml"
}
