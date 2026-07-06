# esphome-mcp

An [MCP](https://modelcontextprotocol.io) server that lets LLM clients like
Claude manage ESPHome devices: read/write YAML configs, compile, install
firmware OTA, pull logs, and read live entity states.

It talks to two ESPHome transports:

1. **ESPHome Dashboard HTTP/WebSocket API** (port 6052) — config CRUD,
   compile, install, logs. Works against the dashboard run by `esphome
   dashboard` or the Home Assistant ESPHome addon.
2. **ESPHome Native TCP API** (port 6053, Noise-encrypted) — live entity
   states via `esphome_list_entities`.

## Tools

| Tool                     | Transport  | Description                                            |
| ------------------------ | ---------- | ----------------------------------------------------- |
| `esphome_list_devices`   | Dashboard  | List configured devices + online status + versions   |
| `esphome_get_config`     | Dashboard  | Get raw YAML for a device                             |
| `esphome_save_config`    | Dashboard  | Overwrite a device's YAML                             |
| `esphome_validate_config`| Dashboard  | Validate config without compiling                     |
| `esphome_compile`        | Dashboard  | Compile firmware (summarized output)                  |
| `esphome_install`        | Dashboard  | Compile + OTA install                                 |
| `esphome_get_logs`       | Dashboard  | Fetch the last N log lines                            |
| `esphome_list_entities`  | Native API | List entities + current states (requires API PSK)     |

## Install

```bash
make install   # installs `esphome-mcp` to $GOBIN
```

## Configure

Config is read from `~/.config/esphome-mcp/config.yaml` (see
`config.example.yaml`) or environment variables prefixed with `ESPHOME_`:

| Env var           | Config key     | Default                       |
| ----------------- | -------------- | ----------------------------- |
| `ESPHOME_URL`     | `url`          | `http://localhost:6052`       |
| `ESPHOME_USERNAME`| `username`     | `""`                          |
| `ESPHOME_PASSWORD`| `password`     | `""`                          |
| `ESPHOME_PSK`     | `psk`          | `""` (native API encryption)  |

`esphome_list_entities` takes `host`, `port`, and `psk` per call; the `psk`
falls back to `ESPHOME_PSK` / the `psk` config key. The PSK is the
base64-encoded `api.encryption.key` from the device's YAML.

## Use with Claude Code / MCP

Add to `.mcp.json`:

```json
{
  "mcpServers": {
    "esphome": {
      "command": "esphome-mcp",
      "env": {
        "ESPHOME_URL": "http://homeassistant.local:6052",
        "ESPHOME_PASSWORD": "your-dashboard-password",
        "ESPHOME_PSK": "base64-api-encryption-key"
      }
    }
  }
}
```

## Develop

```bash
make build     # build the binary
make test      # run tests (includes a full Noise handshake round-trip)
make lint      # go vet
```

## Notes

- The native API client implements the `Noise_NNpsk0_25519_ChaChaPoly_SHA256`
  handshake required by modern ESPHome firmware. Plaintext (unencrypted) native
  connections are no longer supported by ESPHome as of 2026.1.
- Dashboard auth uses HTTP Basic auth via the `Authorization` header, applied
  to both HTTP requests and the WebSocket upgrade.
- `esphome_compile` / `esphome_install` return a summarized build log (errors
  + last N lines) to keep token usage low.
