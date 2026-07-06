# Changelog

All notable changes to this project are documented in this file.

## v1.0.0

Initial tagged release for the Go ESPHome MCP server.

### Features

- Added MCP tools for listing ESPHome dashboard devices, reading and saving YAML configs, validating configs, compiling firmware, installing firmware OTA, fetching logs, and listing native API entities.
- Added ESPHome native API entity discovery with Noise PSK encryption and dashboard-based host/key auto-discovery.
- Added stdio mode for local MCP clients and Streamable HTTP mode at `/mcp` for container and remote MCP clients.
- Added multi-architecture release artifacts for Linux, macOS, and Windows on amd64/arm64.
- Added GHCR Docker images for `linux/amd64` and `linux/arm64`.

### Notes

- The dashboard integration currently targets the legacy ESPHome dashboard HTTP/WebSocket endpoints. ESPHome 2026.6 Device Builder `/ws` protocol support is planned separately.
- The HTTP MCP endpoint should be run on a trusted network or behind access control when exposed beyond localhost.
