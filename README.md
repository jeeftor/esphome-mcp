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

| Env var             | Config key     | Default                       |
| ------------------- | -------------- | ----------------------------- |
| `ESPHOME_URL`       | `url`          | `http://localhost:6052`       |
| `ESPHOME_USERNAME`  | `username`     | `""`                          |
| `ESPHOME_PASSWORD`  | `password`     | `""`                          |
| `ESPHOME_HA_ADDON`  | `ha_addon`     | `false`                       |
| `ESPHOME_HA_LOGIN`  | `ha_login`     | `false`                       |
| `ESPHOME_PSK`       | `psk`          | `""` (native API encryption)  |

`esphome_list_entities` takes `host`, `port`, and `psk` per call; the `psk`
falls back to `ESPHOME_PSK` / the `psk` config key. The PSK is the
base64-encoded `api.encryption.key` from the device's YAML.

## Auth modes

### Standalone ESPHome (password)

Set `ESPHOME_PASSWORD` (and `ESPHOME_USERNAME` if your dashboard requires it).
Basic auth is sent on all HTTP and WebSocket requests.

### Home Assistant ESPHome addon

The HA addon uses **HA Supervisor auth** by default, which does **not** support
Basic auth. You have three options:

**Option A — Disable addon auth (simplest):**
In the ESPHome addon config, set "Disable external authentication"
(`DISABLE_HA_AUTHENTICATION=true`) and map port 6052. Then no credentials are
needed — just set `ESPHOME_URL` and go.

**Option B — Ingress bypass (port 6052 mapped):**
Map port 6052 in the addon config and set `ESPHOME_HA_ADDON=true`. This sends
`X-HA-Ingress: YES` on all requests, which the addon treats as authenticated
(the same way HA's ingress proxy does).

**Option C — Cookie-based login (HA Supervisor auth):**
Set `ESPHOME_HA_LOGIN=true` with your HA username and password. The server
POSTs to `/login` at startup and captures the session cookie for all
subsequent requests.

## Use with Claude Code / MCP

**Standalone ESPHome:**

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

**Home Assistant ESPHome addon (ingress bypass):**

```json
{
  "mcpServers": {
    "esphome": {
      "command": "esphome-mcp",
      "env": {
        "ESPHOME_URL": "http://homeassistant.local:6052",
        "ESPHOME_HA_ADDON": "true",
        "ESPHOME_PSK": "base64-api-encryption-key"
      }
    }
  }
}
```

**Home Assistant ESPHome addon (cookie login):**

```json
{
  "mcpServers": {
    "esphome": {
      "command": "esphome-mcp",
      "env": {
        "ESPHOME_URL": "http://homeassistant.local:6052",
        "ESPHOME_HA_LOGIN": "true",
        "ESPHOME_USERNAME": "your-ha-user",
        "ESPHOME_PASSWORD": "your-ha-password",
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
- Dashboard auth supports three modes: Basic auth (standalone ESPHome with
  password), HA addon ingress bypass (`X-HA-Ingress` header), and cookie-based
  HA Supervisor login. See [Auth modes](#auth-modes) above.
- `esphome_compile` / `esphome_install` return a summarized build log (errors
  + last N lines) to keep token usage low.
