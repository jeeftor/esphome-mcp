// Command esphome-mcp is an MCP server exposing ESPHome dashboard and native
// API operations as tools for LLM clients such as Claude.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/jeeftor/esphome-mcp/internal/dashboard"
	"github.com/jeeftor/esphome-mcp/internal/native"
)

var (
	cfgFile string
	version = "dev"
)

func main() {
	root := &cobra.Command{
		Use:     "esphome-mcp",
		Short:   "ESPHome MCP server",
		Long:    "esphome-mcp exposes ESPHome dashboard and native API operations as MCP tools over stdio.",
		Version: version,
		RunE:    run,
	}
	root.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default: ~/.config/esphome-mcp/config.yaml)")

	cobra.CheckErr(root.Execute())
}

func init() {
	viper.SetEnvPrefix("esphome")
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	viper.AutomaticEnv()
	viper.SetDefault("url", "http://localhost:6052")
	viper.SetDefault("password", "")
	viper.SetDefault("username", "")
	viper.SetDefault("ha_addon", false)
	viper.SetDefault("ha_login", false)
	viper.SetDefault("ingress", false)
	viper.SetDefault("psk", "")
	viper.SetDefault("expected_name", "")
}

func loadConfig() error {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		viper.SetConfigName("config")
		viper.SetConfigType("yaml")
		viper.AddConfigPath("$HOME/.config/esphome-mcp")
		viper.AddConfigPath(".")
	}
	// Config file is optional; env vars and defaults still apply.
	_ = viper.ReadInConfig()
	return nil
}

func run(cmd *cobra.Command, _ []string) error {
	_ = loadConfig()

	dash := dashboard.New(
		viper.GetString("url"),
		viper.GetString("username"),
		viper.GetString("password"),
	)
	// HA addon: either use the ingress header bypass (when port 6052 is
	// mapped) or perform a cookie-based login against HA Supervisor auth.
	haAddon := viper.GetBool("ha_addon")
	ingress := viper.GetBool("ingress")
	if haAddon || ingress {
		dash.Ingress = true
	} else if viper.GetString("password") != "" && viper.GetBool("ha_login") {
		// HA addon with Supervisor auth: login to get a session cookie.
		// This is the fallback when ingress is not available.
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := dash.Login(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "warning: HA addon login failed: %v\n", err)
		}
		cancel()
	}

	s := server.NewMCPServer(
		"esphome-mcp",
		version,
		server.WithToolCapabilities(false),
	)

	registerTools(s, dash)

	if err := server.ServeStdio(s); err != nil {
		return fmt.Errorf("mcp server: %w", err)
	}
	return nil
}

// toolEnv holds shared dependencies for tool handlers.
type toolEnv struct{ dash *dashboard.Client }

func registerTools(s *server.MCPServer, dash *dashboard.Client) {
	env := &toolEnv{dash: dash}

	// --- Config management ---
	s.AddTool(esphomeListDevicesTool(), env.handleListDevices)
	s.AddTool(esphomeGetConfigTool(), env.handleGetConfig)
	s.AddTool(esphomeSaveConfigTool(), env.handleSaveConfig)
	s.AddTool(esphomeValidateTool(), env.handleValidate)

	// --- Firmware ---
	s.AddTool(esphomeCompileTool(), env.handleCompile)
	s.AddTool(esphomeInstallTool(), env.handleInstall)

	// --- Observability ---
	s.AddTool(esphomeGetLogsTool(), env.handleGetLogs)
	s.AddTool(esphomeListEntitiesTool(), env.handleListEntities)
}

// ---------- Tool definitions ----------

func esphomeListDevicesTool() mcp.Tool {
	return mcp.NewTool("esphome_list_devices",
		mcp.WithDescription("List all ESPHome devices configured in the dashboard, with their online status, platform, and versions."),
	)
}

func esphomeGetConfigTool() mcp.Tool {
	return mcp.NewTool("esphome_get_config",
		mcp.WithDescription("Get the raw YAML configuration for an ESPHome device."),
		mcp.WithString("device", mcp.Required(), mcp.Description("Device name or configuration filename (e.g. 'livingroom' or 'livingroom.yaml').")),
	)
}

func esphomeSaveConfigTool() mcp.Tool {
	return mcp.NewTool("esphome_save_config",
		mcp.WithDescription("Overwrite the YAML configuration for an ESPHome device. The full YAML must be provided."),
		mcp.WithString("device", mcp.Required(), mcp.Description("Device name or configuration filename.")),
		mcp.WithString("yaml", mcp.Required(), mcp.Description("Full YAML configuration content to write.")),
	)
}

func esphomeValidateTool() mcp.Tool {
	return mcp.NewTool("esphome_validate_config",
		mcp.WithDescription("Validate a device's configuration without compiling. Returns the validation output."),
		mcp.WithString("device", mcp.Required(), mcp.Description("Device name or configuration filename.")),
	)
}

func esphomeCompileTool() mcp.Tool {
	return mcp.NewTool("esphome_compile",
		mcp.WithDescription("Compile firmware for a device. Returns a summary of the build output (errors and last lines)."),
		mcp.WithString("device", mcp.Required(), mcp.Description("Device name or configuration filename.")),
	)
}

func esphomeInstallTool() mcp.Tool {
	return mcp.NewTool("esphome_install",
		mcp.WithDescription("Compile and OTA-install firmware to a device. Returns a summary of the install output."),
		mcp.WithString("device", mcp.Required(), mcp.Description("Device name or configuration filename.")),
		mcp.WithString("port", mcp.Description("Upload target. Defaults to 'OTA'. May also be a serial port or IP address.")),
	)
}

func esphomeGetLogsTool() mcp.Tool {
	return mcp.NewTool("esphome_get_logs",
		mcp.WithDescription("Fetch recent logs from a device via the dashboard. Returns the last N lines."),
		mcp.WithString("device", mcp.Required(), mcp.Description("Device name or configuration filename.")),
		mcp.WithNumber("lines", mcp.Description("Maximum number of log lines to collect (default 100).")),
	)
}

func esphomeListEntitiesTool() mcp.Tool {
	return mcp.NewTool("esphome_list_entities",
		mcp.WithDescription("List entities and their current states for a device via the ESPHome native API (port 6053). Requires the device's API encryption PSK."),
		mcp.WithString("host", mcp.Required(), mcp.Description("Device hostname or IP address for the native API connection.")),
		mcp.WithNumber("port", mcp.Description("Native API port (default 6053).")),
		mcp.WithString("psk", mcp.Description("Base64-encoded API encryption key (api.encryption.key). Falls back to the ESPHOME_PSK env var / config.")),
	)
}

// ---------- Tool handlers ----------

func (e *toolEnv) handleListDevices(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	devices, err := e.dash.ListDevices(ctx)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	ping, pingErr := e.dash.Ping(ctx)

	var sb strings.Builder
	if len(devices.Configured) == 0 {
		sb.WriteString("No configured devices found.\n")
	} else {
		for _, d := range devices.Configured {
			online := "?"
			if pingErr == nil {
				if v, ok := ping[d.Configuration]; ok {
					if v {
						online = "online"
					} else {
						online = "offline"
					}
				}
			}
			platform := ""
			if d.TargetPlatform != nil {
				platform = *d.TargetPlatform
			}
			addr := ""
			if d.Address != nil {
				addr = *d.Address
			}
			fmt.Fprintf(&sb, "- %s (%s) [%s] %s — %s\n", d.Name, d.Configuration, platform, online, addr)
			if d.DeployedVersion != nil && d.CurrentVersion != nil && *d.DeployedVersion != *d.CurrentVersion {
				fmt.Fprintf(&sb, "    update available: %s -> %s\n", *d.DeployedVersion, *d.CurrentVersion)
			}
		}
	}
	if len(devices.Importable) > 0 {
		sb.WriteString("\nAdoptable devices:\n")
		for _, d := range devices.Importable {
			fmt.Fprintf(&sb, "- %s (%s)\n", d.Name, d.ProjectName)
		}
	}
	return mcp.NewToolResultText(strings.TrimRight(sb.String(), "\n")), nil
}

func (e *toolEnv) handleGetConfig(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	device, err := req.RequireString("device")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	yaml, err := e.dash.GetConfig(ctx, device)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(yaml), nil
}

func (e *toolEnv) handleSaveConfig(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	device, err := req.RequireString("device")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	yaml, err := req.RequireString("yaml")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if err := e.dash.SaveConfig(ctx, device, yaml); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("Saved configuration for %s.", device)), nil
}

func (e *toolEnv) handleValidate(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	device, err := req.RequireString("device")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	out, code, err := e.dash.Validate(ctx, device)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(summarize(out, code, 40)), nil
}

func (e *toolEnv) handleCompile(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	device, err := req.RequireString("device")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	out, code, err := e.dash.Compile(ctx, device)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(summarize(out, code, 20)), nil
}

func (e *toolEnv) handleInstall(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	device, err := req.RequireString("device")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	port := req.GetString("port", "OTA")
	out, code, err := e.dash.Install(ctx, device, port)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(summarize(out, code, 20)), nil
}

func (e *toolEnv) handleGetLogs(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	device, err := req.RequireString("device")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	lines := int(req.GetFloat("lines", 100))
	out, err := e.dash.Logs(ctx, device, "OTA", lines, 30*time.Second)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if out == "" {
		out = "(no log output received)"
	}
	return mcp.NewToolResultText(out), nil
}

func (e *toolEnv) handleListEntities(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	host, err := req.RequireString("host")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	port := int(req.GetFloat("port", 6053))
	psk := req.GetString("psk", viper.GetString("psk"))

	nc := &native.Client{Host: host, Port: port, PSK: psk, ExpectedName: viper.GetString("expected_name")}
	res, err := nc.ListEntities(ctx, 3*time.Second)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	// Build a map of key -> state for joining.
	states := make(map[uint32]string, len(res.States))
	for _, s := range res.States {
		states[s.Key] = s.State
	}

	type row struct {
		Platform string `json:"platform"`
		Name     string `json:"name"`
		State    string `json:"state,omitempty"`
		ObjectID string `json:"object_id,omitempty"`
	}
	rows := make([]row, 0, len(res.Entities))
	for _, ent := range res.Entities {
		r := row{Platform: ent.Platform, Name: ent.Name, ObjectID: ent.ObjectID}
		if s, ok := states[ent.Key]; ok {
			r.State = s
		}
		rows = append(rows, r)
	}

	out, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(out)), nil
}

// summarize trims long command output to errors + the last N lines, prefixed
// with the exit code.
func summarize(output string, code int, lastN int) string {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	var errors []string
	for _, l := range lines {
		if strings.Contains(l, "error") || strings.Contains(l, "ERROR") || strings.Contains(l, "FAILED") {
			errors = append(errors, l)
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Exit code: %d\n", code)
	if len(errors) > 0 {
		fmt.Fprintf(&sb, "\nErrors (%d):\n", len(errors))
		for _, e := range errors {
			sb.WriteString(e)
			sb.WriteByte('\n')
		}
	}
	tail := lines
	if len(tail) > lastN {
		tail = tail[len(tail)-lastN:]
	}
	fmt.Fprintf(&sb, "\nLast %d lines:\n", len(tail))
	for _, l := range tail {
		sb.WriteString(l)
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}
