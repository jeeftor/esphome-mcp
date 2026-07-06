package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// wsCommand runs a dashboard WebSocket command (compile, upload, validate,
// logs) and returns the combined line output and the process exit code.
//
// The ESPHome dashboard WebSocket protocol: the client sends a single
// {"type":"spawn", ...} message; the server streams {"event":"line","data":...}
// messages and finishes with {"event":"exit","code":N}.
func (c *Client) wsCommand(ctx context.Context, path string, payload map[string]any, maxLines int, timeout time.Duration) (string, int, error) {
	wsURL := strings.Replace(c.BaseURL, "http://", "ws://", 1)
	if strings.HasPrefix(c.BaseURL, "https://") {
		wsURL = strings.Replace(c.BaseURL, "https://", "wss://", 1)
	}
	wsURL += path

	headers := http.Header{}
	if h := c.authHeader(); h != "" {
		headers.Set("Authorization", h)
	}

	conn, _, err := c.Dialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		return "", -1, fmt.Errorf("websocket dial %s: %w", path, err)
	}
	defer conn.Close()

	// The dashboard ignores messages until "spawn" is received.
	payload["type"] = "spawn"
	spawn, err := json.Marshal(payload)
	if err != nil {
		return "", -1, err
	}
	if err := conn.WriteMessage(websocket.TextMessage, spawn); err != nil {
		return "", -1, fmt.Errorf("websocket send spawn: %w", err)
	}

	var sb strings.Builder
	lines := 0
	code := -1

	deadline := time.Now().Add(timeout)
	if maxLines <= 0 {
		maxLines = 1 << 30
	}

	for lines < maxLines {
		if timeout > 0 {
			if err := conn.SetReadDeadline(deadline); err != nil {
				break
			}
		}
		_, msg, err := conn.ReadMessage()
		if err != nil {
			// A timeout or network close ends the stream; whatever we have is
			// returned alongside the last known exit code.
			break
		}
		var ev struct {
			Event string `json:"event"`
			Data  string `json:"data"`
			Code  int    `json:"code"`
		}
		if err := json.Unmarshal(msg, &ev); err != nil {
			continue
		}
		switch ev.Event {
		case "line":
			sb.WriteString(ev.Data)
			lines++
		case "exit":
			code = ev.Code
			return sb.String(), code, nil
		}
	}
	return sb.String(), code, nil
}

// Validate runs `esphome config` on the device and returns the output + exit code.
func (c *Client) Validate(ctx context.Context, configuration string) (string, int, error) {
	configuration = ensureYAMLExt(configuration)
	return c.wsCommand(ctx, "/validate", map[string]any{"configuration": configuration}, 1<<30, 5*time.Minute)
}

// Compile runs `esphome compile` on the device and returns the output + exit code.
func (c *Client) Compile(ctx context.Context, configuration string) (string, int, error) {
	configuration = ensureYAMLExt(configuration)
	return c.wsCommand(ctx, "/compile", map[string]any{"configuration": configuration}, 1<<30, 20*time.Minute)
}

// Install runs `esphome upload` (OTA by default) and returns the output + exit code.
func (c *Client) Install(ctx context.Context, configuration, port string) (string, int, error) {
	configuration = ensureYAMLExt(configuration)
	if port == "" {
		port = "OTA"
	}
	return c.wsCommand(ctx, "/upload", map[string]any{
		"configuration": configuration,
		"port":          port,
	}, 1<<30, 20*time.Minute)
}

// Logs streams the device logs for up to maxLines lines (or until timeout),
// then closes the stream and returns what was collected.
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
	out, _, err := c.wsCommand(ctx, "/logs", map[string]any{
		"configuration": configuration,
		"port":          port,
	}, maxLines, timeout)
	return out, err
}

// encodeQuery is a small helper kept for future endpoints needing complex params.
func encodeQuery(params map[string]string) string {
	v := url.Values{}
	for k, p := range params {
		v.Set(k, p)
	}
	return v.Encode()
}
