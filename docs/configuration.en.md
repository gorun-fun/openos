# Configuration Reference

[简体中文](configuration.md) | **English**

All OpenOS configuration is injected via environment variables, each with a sensible default — a minimal deployment starts with zero config. Whatever admins adjust online (AI service, apps, roles & permissions, …) persists in SQLite and takes precedence over env bootstrap values.

## Basics

| Variable | Default | Notes |
|---|---|---|
| `OPENOS_ADDR` | `127.0.0.1:8081` | HTTP listen address (`0.0.0.0:8081` in the container; pair with a reverse proxy when exposed) |
| `OPENOS_DATA` | `data` (relative to the working directory) | runtime data directory: SQLite, `homes/<user>`, attachments, goder sessions, … |
| `OPENOS_DIST` | `web/dist` | frontend static directory; run.sh / the container inject an absolute path |
| `OPENOS_SEED_DEMO` | on | `0` = don't seed the default demo accounts (admin/user/guest) and sample files |
| `OPENOS_JWT_SECRET` | auto-generated and persisted | JWT signing key (stored in the `system_kv` table); **must be pinned explicitly when instances share sessions** |
| `OPENOS_TOKEN_TTL` | `24h` | token lifetime (Go duration, e.g. `168h`); the frontend renews adaptively per the `tokenTtl` response |
| `OPENOS_TRUST_PROXY` | off | `1` = bucket brute-force counters by the last `X-Forwarded-For` hop (single-layer proxy semantics; recommended behind a proxy) |

## AI service

| Variable | Notes |
|---|---|
| `OPENOS_AI_BASE_URL` | OpenAI-compatible API base URL (e.g. `https://open.bigmodel.cn/api/paas/v4`) |
| `OPENOS_AI_API_KEY` | API key (stored server-side only; masked in API responses) |
| `OPENOS_AI_MODEL` | default model name |

The above only **bootstrap the first start**; afterwards the online config under "System Settings → AI Service" wins (saved changes apply immediately, no restart).

## goder (AI coding IDE)

| Variable | Default | Notes |
|---|---|---|
| `OPENOS_GODER` | on | `off` = disable entirely (no subprocess, app removed) |
| `OPENOS_GODER_BIN` | `<data>/bin/goder-serve` | goder-serve gateway binary (`make goder` output; the feature turns itself off if missing) |
| `OPENOS_GODER_JS` | `<data>/goder/goder.js` | goder embed library path |
| `OPENOS_GODER_ADDR` | `127.0.0.1:7711` | goder-serve listen address (**loopback enforced**; non-loopback refuses to start) |
| `OPENOS_GH_BIN` | `gh` on PATH | gh executable path (goder runtime dependency) |
| `GODER_REPO` / `GODER_SRC` | `git.gorun.fun/share/goder.git` / `.goder-src` | `make goder` only: source repository / local clone directory |
| `GODER_SETTINGS_SEED` | — | goder model-config seed switch (passed through the embedding convention) |

**goder subprocess environment passthrough** (passed through when present; `OPENOS_*` secrets are never inherited):

- `GODER_GH_ENV_<KEY>=<value>`: prefix stripped, injected as `KEY=<value>` into every gh subprocess. Typical: `GODER_GH_ENV_GRF_TURN_WEBHOOK_URL` / `_SECRET` (turn-settled events posted out, HMAC-signed header `X-GRF-Signature`)
- `GODER_IDLE_TTL`: idle reclamation of gh processes
- `GODER_WEBHOOK_URL` / `GODER_WEBHOOK_SECRET`: user-process lifecycle event callbacks
- `GODER_ADMIN_TOKEN`: goder-serve ops-plane token (when openos doesn't configure one, a random value is generated per process start and injected)

## Desktop-control MCP plugin (osmcp)

| Variable | Default | Notes |
|---|---|---|
| `OPENOS_OSMCP` | on | `off` = disable the goder desktop-control plugin |
| `OPENOS_OSMCP_ADDR` | `127.0.0.1:7712` | MCP service listen address (loopback enforced; falls back to a random port if taken) |
| `OPENOS_OSMCP_ALLOW_REMOTE` | off | `1` = allow non-loopback listening (split-container deployments; you own the network boundary) |

## Feature trimming (data boundary)

| Variable | Default | Notes |
|---|---|---|
| `OPENOS_VFS` | on | `off` = remove the files/terminal built-in apps, `/api/fs/*` returns 503 (host mode: business files belong to the integrator) |
| `OPENOS_GODER` | on | `off` = disable goder (see above) |
| `OPENOS_MARKET` | on | `off` = remove the app store and `/api/market/*` |
| `OPENOS_MARKET_URL` | official catalog URL | the store's official default catalog; `off` disables only the official source (custom sources still work) |
| `OPENOS_PROXY` | on | `off` = remove the unified proxy routes `/app/*` and `/px/*` |
| `OPENOS_STEWARD` | on | `off` = remove Code Steward (the engine and `/api/steward/*`) |

## Real terminal

| Variable | Default | Notes |
|---|---|---|
| `OPENOS_REAL_SHELL` | `off` | `on` = restricted execution (cd tracking); `full` = directory restriction lifted. **"Restricted" in `on` is not a filesystem sandbox** — commands run with the server process's privileges; keep `off` on public deployments, see [security notes](security.en.md) |

## Host integration (machine-to-machine)

| Variable | Default | Notes |
|---|---|---|
| `OPENOS_EXCHANGE_SECRET` | empty = disabled | fully-trusted inter-host secret guarding: token exchange `/api/auth/exchange`, JWT introspection `/api/auth/introspect`, role declare `/api/host/declare`, and the machine-to-machine group `/api/host/*`. **Equivalent to admin credentials — trusted networks only** |
| `OPENOS_EXCHANGE_ROLES` | empty | role whitelist mintable via exchange (comma-separated) — a leaked key still can't mint admin; **strongly recommended** |

## Data retention

| Variable | Default | Notes |
|---|---|---|
| `OPENOS_AUDIT_RETENTION_DAYS` | `90` | audit log retention days (0 = forever); cleaned at startup and daily |
| `OPENOS_CHAT_RETENTION_DAYS` | `90` | chat history retention days (0 = forever) |
| `OPENOS_ATTACH_RETENTION_DAYS` | `30` | system attachment retention days (0 = forever) |

## Others

| Variable | Default | Notes |
|---|---|---|
| `OPENOS_FS_WATCH` | on (3s rounds) | `off` = disable the unified-home disk-change polling watcher (the `fs:changed` fallback for writes bypassing REST from goder/terminal) |
| `OPENOS_HOLIDAY_SYNC` | on | `off` = the calendar uses only built-in holiday data (air-gapped intranets) |
| `OPENOS_ROOM_URL` | empty | game room service URL (store game apps discover it via `/api/rooms/config` and connect directly with a token) |

## Desktop client (openos-desktop)

| Variable | Default | Notes |
|---|---|---|
| `OPENOS_DESKTOP_CONF_PORT` | `17700` | client "choose server" loopback settings page port (auto-increments if taken) |
| `OPENOS_DESKTOP_UPDATE_URL` | `https://download.gorun.fun/openos/cli` | auto-update channel URL |
| `OPENOS_DESKTOP_URL` | — | passed by run.sh to the client as `-url` for a direct connection (skips the picker; current run only) |
| `OPENOS_DESKTOP_DOC_REFERRER` | built-in per-site value | exemption value for the desktop-mode document.referrer spoof (site container adaptation) |
