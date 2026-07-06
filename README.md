# esphome-mcp

An [MCP](https://modelcontextprotocol.io) server that lets LLM clients like
Claude manage ESPHome devices: read/write YAML configs, compile, install
firmware OTA, pull logs, and read live entity states.

It talks to two ESPHome transports:

1. **ESPHome Device Builder WebSocket API** (port 6052, `/ws`) — config CRUD,
   validate, compile, install, and logs. This targets the modern ESPHome
   2026.6+ dashboard protocol used by ESPHome 2026.7.
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
| `esphome_list_entities`  | Native API | List entities + current states (auto-discovers PSK)  |

The server also registers compatibility aliases matching
`loryanstrant/ESPHome-MCP`, including `list_devices`, `list_device_names`,
`get_device_configuration`, `edit_device_configuration`,
`validate_device_configuration`, `install_device_configuration`, `update_device`,
and related status/version/log tools.

`esphome_list_entities` can auto-discover the device's host and encryption PSK
from the dashboard — just pass `device` instead of `host`/`psk`. The host is
fetched from `devices/list` and the PSK is fetched from the Device Builder API
when available, with YAML parsing as a fallback.

## Install

### From source

```bash
make install   # installs `esphome-mcp` to $GOBIN
```

### Docker

```bash
docker pull ghcr.io/jeeftor/esphome-mcp:latest
docker run --rm -p 3333:3333 \
  -e ESPHOME_URL=http://homeassistant.local:6052 \
  ghcr.io/jeeftor/esphome-mcp:latest
```

The MCP endpoint is at `http://localhost:3333/mcp` (Streamable HTTP transport).

### Docker Compose

```bash
# Edit compose.yaml to set your ESPHOME_URL, then:
docker compose up -d
```

## Run

### Stdio mode (default — for Claude Code, etc.)

```bash
esphome-mcp
```

Reads config from `~/.config/esphome-mcp/config.yaml` or `ESPHOME_*` env vars.

### HTTP serve mode (for Docker / remote MCP clients)

```bash
esphome-mcp serve --http-addr 0.0.0.0:3333
```

Exposes the MCP Streamable HTTP endpoint at `/mcp`.

### Version

```bash
esphome-mcp version
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
| `ESPHOME_PSK`       | `psk`          | `""` (native API encryption)  |

`esphome_list_entities` can auto-discover the device's host and encryption PSK
from the dashboard — just pass `device` instead of `host`/`psk`. You can also
set `ESPHOME_PSK` as a fallback or provide `psk` per call.

## Auth modes

### Standalone ESPHome (password)

Set `ESPHOME_PASSWORD` (and `ESPHOME_USERNAME` if your dashboard requires it).
The client sends Basic auth during the WebSocket handshake and also performs
Device Builder `auth/login` when the server reports `requires_auth`.

### Home Assistant ESPHome addon

The HA addon uses **HA Supervisor auth** by default, which does **not** support
Basic auth. The `/login` endpoint requires `SUPERVISOR_TOKEN` (only available
inside the addon container), so external clients **cannot** use cookie-based
login. You have two options:

**Option A — Disable addon auth (simplest):**
In the ESPHome addon config, set "Disable external authentication"
(`DISABLE_HA_AUTHENTICATION=true`, aka `leave_front_door_open`) and map port
6052 in the addon's Network section. Then no credentials are needed — just set
`ESPHOME_URL` and go.

**Option B — Ingress bypass (port 6052 mapped, auth still enabled):**
Map port 6052 in the addon config and set `ESPHOME_HA_ADDON=true`. This sends
`X-HA-Ingress: YES` on all requests, which the addon treats as authenticated
(the same way HA's ingress proxy does).

## Use with Claude Code / MCP

### Stdio (local)

**Standalone ESPHome:**

```json
{
  "mcpServers": {
    "esphome": {
      "command": "esphome-mcp",
      "env": {
        "ESPHOME_URL": "http://homeassistant.local:6052",
        "ESPHOME_PASSWORD": "your-dashboard-password"
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
        "ESPHOME_HA_ADDON": "true"
      }
    }
  }
}
```

Note: with auto-discovery, you usually do not need to set `ESPHOME_PSK`;
`esphome_list_entities` asks Device Builder for the API key when you pass the
`device` parameter. If your dashboard cannot expose the key, pass `psk`.

### HTTP (Docker / remote)

If you run `esphome-mcp serve` in a container or on a remote host, point your
MCP client at the HTTP endpoint:

```json
{
  "mcpServers": {
    "esphome": {
      "url": "http://localhost:3333/mcp"
    }
  }
}
```

## Docker

Tagged releases publish multi-architecture images (`linux/amd64`,
`linux/arm64`) to GitHub Container Registry:

```bash
docker pull ghcr.io/jeeftor/esphome-mcp:latest
docker run --rm -p 3333:3333 \
  -e ESPHOME_URL=http://homeassistant.local:6052 \
  -e ESPHOME_HA_ADDON=true \
  ghcr.io/jeeftor/esphome-mcp:latest
```

The image is built from a static Go binary into a minimal `scratch` runtime
(~15 MB). It includes CA certificates for outbound HTTPS to the ESPHome
dashboard. It runs as non-root user 65532.

Build locally:

```bash
make docker
docker run --rm -p 3333:3333 \
  -e ESPHOME_URL=http://host.docker.internal:6052 \
  esphome-mcp:local
```

Or with Docker Compose:

```bash
docker compose up -d
```

## Releases

GitHub Actions runs tests, `go vet`, and a GoReleaser config check before
publishing any tag release. Tags matching `v*` build binaries for
Linux/macOS/Windows (amd64/arm64), publish multi-arch Docker images to GHCR,
and update the GitHub Release notes from `CHANGELOG.md`.

```bash
make test
make lint
make release-check
git tag -a vX.Y.Z -m "Release vX.Y.Z"
git push origin vX.Y.Z
```

## Develop

```bash
make build     # build the binary
make serve     # run as HTTP server on :3333
make test      # run tests (includes a full Noise handshake round-trip)
make lint      # go vet
make help      # list all targets
```

## Notes

- The native API client implements the `Noise_NNpsk0_25519_ChaChaPoly_SHA256`
  handshake required by modern ESPHome firmware. Plaintext (unencrypted) native
  connections are no longer supported by ESPHome as of 2026.1.
- Dashboard auth supports: Basic auth (standalone ESPHome with password), HA
  addon ingress bypass (`X-HA-Ingress` header), or no auth (addon with
  `DISABLE_HA_AUTHENTICATION`). See [Auth modes](#auth-modes) above.
- Dashboard operations use the modern Device Builder `/ws` command bus
  (`devices/list`, `devices/get_config`, `editor/validate_yaml`,
  `firmware/compile`, `firmware/install`, and `devices/logs`).
- `esphome_compile` / `esphome_install` return a summarized build log (errors
  + last N lines) to keep token usage low.
- The HTTP serve mode uses the MCP Streamable HTTP transport, which supports
  session management and is compatible with MCP clients that accept HTTP URLs.
