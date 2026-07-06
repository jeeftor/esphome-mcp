package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type wsCommand struct {
	Command   string         `json:"command"`
	MessageID int            `json:"message_id"`
	Args      map[string]any `json:"args"`
}

func newDeviceBuilderServer(t *testing.T, requiresAuth bool) (*httptest.Server, *[]string) {
	t.Helper()

	var seen []string
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			_ = json.NewEncoder(w).Encode(map[string]string{"version": "2026.7.0"})
		case "/ws":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Fatalf("upgrade websocket: %v", err)
			}
			defer conn.Close()

			if err := conn.WriteJSON(map[string]any{
				"server_version":  "2026.7.0",
				"esphome_version": "2026.7.0",
				"requires_auth":   requiresAuth,
				"status_use_ping": true,
				"streamer_status": "started",
			}); err != nil {
				t.Fatalf("write server_info: %v", err)
			}

			for {
				var cmd wsCommand
				if err := conn.ReadJSON(&cmd); err != nil {
					return
				}
				seen = append(seen, cmd.Command)
				reply := func(result any) {
					t.Helper()
					if err := conn.WriteJSON(map[string]any{
						"message_id": strconv.Itoa(cmd.MessageID),
						"result":     result,
					}); err != nil {
						t.Fatalf("write reply for %s: %v", cmd.Command, err)
					}
				}
				stream := func(lines []string, result map[string]any) {
					t.Helper()
					for _, line := range lines {
						if err := conn.WriteJSON(map[string]any{
							"message_id": strconv.Itoa(cmd.MessageID),
							"event":      "output",
							"data":       line,
						}); err != nil {
							t.Fatalf("write output for %s: %v", cmd.Command, err)
						}
					}
					if err := conn.WriteJSON(map[string]any{
						"message_id": strconv.Itoa(cmd.MessageID),
						"event":      "result",
						"data":       result,
					}); err != nil {
						t.Fatalf("write stream result for %s: %v", cmd.Command, err)
					}
				}

				switch cmd.Command {
				case "auth/login":
					if cmd.Args["username"] != "user" || cmd.Args["password"] != "pass" {
						t.Fatalf("unexpected auth args: %#v", cmd.Args)
					}
					reply(map[string]any{"token": "ok"})
				case "devices/list":
					reply(map[string]any{
						"configured": []map[string]any{{
							"name":             "plug",
							"friendly_name":    "Plug",
							"configuration":    "plug.yaml",
							"state":            "ONLINE",
							"address":          "192.0.2.10",
							"target_platform":  "ESP32",
							"deployed_version": "2026.6.1",
							"current_version":  "2026.7.0",
						}},
						"importable": []map[string]any{{
							"name":               "adopt-me",
							"friendly_name":      "Adopt Me",
							"package_import_url": "github://example/adopt.yaml",
						}},
					})
				case "devices/get_config":
					if cmd.Args["configuration"] != "plug.yaml" {
						t.Fatalf("unexpected get_config args: %#v", cmd.Args)
					}
					reply("esphome:\n  name: plug\napi:\n  encryption:\n    key: abc123\n")
				case "devices/get_api_key":
					if cmd.Args["configuration"] != "plug.yaml" {
						t.Fatalf("unexpected get_api_key args: %#v", cmd.Args)
					}
					reply(map[string]any{"key": "server-key"})
				case "devices/update_config":
					if cmd.Args["configuration"] != "plug.yaml" || cmd.Args["content"] != "new: yaml\n" {
						t.Fatalf("unexpected update_config args: %#v", cmd.Args)
					}
					reply(map[string]any{"saved": true})
				case "editor/validate_yaml":
					if cmd.Args["configuration"] != "plug.yaml" || !strings.Contains(cmd.Args["content"].(string), "name: plug") {
						t.Fatalf("unexpected validate_yaml args: %#v", cmd.Args)
					}
					reply(map[string]any{})
				case "firmware/compile":
					reply(map[string]any{"job_id": "compile-1"})
				case "firmware/install":
					if cmd.Args["port"] != "OTA" || cmd.Args["force_local"] != false {
						t.Fatalf("unexpected install args: %#v", cmd.Args)
					}
					reply(map[string]any{"job_id": "install-1"})
				case "firmware/follow_job":
					stream([]string{"INFO build\n", "SUCCESS\n"}, map[string]any{"exit_code": 0})
				case "devices/logs":
					stream([]string{"[D][sensor:001]: value\n"}, map[string]any{})
				case "devices/stop_stream":
					reply(map[string]any{"stopped": true})
				case "ping":
					reply(map[string]any{"ok": true})
				default:
					_ = conn.WriteJSON(map[string]any{
						"message_id": strconv.Itoa(cmd.MessageID),
						"error_code": "unknown_command",
						"details":    cmd.Command,
					})
				}
			}
		default:
			http.NotFound(w, r)
		}
	}))
	return srv, &seen
}

func TestDeviceBuilderClientUsesModernWebSocketCommands(t *testing.T) {
	srv, seen := newDeviceBuilderServer(t, false)
	defer srv.Close()

	client := New(srv.URL, "", "")
	ctx := context.Background()

	version, err := client.Version(ctx)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if version != "2026.7.0" {
		t.Fatalf("Version = %q, want 2026.7.0", version)
	}

	devices, err := client.ListDevices(ctx)
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if len(devices.Configured) != 1 || devices.Configured[0].Configuration != "plug.yaml" {
		t.Fatalf("ListDevices returned %#v", devices)
	}

	cfg, err := client.GetConfig(ctx, "plug")
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if !strings.Contains(cfg, "name: plug") {
		t.Fatalf("GetConfig returned %q", cfg)
	}

	if err := client.SaveConfig(ctx, "plug", "new: yaml\n"); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	out, code, err := client.Validate(ctx, "plug")
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if code != 0 || out != "Configuration is valid." {
		t.Fatalf("Validate = (%q, %d), want valid/0", out, code)
	}

	out, code, err = client.Compile(ctx, "plug")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if code != 0 || !strings.Contains(out, "SUCCESS") {
		t.Fatalf("Compile = (%q, %d), want SUCCESS/0", out, code)
	}

	out, code, err = client.Install(ctx, "plug", "")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if code != 0 || !strings.Contains(out, "SUCCESS") {
		t.Fatalf("Install = (%q, %d), want SUCCESS/0", out, code)
	}

	logs, err := client.Logs(ctx, "plug", "", 10, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if !strings.Contains(logs, "sensor") {
		t.Fatalf("Logs returned %q", logs)
	}

	want := []string{
		"devices/list",
		"devices/get_config",
		"devices/update_config",
		"devices/get_config",
		"editor/validate_yaml",
		"firmware/compile",
		"firmware/follow_job",
		"firmware/install",
		"firmware/follow_job",
		"devices/logs",
		"devices/stop_stream",
	}
	if strings.Join(*seen, ",") != strings.Join(want, ",") {
		t.Fatalf("commands = %v, want %v", *seen, want)
	}
}

func TestDeviceBuilderClientAuthenticatesWhenRequired(t *testing.T) {
	srv, seen := newDeviceBuilderServer(t, true)
	defer srv.Close()

	client := New(srv.URL, "user", "pass")
	if _, err := client.ListDevices(context.Background()); err != nil {
		t.Fatalf("ListDevices: %v", err)
	}

	wantPrefix := []string{"auth/login", "devices/list"}
	if strings.Join(*seen, ",") != strings.Join(wantPrefix, ",") {
		t.Fatalf("commands = %v, want %v", *seen, wantPrefix)
	}
}

func TestGetEncryptionKeyParsesDeviceBuilderYAML(t *testing.T) {
	srv, _ := newDeviceBuilderServer(t, false)
	defer srv.Close()

	client := New(srv.URL, "", "")
	key, err := client.GetEncryptionKey(context.Background(), "plug")
	if err != nil {
		t.Fatalf("GetEncryptionKey: %v", err)
	}
	if key != "server-key" {
		t.Fatalf("key = %q, want server-key", key)
	}
}
