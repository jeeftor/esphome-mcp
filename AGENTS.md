# Agent Instructions

## ESPHome API Knowledge
- The HA ESPHome addon uses HA Supervisor auth by default, NOT Basic auth.
  External clients cannot use cookie-based /login (requires SUPERVISOR_TOKEN
  only available inside the addon container). Use the X-HA-Ingress header
  bypass (ESPHOME_HA_ADDON=true) or disable addon auth.
- Dashboard operations target the ESPHome 2026.6+ Device Builder `/ws` command
  protocol, not the legacy HTTP/per-endpoint WebSocket dashboard API.
- The native API (port 6053) requires Noise encryption (PSK). The PSK can be
  auto-discovered from Device Builder when available; callers can still provide
  `psk` explicitly or set `ESPHOME_PSK`.

## Release Cadence
- After implementing user-facing features or behavior changes, push a patch
  release frequently once verification passes.
- Tag with `git tag -a vX.Y.Z -m "Release vX.Y.Z"` and push to trigger the
  release workflow (GoReleaser + Docker multi-arch images).

## Build
- `make build` — local binary with version ldflags
- `make test` — go test ./...
- `make docker` — local Docker image (scratch-based, ~15MB)
