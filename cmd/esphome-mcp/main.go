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
	cfgFile   string
	httpAddr  string
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	root := &cobra.Command{
		Use:   "esphome-mcp",
		Short: "ESPHome MCP server",
		Long: "esphome-mcp exposes ESPHome dashboard and native API operations as MCP tools.\n\n" +
			"By default it runs over stdio for use with MCP clients like Claude Code.\n" +
			"Use 'esphome-mcp serve' to run as an HTTP server for remote/Docker deployments.",
		Version: version,
		RunE:    runStdio,
	}
	root.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default: ~/.config/esphome-mcp/config.yaml)")

	// serve subcommand — Streamable HTTP transport for Docker/remote use.
	serveCmd := &cobra.Command{
		Use:   "serve",
		Short: "Run as an HTTP MCP server (Streamable HTTP transport)",
		RunE:  runServe,
	}
	serveCmd.Flags().StringVar(&httpAddr, "http-addr", "0.0.0.0:3333", "HTTP listen address")
	root.AddCommand(serveCmd)

	// version subcommand — print build metadata.
	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Print build version, commit, and date",
		Run: func(*cobra.Command, []string) {
			fmt.Printf("esphome-mcp %s (commit: %s, built: %s)\n", version, commit, buildDate)
		},
	}
	root.AddCommand(versionCmd)

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

// newDashboardClient creates a dashboard client from the current config.
func newDashboardClient() *dashboard.Client {
	dash := dashboard.New(
		viper.GetString("url"),
		viper.GetString("username"),
		viper.GetString("password"),
	)
	// HA addon auth: the addon uses HA Supervisor auth by default, which
	// does NOT support Basic auth. The X-HA-Ingress header bypasses auth
	// for direct port 6052 access (requires port mapping in addon config).
	// If the user sets DISABLE_HA_AUTHENTICATION on the addon, no auth is
	// needed at all.
	//
	// Note: cookie-based login via /login does NOT work for external clients
	// because it requires SUPERVISOR_TOKEN (only available inside the addon
	// container). The ingress header is the only viable auth path for an
	// external MCP server talking to a HA addon with auth enabled.
	if viper.GetBool("ha_addon") || viper.GetBool("ingress") {
		dash.Ingress = true
	}
	return dash
}

// newMCPServer builds the MCP server with all tools registered.
func newMCPServer(dash *dashboard.Client) *server.MCPServer {
	s := server.NewMCPServer(
		"esphome-mcp",
		version,
		server.WithToolCapabilities(false),
	)
	registerTools(s, dash)
	return s
}

// runStdio runs the MCP server over stdio (default mode for Claude Code etc).
func runStdio(_ *cobra.Command, _ []string) error {
	_ = loadConfig()
	dash := newDashboardClient()
	s := newMCPServer(dash)
	if err := server.ServeStdio(s); err != nil {
		return fmt.Errorf("mcp server: %w", err)
	}
	return nil
}

// runServe runs the MCP server as a Streamable HTTP server for Docker/remote
// deployments. The MCP endpoint is available at /mcp.
func runServe(cmd *cobra.Command, _ []string) error {
	_ = loadConfig()
	dash := newDashboardClient()
	s := newMCPServer(dash)

	addr, _ := cmd.Flags().GetString("http-addr")
	fmt.Fprintf(os.Stderr, "esphome-mcp %s listening on http://%s/mcp\n", version, addr)

	httpServer := server.NewStreamableHTTPServer(s, server.WithEndpointPath("/mcp"))
	if err := httpServer.Start(addr); err != nil {
		return fmt.Errorf("http server: %w", err)
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

	// --- Compatibility aliases matching loryanstrant/ESPHome-MCP ---
	s.AddTool(aliasListDevicesTool(), env.handleListDevices)
	s.AddTool(aliasListDeviceNamesTool(), env.handleListDeviceNames)
	s.AddTool(aliasCheckDeviceUpdateTool(), env.handleCheckDeviceUpdate)
	s.AddTool(aliasGetDeviceStatusTool(), env.handleGetDeviceStatus)
	s.AddTool(aliasGetDeviceVersionTool(), env.handleGetDeviceVersion)
	s.AddTool(aliasGetDeviceConfigTool(), env.handleGetConfig)
	s.AddTool(aliasEditDeviceConfigTool(), env.handleEditDeviceConfiguration)
	s.AddTool(aliasValidateDeviceConfigTool(), env.handleValidate)
	s.AddTool(aliasGetDeviceLogsTool(), env.handleGetLogs)
	s.AddTool(aliasInstallDeviceConfigTool(), env.handleInstall)
	s.AddTool(aliasUpdateDeviceTool(), env.handleInstall)
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
		mcp.WithDescription("List entities and their current states for a device via the ESPHome native API (port 6053, Noise-encrypted). The device's host and encryption PSK are auto-discovered from the dashboard when possible; provide them explicitly to override."),
		mcp.WithString("device", mcp.Description("Device name or configuration filename. If provided, host and PSK are auto-discovered from the dashboard.")),
		mcp.WithString("host", mcp.Description("Device hostname or IP address. Required if 'device' is not provided or auto-discovery fails.")),
		mcp.WithNumber("port", mcp.Description("Native API port (default 6053).")),
		mcp.WithString("psk", mcp.Description("Base64-encoded API encryption key (api.encryption.key). Falls back to the ESPHOME_PSK env var / config, or auto-discovery from the dashboard via Device Builder.")),
	)
}

func aliasListDevicesTool() mcp.Tool {
	return mcp.NewTool("list_devices", mcp.WithDescription("List all ESPHome devices configured in the dashboard."))
}

func aliasListDeviceNamesTool() mcp.Tool {
	return mcp.NewTool("list_device_names", mcp.WithDescription("List ESPHome device names, one per line."))
}

func aliasCheckDeviceUpdateTool() mcp.Tool {
	return mcp.NewTool("check_device_update",
		mcp.WithDescription("Check whether an ESPHome device has a firmware update available."),
		mcp.WithString("device_name", mcp.Required(), mcp.Description("Device name.")),
	)
}

func aliasGetDeviceStatusTool() mcp.Tool {
	return mcp.NewTool("get_device_status",
		mcp.WithDescription("Check whether an ESPHome device is online or offline."),
		mcp.WithString("device_name", mcp.Required(), mcp.Description("Device name.")),
	)
}

func aliasGetDeviceVersionTool() mcp.Tool {
	return mcp.NewTool("get_device_version",
		mcp.WithDescription("Get the deployed and current ESPHome firmware versions for a device."),
		mcp.WithString("device_name", mcp.Required(), mcp.Description("Device name.")),
	)
}

func aliasGetDeviceConfigTool() mcp.Tool {
	return mcp.NewTool("get_device_configuration",
		mcp.WithDescription("Get the raw YAML configuration for an ESPHome device."),
		mcp.WithString("device_name", mcp.Required(), mcp.Description("Device name or configuration filename.")),
	)
}

func aliasEditDeviceConfigTool() mcp.Tool {
	return mcp.NewTool("edit_device_configuration",
		mcp.WithDescription("Save a new YAML configuration for an ESPHome device, then validate it."),
		mcp.WithString("device_name", mcp.Required(), mcp.Description("Device name or configuration filename.")),
		mcp.WithString("yaml_content", mcp.Required(), mcp.Description("Full YAML configuration content to write.")),
	)
}

func aliasValidateDeviceConfigTool() mcp.Tool {
	return mcp.NewTool("validate_device_configuration",
		mcp.WithDescription("Validate a device's saved ESPHome configuration."),
		mcp.WithString("device_name", mcp.Description("Device name or configuration filename.")),
		mcp.WithString("device_or_path", mcp.Description("Device name or configuration filename.")),
	)
}

func aliasGetDeviceLogsTool() mcp.Tool {
	return mcp.NewTool("get_device_logs",
		mcp.WithDescription("Fetch recent logs from an ESPHome device."),
		mcp.WithString("device_name", mcp.Required(), mcp.Description("Device name or configuration filename.")),
		mcp.WithNumber("lines", mcp.Description("Maximum number of log lines to collect (default 100).")),
	)
}

func aliasInstallDeviceConfigTool() mcp.Tool {
	return mcp.NewTool("install_device_configuration",
		mcp.WithDescription("Compile and OTA-install firmware to an ESPHome device."),
		mcp.WithString("device_name", mcp.Required(), mcp.Description("Device name or configuration filename.")),
	)
}

func aliasUpdateDeviceTool() mcp.Tool {
	return mcp.NewTool("update_device",
		mcp.WithDescription("Compile and OTA-install the current configuration for an ESPHome device."),
		mcp.WithString("device_name", mcp.Required(), mcp.Description("Device name or configuration filename.")),
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
	device, err := requireDevice(req)
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
	device, err := requireDevice(req)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	yaml := req.GetString("yaml", "")
	if yaml == "" {
		yaml = req.GetString("yaml_content", "")
	}
	if yaml == "" {
		err = fmt.Errorf("missing required string: yaml")
	}
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if err := e.dash.SaveConfig(ctx, device, yaml); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("Saved configuration for %s.", device)), nil
}

func (e *toolEnv) handleValidate(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	device, err := requireDevice(req)
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
	device, err := requireDevice(req)
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
	device, err := requireDevice(req)
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
	device, err := requireDevice(req)
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

func (e *toolEnv) handleListDeviceNames(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	devices, err := e.dash.ListDevices(ctx)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	names := make([]string, 0, len(devices.Configured))
	for _, d := range devices.Configured {
		names = append(names, d.Name)
	}
	if len(names) == 0 {
		return mcp.NewToolResultText("No devices found in the ESPHome dashboard."), nil
	}
	return mcp.NewToolResultText(strings.Join(names, "\n")), nil
}

func (e *toolEnv) handleCheckDeviceUpdate(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	device, err := e.resolveDevice(ctx, req)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	name := displayDeviceName(device)
	if device.DeployedVersion == nil || *device.DeployedVersion == "" {
		return mcp.NewToolResultText(fmt.Sprintf("%s: No deployed version found.", name)), nil
	}
	if device.CurrentVersion == nil || *device.CurrentVersion == "" {
		return mcp.NewToolResultText(fmt.Sprintf("%s: Running version %s. Unable to determine if an update is available.", name, *device.DeployedVersion)), nil
	}
	if *device.DeployedVersion != *device.CurrentVersion {
		return mcp.NewToolResultText(fmt.Sprintf("%s: Update available! Running %s, latest is %s.", name, *device.DeployedVersion, *device.CurrentVersion)), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("%s: Up to date at version %s.", name, *device.DeployedVersion)), nil
}

func (e *toolEnv) handleGetDeviceStatus(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	device, err := e.resolveDevice(ctx, req)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	status := device.State
	if status == "" {
		status = device.Status
	}
	if status == "" {
		status = "unknown"
	}
	addr := "n/a"
	if device.Address != nil && *device.Address != "" {
		addr = *device.Address
	}
	return mcp.NewToolResultText(fmt.Sprintf("%s: %s (address: %s)", displayDeviceName(device), status, addr)), nil
}

func (e *toolEnv) handleGetDeviceVersion(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	device, err := e.resolveDevice(ctx, req)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	deployed := "not yet flashed"
	if device.DeployedVersion != nil && *device.DeployedVersion != "" {
		deployed = *device.DeployedVersion
	}
	current := "unknown"
	if device.CurrentVersion != nil && *device.CurrentVersion != "" {
		current = *device.CurrentVersion
	}
	return mcp.NewToolResultText(fmt.Sprintf("%s:\n  Deployed version: %s\n  Current version: %s", displayDeviceName(device), deployed, current)), nil
}

func (e *toolEnv) handleEditDeviceConfiguration(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	saveResult, _ := e.handleSaveConfig(ctx, req)
	if saveResult.IsError {
		return saveResult, nil
	}
	validateResult, _ := e.handleValidate(ctx, req)
	if validateResult.IsError {
		return mcp.NewToolResultText(saveResult.Content[0].(mcp.TextContent).Text + "\n\nWarning: validation failed: " + validateResult.Content[0].(mcp.TextContent).Text), nil
	}
	return mcp.NewToolResultText(saveResult.Content[0].(mcp.TextContent).Text + "\n\n" + validateResult.Content[0].(mcp.TextContent).Text), nil
}

func (e *toolEnv) handleListEntities(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	host := req.GetString("host", "")
	port := int(req.GetFloat("port", 6053))
	psk := req.GetString("psk", viper.GetString("psk"))
	device := req.GetString("device", "")

	// Auto-discover host and PSK from the dashboard when a device name is
	// provided and the corresponding parameter wasn't explicitly set.
	if device != "" {
		cfgName := device
		if host == "" {
			if h, err := e.lookupDeviceAddress(ctx, cfgName); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("auto-discover host: %v (provide 'host' explicitly)", err)), nil
			} else {
				host = h
			}
		}
		if psk == "" {
			if key, err := e.dash.GetEncryptionKey(ctx, cfgName); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("auto-discover PSK: %v (provide 'psk' explicitly)", err)), nil
			} else if key == "" {
				return mcp.NewToolResultError("device has no api.encryption.key configured; encryption is required for the native API"), nil
			} else {
				psk = key
			}
		}
	}

	if host == "" {
		return mcp.NewToolResultError("host is required (provide 'host' or 'device' for auto-discovery)"), nil
	}
	if psk == "" {
		return mcp.NewToolResultError("psk is required (provide 'psk', set ESPHOME_PSK, or provide 'device' for auto-discovery)"), nil
	}

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

// lookupDeviceAddress finds a device's IP address from Device Builder by
// matching the device name or configuration filename.
func (e *toolEnv) lookupDeviceAddress(ctx context.Context, device string) (string, error) {
	devices, err := e.dash.ListDevices(ctx)
	if err != nil {
		return "", err
	}
	target := device
	if !strings.HasSuffix(target, ".yaml") && !strings.HasSuffix(target, ".yml") {
		target += ".yaml"
	}
	for _, d := range devices.Configured {
		if d.Configuration == target || d.Name == device {
			if d.Address == nil || *d.Address == "" {
				return "", fmt.Errorf("device %q has no known address (may be offline or using host platform)", device)
			}
			return *d.Address, nil
		}
	}
	return "", fmt.Errorf("device %q not found in dashboard", device)
}

func (e *toolEnv) resolveDevice(ctx context.Context, req mcp.CallToolRequest) (dashboard.ConfiguredDevice, error) {
	name, err := requireDevice(req)
	if err != nil {
		return dashboard.ConfiguredDevice{}, err
	}
	devices, err := e.dash.ListDevices(ctx)
	if err != nil {
		return dashboard.ConfiguredDevice{}, err
	}
	target := strings.TrimSuffix(strings.TrimSuffix(strings.ToLower(name), ".yaml"), ".yml")
	for _, d := range devices.Configured {
		cfg := strings.TrimSuffix(strings.TrimSuffix(strings.ToLower(d.Configuration), ".yaml"), ".yml")
		if strings.EqualFold(d.Name, name) || strings.EqualFold(d.FriendlyName, name) || cfg == target {
			return d, nil
		}
	}
	return dashboard.ConfiguredDevice{}, fmt.Errorf("device %q not found in dashboard", name)
}

func requireDevice(req mcp.CallToolRequest) (string, error) {
	for _, key := range []string{"device", "device_name", "device_or_path"} {
		if value := req.GetString(key, ""); value != "" {
			return value, nil
		}
	}
	return "", fmt.Errorf("missing required string: device")
}

func displayDeviceName(device dashboard.ConfiguredDevice) string {
	if device.FriendlyName != "" {
		return device.FriendlyName
	}
	if device.Name != "" {
		return device.Name
	}
	return device.Configuration
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
