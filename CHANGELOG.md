# Changelog

All notable changes to this project are documented in this file.

## v1.0.1

Corrected v1 release for modern ESPHome Device Builder deployments.

### Fixed

- Replaced the legacy dashboard HTTP/per-endpoint WebSocket integration with the ESPHome 2026.6+ Device Builder `/ws` command protocol used by ESPHome 2026.7.
- Moved dashboard operations to modern commands: `devices/list`, `devices/get_config`, `devices/update_config`, `editor/validate_yaml`, `firmware/compile`, `firmware/install`, `firmware/follow_job`, `devices/logs`, and `devices/stop_stream`.
- Added Device Builder `auth/login` support when the server reports `requires_auth`.
- Updated native API PSK auto-discovery to prefer Device Builder key lookup, with YAML parsing as a fallback.

### Added

- Added compatibility tool aliases matching the `loryanstrant/ESPHome-MCP` tool surface, including `list_devices`, `list_device_names`, `get_device_configuration`, `edit_device_configuration`, `validate_device_configuration`, `install_device_configuration`, and `update_device`.
- Added dashboard protocol tests covering Device Builder auth, command envelopes, config read/write, validation, firmware jobs, logs, and API key discovery.

## v1.0.0

Initial tagged release for the Go ESPHome MCP server.

### Features

- Added MCP tools for listing ESPHome dashboard devices, reading and saving YAML configs, validating configs, compiling firmware, installing firmware OTA, fetching logs, and listing native API entities.
- Added ESPHome native API entity discovery with Noise PSK encryption and dashboard-based host/key auto-discovery.
- Added stdio mode for local MCP clients and Streamable HTTP mode at `/mcp` for container and remote MCP clients.
- Added multi-architecture release artifacts for Linux, macOS, and Windows on amd64/arm64.
- Added GHCR Docker images for `linux/amd64` and `linux/arm64`.

### Notes

- Superseded by `v1.0.1` for ESPHome 2026.6+ and ESPHome 2026.7 Device Builder deployments.
- The HTTP MCP endpoint should be run on a trusted network or behind access control when exposed beyond localhost.
