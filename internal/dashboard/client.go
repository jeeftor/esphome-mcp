// Package dashboard implements a client for the ESPHome 2026.6+ Device Builder
// API exposed on port 6052 by `esphome dashboard` and the Home Assistant
// ESPHome add-on.
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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"gopkg.in/yaml.v3"
)

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

// Client is an ESPHome Device Builder API client.
type Client struct {
	BaseURL  string
	Username string
	Password string
	Ingress  bool // send X-HA-Ingress: YES for HA add-on ingress-style auth.
	HTTP     *http.Client
	Dialer   *websocket.Dialer

	mu        sync.Mutex
	messageID int
}

// New returns a dashboard client targeting the given base URL.
func New(baseURL, username, password string) *Client {
	return &Client{
		BaseURL:  strings.TrimRight(baseURL, "/"),
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
	State              string   `json:"state"`
	Status             string   `json:"status"`
}

// ImportableDevice is a discovered device that can be adopted.
type ImportableDevice struct {
	Name             string `json:"name"`
	FriendlyName     string `json:"friendly_name"`
	PackageImportURL string `json:"package_import_url"`
	ProjectName      string `json:"project_name"`
	ProjectVersion   string `json:"project_version"`
	Network          string `json:"network"`
	Ignored          bool   `json:"ignored"`
}

// DeviceListResponse is the payload returned by devices/list.
type DeviceListResponse struct {
	Configured []ConfiguredDevice `json:"configured"`
	Importable []ImportableDevice `json:"importable"`
}

type serverInfo struct {
	ServerVersion  string `json:"server_version"`
	ESPHomeVersion string `json:"esphome_version"`
	RequiresAuth   bool   `json:"requires_auth"`
}

type wsRequest struct {
	Command   string         `json:"command"`
	MessageID int            `json:"message_id"`
	Args      map[string]any `json:"args,omitempty"`
}

type wsResponse struct {
	MessageID string          `json:"message_id"`
	Result    json.RawMessage `json:"result"`
	ErrorCode string          `json:"error_code"`
	Details   string          `json:"details"`
	Event     string          `json:"event"`
	Data      json.RawMessage `json:"data"`
}

func (c *Client) nextMessageID() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.messageID++
	return c.messageID
}

func (c *Client) wsURL() (string, error) {
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("unsupported dashboard URL scheme %q", u.Scheme)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/ws"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func (c *Client) applyAuth(headers http.Header) {
	if c.Ingress {
		headers.Set("X-HA-Ingress", "YES")
	}
	if c.Username != "" && c.Password != "" {
		headers.Set("Authorization", "Basic "+basicAuth(c.Username, c.Password))
	}
}

func basicAuth(username, password string) string {
	return base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
}

func (c *Client) connect(ctx context.Context) (*websocket.Conn, error) {
	wsURL, err := c.wsURL()
	if err != nil {
		return nil, err
	}
	headers := http.Header{}
	c.applyAuth(headers)
	conn, _, err := c.Dialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		return nil, fmt.Errorf("websocket dial /ws: %w", err)
	}

	var info serverInfo
	if err := conn.ReadJSON(&info); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("read server_info: %w", err)
	}
	if info.RequiresAuth {
		if c.Username == "" && c.Password == "" {
			_ = conn.Close()
			return nil, fmt.Errorf("dashboard requires authentication but no username/password were configured")
		}
		if _, err := c.sendOnConn(ctx, conn, "auth/login", map[string]any{
			"username": c.Username,
			"password": c.Password,
		}, 30*time.Second); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("auth/login: %w", err)
		}
	}
	return conn, nil
}

func (c *Client) send(ctx context.Context, command string, args map[string]any, timeout time.Duration) (json.RawMessage, error) {
	conn, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return c.sendOnConn(ctx, conn, command, args, timeout)
}

func (c *Client) sendOnConn(ctx context.Context, conn *websocket.Conn, command string, args map[string]any, timeout time.Duration) (json.RawMessage, error) {
	id := c.nextMessageID()
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetReadDeadline(deadline)
		_ = conn.SetWriteDeadline(deadline)
	} else {
		deadline := time.Now().Add(timeout)
		_ = conn.SetReadDeadline(deadline)
		_ = conn.SetWriteDeadline(deadline)
	}
	if err := conn.WriteJSON(wsRequest{Command: command, MessageID: id, Args: args}); err != nil {
		return nil, fmt.Errorf("send %s: %w", command, err)
	}
	for {
		var resp wsResponse
		if err := conn.ReadJSON(&resp); err != nil {
			return nil, fmt.Errorf("read %s response: %w", command, err)
		}
		if resp.MessageID != strconv.Itoa(id) {
			continue
		}
		if resp.ErrorCode != "" {
			if resp.Details != "" {
				return nil, fmt.Errorf("%s: %s", resp.ErrorCode, resp.Details)
			}
			return nil, fmt.Errorf("%s", resp.ErrorCode)
		}
		return resp.Result, nil
	}
}

// Version returns the ESPHome dashboard version. This is the only REST endpoint
// used by the client; all operational actions use /ws.
func (c *Client) Version(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/version", nil)
	if err != nil {
		return "", err
	}
	headers := http.Header{}
	c.applyAuth(headers)
	req.Header = headers
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("version: %s", resp.Status)
	}
	var out struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("version: %w", err)
	}
	if out.Version == "" {
		return "unknown", nil
	}
	return out.Version, nil
}

// ListDevices returns all configured and importable devices from Device Builder.
func (c *Client) ListDevices(ctx context.Context) (*DeviceListResponse, error) {
	raw, err := c.send(ctx, "devices/list", nil, 30*time.Second)
	if err != nil {
		return nil, err
	}
	var out DeviceListResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("devices/list: %w", err)
	}
	return &out, nil
}

// Ping runs the Device Builder ping command and returns configured device states.
func (c *Client) Ping(ctx context.Context) (map[string]bool, error) {
	if _, err := c.send(ctx, "ping", nil, 10*time.Second); err != nil {
		return nil, err
	}
	devices, err := c.ListDevices(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(devices.Configured))
	for _, d := range devices.Configured {
		online := strings.EqualFold(d.State, "online") || strings.EqualFold(d.Status, "online")
		out[d.Configuration] = online
	}
	return out, nil
}

// GetConfig returns the raw YAML configuration for the named device.
func (c *Client) GetConfig(ctx context.Context, configuration string) (string, error) {
	configuration = ensureYAMLExt(configuration)
	raw, err := c.send(ctx, "devices/get_config", map[string]any{"configuration": configuration}, 30*time.Second)
	if err != nil {
		return "", err
	}
	var out string
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("devices/get_config: unexpected result: %w", err)
	}
	return out, nil
}

// SaveConfig writes the YAML configuration for the named device.
func (c *Client) SaveConfig(ctx context.Context, configuration, yaml string) error {
	configuration = ensureYAMLExt(configuration)
	_, err := c.send(ctx, "devices/update_config", map[string]any{
		"configuration": configuration,
		"content":       yaml,
	}, 30*time.Second)
	return err
}

func formatValidation(raw json.RawMessage) (string, int, error) {
	var result struct {
		YAMLErrors []any `json:"yaml_errors"`
		Errors     []any `json:"validation_errors"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", -1, fmt.Errorf("editor/validate_yaml: %w", err)
	}
	if len(result.YAMLErrors) == 0 && len(result.Errors) == 0 {
		return "Configuration is valid.", 0, nil
	}
	var sb strings.Builder
	for _, item := range result.YAMLErrors {
		fmt.Fprintf(&sb, "YAML error: %s\n", validationMessage(item))
	}
	for _, item := range result.Errors {
		fmt.Fprintf(&sb, "Validation error: %s\n", validationMessage(item))
	}
	return strings.TrimRight(sb.String(), "\n"), 1, nil
}

func validationMessage(item any) string {
	if m, ok := item.(map[string]any); ok {
		if msg, ok := m["message"].(string); ok {
			return msg
		}
	}
	return fmt.Sprint(item)
}

// Validate validates the saved device configuration.
func (c *Client) Validate(ctx context.Context, configuration string) (string, int, error) {
	configuration = ensureYAMLExt(configuration)
	content, err := c.GetConfig(ctx, configuration)
	if err != nil {
		return "", -1, err
	}
	raw, err := c.send(ctx, "editor/validate_yaml", map[string]any{
		"configuration": configuration,
		"content":       content,
	}, 60*time.Second)
	if err != nil {
		return "", -1, err
	}
	return formatValidation(raw)
}

func (c *Client) followJob(ctx context.Context, conn *websocket.Conn, jobID string, timeout time.Duration) (string, int, error) {
	id := c.nextMessageID()
	if timeout <= 0 {
		timeout = 20 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	_ = conn.SetReadDeadline(deadline)
	_ = conn.SetWriteDeadline(deadline)
	if err := conn.WriteJSON(wsRequest{
		Command:   "firmware/follow_job",
		MessageID: id,
		Args:      map[string]any{"job_id": jobID},
	}); err != nil {
		return "", -1, fmt.Errorf("send firmware/follow_job: %w", err)
	}
	var sb strings.Builder
	code := -1
	for {
		var resp wsResponse
		if err := conn.ReadJSON(&resp); err != nil {
			return stripANSI(sb.String()), code, fmt.Errorf("read firmware/follow_job: %w", err)
		}
		if resp.MessageID != strconv.Itoa(id) {
			continue
		}
		if resp.ErrorCode != "" {
			return stripANSI(sb.String()), code, fmt.Errorf("%s: %s", resp.ErrorCode, resp.Details)
		}
		switch resp.Event {
		case "output":
			var line string
			if err := json.Unmarshal(resp.Data, &line); err == nil {
				sb.WriteString(line)
			}
		case "result":
			var result struct {
				ExitCode *int     `json:"exit_code"`
				Output   []string `json:"output"`
			}
			_ = json.Unmarshal(resp.Data, &result)
			if sb.Len() == 0 {
				for _, line := range result.Output {
					sb.WriteString(line)
				}
			}
			if result.ExitCode != nil {
				code = *result.ExitCode
			}
			return stripANSI(sb.String()), code, nil
		}
	}
}

func (c *Client) firmwareJob(ctx context.Context, command string, args map[string]any, timeout time.Duration) (string, int, error) {
	conn, err := c.connect(ctx)
	if err != nil {
		return "", -1, err
	}
	defer conn.Close()
	raw, err := c.sendOnConn(ctx, conn, command, args, 60*time.Second)
	if err != nil {
		return "", -1, err
	}
	var job struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(raw, &job); err != nil || job.JobID == "" {
		return "", -1, fmt.Errorf("%s: no job_id in response", command)
	}
	return c.followJob(ctx, conn, job.JobID, timeout)
}

// Compile runs firmware compilation for the device and returns output + exit code.
func (c *Client) Compile(ctx context.Context, configuration string) (string, int, error) {
	return c.firmwareJob(ctx, "firmware/compile", map[string]any{
		"configuration": ensureYAMLExt(configuration),
	}, 20*time.Minute)
}

// Install compiles and uploads firmware to the device. OTA is the default port.
func (c *Client) Install(ctx context.Context, configuration, port string) (string, int, error) {
	if port == "" {
		port = "OTA"
	}
	return c.firmwareJob(ctx, "firmware/install", map[string]any{
		"configuration": ensureYAMLExt(configuration),
		"port":          port,
		"force_local":   false,
	}, 30*time.Minute)
}

// Logs streams device logs for up to maxLines or timeout, then stops the stream.
func (c *Client) Logs(ctx context.Context, configuration, port string, maxLines int, timeout time.Duration) (string, error) {
	configuration = ensureYAMLExt(configuration)
	if port == "" {
		port = "OTA"
	}
	if maxLines <= 0 {
		maxLines = 100
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	conn, err := c.connect(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close()

	streamID := c.nextMessageID()
	deadline := time.Now().Add(timeout)
	_ = conn.SetReadDeadline(deadline)
	_ = conn.SetWriteDeadline(deadline)
	if err := conn.WriteJSON(wsRequest{
		Command:   "devices/logs",
		MessageID: streamID,
		Args: map[string]any{
			"configuration": configuration,
			"port":          port,
		},
	}); err != nil {
		return "", fmt.Errorf("send devices/logs: %w", err)
	}

	var sb strings.Builder
	lines := 0
	for lines < maxLines {
		var resp wsResponse
		if err := conn.ReadJSON(&resp); err != nil {
			break
		}
		if resp.MessageID != strconv.Itoa(streamID) {
			continue
		}
		if resp.ErrorCode != "" {
			return stripANSI(sb.String()), fmt.Errorf("%s: %s", resp.ErrorCode, resp.Details)
		}
		switch resp.Event {
		case "output":
			var line string
			if err := json.Unmarshal(resp.Data, &line); err == nil {
				sb.WriteString(line)
				lines++
			}
		case "result":
			_ = c.stopStream(conn, streamID)
			return stripANSI(sb.String()), nil
		}
	}
	_ = c.stopStream(conn, streamID)
	return stripANSI(sb.String()), nil
}

func (c *Client) stopStream(conn *websocket.Conn, streamID int) error {
	id := c.nextMessageID()
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	if err := conn.WriteJSON(wsRequest{
		Command:   "devices/stop_stream",
		MessageID: id,
		Args:      map[string]any{"stream_id": streamID},
	}); err != nil {
		return err
	}
	for {
		var resp wsResponse
		if err := conn.ReadJSON(&resp); err != nil {
			return nil
		}
		if resp.MessageID == strconv.Itoa(id) {
			return nil
		}
	}
}

// GetEncryptionKey extracts api.encryption.key from the raw YAML returned by
// Device Builder. Secret references cannot be resolved without server-side
// show-secrets support, so callers should pass psk explicitly if the key uses
// !secret.
func (c *Client) GetEncryptionKey(ctx context.Context, configuration string) (string, error) {
	configuration = ensureYAMLExt(configuration)
	if raw, err := c.send(ctx, "devices/get_api_key", map[string]any{"configuration": configuration}, 30*time.Second); err == nil {
		var result struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal(raw, &result); err == nil && result.Key != "" {
			return result.Key, nil
		}
		var key string
		if err := json.Unmarshal(raw, &key); err == nil && key != "" {
			return key, nil
		}
	}
	content, err := c.GetConfig(ctx, configuration)
	if err != nil {
		return "", err
	}
	var root yaml.Node
	if err := yaml.NewDecoder(bytes.NewBufferString(content)).Decode(&root); err != nil && err != io.EOF {
		return "", fmt.Errorf("parse yaml config: %w", err)
	}
	key := findYAMLPath(&root, "api", "encryption", "key")
	if key == nil {
		return "", nil
	}
	if key.Tag != "" && key.Tag != "!!str" {
		return "", fmt.Errorf("api.encryption.key uses %s; provide psk explicitly because Device Builder get_config does not resolve secrets", key.Tag)
	}
	if strings.HasPrefix(strings.TrimSpace(key.Value), "!secret") {
		return "", fmt.Errorf("api.encryption.key uses !secret; provide psk explicitly because Device Builder get_config does not resolve secrets")
	}
	return key.Value, nil
}

func findYAMLPath(node *yaml.Node, path ...string) *yaml.Node {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		return findYAMLPath(node.Content[0], path...)
	}
	if len(path) == 0 {
		return node
	}
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == path[0] {
			return findYAMLPath(node.Content[i+1], path[1:]...)
		}
	}
	return nil
}

func stripANSI(text string) string {
	return ansiPattern.ReplaceAllString(text, "")
}

// ensureYAMLExt makes sure a configuration name ends with .yaml or .yml.
func ensureYAMLExt(name string) string {
	if strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml") {
		return name
	}
	return name + ".yaml"
}
