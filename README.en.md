# OpenOS

**简体中文** | [English](README.en.md)

A macOS-style **web desktop OS**: single-binary Go backend + Vue 3 frontend, a WebSocket event bus at the core, and goder — a built-in **AI coding IDE**. The AI doesn't just write code; it can operate the entire desktop. Built for production use: multi-user permissions, security hardening, audit logging, and containerized deployment.

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE) ![Go](https://img.shields.io/badge/Go-1.26-00ADD8) ![Vue](https://img.shields.io/badge/Vue-3.5-42b883) ![SQLite](https://img.shields.io/badge/SQLite-WAL-003B57) ![Docker](https://img.shields.io/badge/Docker-ready-2496ED) [![Demo](https://img.shields.io/badge/Live%20Demo-openos.gorun.fun-8A2BE2)](https://openos.gorun.fun/)

**👉 Live demo: <https://openos.gorun.fun>**

| Demo account | Password | Role |
|---|---|---|
| `admin` | `admin123` | Administrator (sees all apps and settings) |
| `user` | `user123` | Regular user |
| `guest` | `guest123` | Guest |

<p align="center">
  <img src="docs/screenshots/desktop.png" alt="OpenOS desktop: file manager / terminal / goder AI IDE" width="840">
</p>
<p align="center">
  <em>Desktop: the file manager, terminal, and goder AI coding IDE work side by side, sharing one user home directory</em>
</p>
<p align="center">
  <img src="docs/screenshots/launchpad.png" alt="Launchpad: built-in apps, store apps, and web apps" width="840">
</p>
<p align="center">
  <em>Launchpad: beyond the built-ins, NetEase Cloud Music / Amap / Photopea are zero-code web apps</em>
</p>

## Contents

- [Core Features](#core-features)
- [Quick Start](#quick-start)
- [Connecting an AI Provider](#connecting-an-ai-provider)
- [Architecture](#architecture)
- [Building Your Own Apps](#building-your-own-apps)
- [goder: AI Coding IDE and Desktop Control](#goder-ai-coding-ide-and-desktop-control)
- [App Store](#app-store)
- [Desktop Client](#desktop-client)
- [Configuration Reference](#configuration-reference)
- [Production Deployment](#production-deployment)
- [Security](#security)
- [Project Structure](#project-structure)
- [Contributing](#contributing)

In-depth guides in [`docs/`](docs/README.en.md): [Docker deployment](docs/deploy-docker.en.md) · [Self-deployment](docs/deploy-bare-metal.en.md) · [Configuration reference](docs/configuration.en.md) · [Security notes](docs/security.en.md) · [App development](docs/app-development.en.md) · [Host integration](docs/host-integration.en.md) · [goder and desktop control](docs/goder.en.md) · [Unified proxy](docs/webproxy.en.md) · [Desktop client](docs/desktop-client.en.md) · [FAQ](docs/faq.en.md)

## Core Features

### 🖥 A complete desktop experience

Window management (drag, eight-direction resize, edge snapping to half-screen/maximize, focus z-order, cross-session restore), **Spotlight-style search** (⌘/Alt+Space: apps + file search + instant calculator), Launchpad (F4), a customizable Dock (drag to reorder, neighbor magnification, Trash), an **app switcher** (Alt+Tab — hold to cycle, release to switch), light/dark themes, wallpapers, desktop widgets (clock / notes / calendar), a **notification center** (top-right banners + the history panel behind the menu-bar clock + Do Not Disturb), PWA install, Chinese/English UI.

### 📦 Built-in apps out of the box

- **Files** (Finder-like): icon / list / column / gallery views, drag to move, multi-select, rename in place, Trash, image thumbnails, recursive search, **cross-user share links**
- **Office suite + PDF**: documents, spreadsheets, and slides read and write cloud files directly — double-click to open
- **Real terminal**: controlled server-side `sh -c` execution with ANSI colors (off by default, see [Security](#security))
- **Calendar**: Chinese public holidays (built-in + online sync, with makeup-workday markers), lunar calendar, event reminders via the notification center
- Notes (pinnable to the desktop), calculator, system settings, feedback, user guide

### ✦ Built-in AI: the goder coding IDE

- A per-user AI coding workbench (projects / sessions / chat / code review / event console) sharing one home directory with Files and the terminal — files the AI changes are instantly visible in the Files app
- Works with **any OpenAI-style API**: fill in Base URL / API Key / model name in system settings; keys are stored server-side only
- **The AI can operate the desktop**: the built-in openos MCP plugin lets it open and arrange windows, switch wallpapers, read/write files, send notifications, and invoke capabilities declared by any app
- **Capability Exposure Layer (CAP)**: apps declare `queries / actions / views` in one standard manifest; the AI discovers and executes them through 4 generic tools (`os_caps / os_app_query / os_app_call / os_app_ui`) — **new apps require no server changes**

### 🔌 Open extensibility

Four integration paths, from zero code to full business systems (see [Building Your Own Apps](#building-your-own-apps)):

| Path | Best for | Code required |
|---|---|---|
| ① Declarative app | data dashboards, bulletin boards, monitoring panels | one JSON blob |
| ② Component SDK | light interactive tools, prototypes | one component |
| ③ Host framework integration | build a full business system on OpenOS as a desktop framework | one frontend project |
| ④ Web app | wrap an existing website | none |

Alongside: an **app store** (remote catalog, component apps, auto-upgrade and rollback), a **unified proxy** (server-side reverse proxy that lets protected sites be embedded), cross-app dialogs / launch-and-await result protocols, and multi-device coordination over the event bus.

### 🛡 Multi-user, production-grade engineering

- JWT login (brute-force lockout, hot token revocation), role × app authorization matrix, fine-grained permission points, audit logs
- A real on-disk home directory per user (quota-isolated); user-level data persisted server-side, consistent across devices
- Single binary + SQLite (pure-Go driver, cross-compilable with `CGO_ENABLED=0`), Docker deployment, six-platform desktop client
- Graceful shutdown, retention-based data cleanup, SSO / token exchange / JWT introspection for host interop

## Quick Start

### Docker (fastest)

```bash
git clone https://github.com/gorun-fun/openos.git && cd openos

# Prepare the build context (goder sources + the gh binary; the Dockerfile consumes
# them via COPY — the build fails without them)
mkdir -p .run
git clone --depth 1 https://git.gorun.fun/share/goder.git .run/goder-src
VER=$(sh .run/goder-src/scripts/version.sh gh)
curl -fsSL -o .run/gh "https://download.gorun.fun/grf-harness/linux/${VER}/gh" && chmod +x .run/gh
# (on arm64 machines replace linux with linux-arm64)

OPENOS_DATA_DIR=./data docker compose up -d --build
# → http://localhost:8091 (data persisted under ./data)
```

The image ships the full goder stack (goder-serve gateway + embed library + gh) — build once, deploy ready. The full workflow (upgrade, backup, HTTPS reverse proxy) is in the [Docker deployment guide](docs/deploy-docker.en.md); for bare metal / binary deployment see the [self-deployment guide](docs/deploy-bare-metal.en.md).

### Running from source

```bash
# Dev mode: backend :8081 + frontend :5173 with hot reload
make dev

# Or step by step
cd server && go run .                     # backend → http://127.0.0.1:8081
cd web && npm install && npm run dev      # frontend → http://localhost:5173

# Production build: single binary + static directory
make build && cd server && ./openos-server

# Multi-platform release archives (darwin/linux × amd64/arm64)
make release
```

> To use the goder AI IDE on a local bare-metal setup, run `make goder` first to build its components (sources are cloned automatically; requires go/npm/git, plus a `gh` executable on PATH — goder's runtime dependency, path overridable with `OPENOS_GH_BIN`). When components are missing, the goder app shows an in-app deployment guide; the rest of OpenOS keeps working.

**Default accounts**: `admin/admin123` (admin) · `user/user123` (regular) · `guest/guest123` (guest).
⚠️ After deploying to any reachable environment, change the default passwords immediately under "System Settings → Accounts".

## Connecting an AI Provider

**Recommended: configure in the UI (effective immediately, no restart)** — an admin opens "System Settings → AI Service", fills in the Base URL, API Key, and model name of any OpenAI-compatible API, and can hit "Test connection". Keys are stored server-side only and always masked in the UI.

You can also bootstrap via environment variables on first start:

```bash
export OPENOS_AI_BASE_URL=https://open.bigmodel.cn/api/paas/v4
export OPENOS_AI_API_KEY=sk-xxx
export OPENOS_AI_MODEL=glm-4-flash
```

When unconfigured, AI features run in a local demo mode (e.g. the terminal `ai` command and the open `/api/ai/chat` endpoint).

## Architecture

```
┌────────────────────────── Browser ─────────────────────────┐
│  Desktop Shell (menu bar / Dock / Launchpad / window mgr)   │
│  ├── Base UI library (OsButton / OsInput / OsTable / Os…)   │
│  ├── App SDK (registerApp component · SchemaApp declarative)│
│  └── Apps (files/terminal/office/calendar/goder/store/web)  │
│        │ REST /api/*                │ WS /ws (event bus)    │
└────────┼────────────────────────────┼───────────────────────┘
         ▼                            ▼
┌─────────────────── Go server (single binary) ───────────────┐
│  auth(JWT+anti-brute)  apps(registry+roles)  bus(event hub) │
│  vfs(disk home+quota+share)  ai(hot config+stream)  goder   │
│  market(store)  webproxy(unified proxy)  attach  audit      │
│     SQLite (WAL, pure-Go driver)    homes/<user> (disk home)│
└─────────────────────────────────────────────────────────────┘
```

### Event bus

WebSocket is the core communication channel: topic subscribe / publish, a `*` suffix is a prefix wildcard, publishes are not echoed to the publisher, automatic reconnect with subscription replay.

```jsonc
// client → server
{ "action": "sub",  "topics": ["sys:*", "fs:changed"] }      // subscribe
{ "action": "pub", "topic": "app:calc", "payload": {...} }   // publish

// server → client
{ "topic": "app:calc", "payload": {...}, "from": "alice", "ts": 1760000000 }
```

Any frontend component (third-party apps included) broadcasts with one `bus.emit` line and subscribes with one `bus.on` line — across tabs and devices:

```ts
import { bus } from '@/bus/bus'
bus.on('app:rooms', (payload, from) => refresh(payload))  // subscribe (wildcards supported)
bus.emit('app:rooms', { floor: 3 })                        // local + remote publish
```

Built-in topics include `sys:notify` (notifications), `sys:presence` (online status), `fs:changed` (file changes), `sys:apps-changed` (app list changes), `chat:room` (chat rooms). Business apps define their own `app:<name>` topics and go.

### Unified home

Every user gets a real on-disk directory on the server (`<data>/homes/<user>`): Files, the real terminal, and goder projects **share the same file tree** — `echo hi > a.md` in the terminal, AI edits inside a project, and what the Files app shows after a refresh are all the same thing. REST reads/writes are quota-checked (100MB / 8MB per file) with symlink-escape validation; writes that bypass REST are picked up by a server-side fingerprint poller that broadcasts `fs:changed`.

## Building Your Own Apps

> This section is a quick tour; the full reference (all dataSource fields, the full SDK API, cross-app protocol details) is in the [app development guide](docs/app-development.en.md), deep host-framework integration in the [host integration guide](docs/host-integration.en.md), and web-app reverse-proxy details in the [unified proxy guide](docs/webproxy.en.md).

### Path 1: declarative app (zero frontend code)

An admin pastes a JSON schema under "System Settings → Third-party Apps"; the app is immediately usable and can be authorized for any role:

```jsonc
{
  "title": "Team Bulletin Board",
  "dataSource": {
    "kind": "rest",                       // or "static" (inline data)
    "url": "/api/announcements",          // relative paths carry the login session automatically
    "path": "data.items",                 // response value path
    "interval": 30,                       // optional: polling seconds
    "topic": "app:announcements"          // optional: refresh immediately on this bus message
  },
  "views": [
    { "type": "stats", "fields": [{ "key": "title", "label": "Latest" }] },
    { "type": "table", "title": "List", "columns": [
      { "key": "title", "label": "Title" }, { "key": "author", "label": "Author" }] }
  ]
}
```

View types: `stats` metric cards / `cards` / `table`; cross-origin APIs are forwarded through the unified proxy by default (`direct: true` forces a direct connection).

### Path 2: component SDK (full frontend power)

Third-party scripts (console, injected, or store bundle apps) call the global SDK:

```js
OpenOS.registerApp('todo', {
  name: 'Todo', width: 420, height: 400,
  component: {
    data: () => ({ items: ['Try OpenOS'], text: '' }),
    template: '<div style="padding:16px">…</div>',
  },
})
OpenOS.open('todo')
```

SDK surface: `OpenOS.bus` (event bus) / `OpenOS.api` (REST client with login session) / `OpenOS.ui` (base component library) / `OpenOS.open` / `OpenOS.openFile` (open by file extension) / `OpenOS.toast` / `OpenOS.notify` / `OpenOS.launch` (launch a window and await its result) / `OpenOS.openDialog` (cross-app dialog) / `OpenOS.me()` / `OpenOS.can(perm)` (permission check) / `OpenOS.vue` (Vue named exports), and more.

### Path 3: host framework integration (for full business systems)

**Without forking the shell code**, consume OpenOS as a desktop base framework: the host frontend assembles the desktop with `createOpenOS()`, injecting business apps, icons, branding, SSO, and theming:

```ts
// Host frontend entry (vite alias: @openos → openos web/src)
import { createOpenOS } from '@openos/framework'

createOpenOS({
  apps: { rooms: () => import('./apps/Rooms.vue') },     // business apps
  icons: { rooms: { svg: '<svg…>' } },                   // business icons
  login: { mode: 'sso', url: '/api/auth/sso/login' },    // login goes through host SSO
  brand: { name: 'Business System', logo: '🏨' },
  theme: { accent: '#e5484d' },                          // global accent color
  capabilities: {                                        // expose business capabilities to the AI (next section)
    rooms: {
      queries: [{ name: 'vacant', title: 'Query vacant rooms today', http: { url: '/api/rooms/vacant' } }],
      actions: [{ name: 'block', title: 'Block a room type', run: (args) => hotelApi.post('/rooms/block', args) }],
      views: [{ name: 'grid', title: 'Room grid', props: { view: 'grid' }, anchor: 'room-grid' }],
    },
  },
})
```

The server side pairs with **token exchange**: after the host authenticates a user, the identity is swapped server-to-server for an OpenOS one (first-seen users are provisioned automatically):

```bash
OPENOS_EXCHANGE_SECRET=<shared-secret> ./openos-server

# Host backend call (hand the returned token to the frontend to complete login):
curl -X POST http://127.0.0.1:8081/api/auth/exchange \
  -H 'X-Exchange-Secret: <shared-secret>' -H 'Content-Type: application/json' \
  -d '{"username":"zhang","role":"front","displayName":"Zhang, Front Desk"}'
# → { "token": "<openos JWT>", "user": {...} }
```

Hosts also get: reverse interop `POST /api/auth/introspect` (business backend validates OpenOS JWTs, RFC 7662-style), the machine-to-machine API group `/api/host/*` (users / roles / apps / AI provider management), `POST /api/host/declare` (declarative projection of host roles and permissions — the authoritative store stays in the host), bulk user sync, trimming switches like `OPENOS_VFS=off` (business files managed by the host), and an independent app release channel (hot-release business apps without rebuilding the host image).

### Path 4: web apps (wrap existing sites)

No code needed from the target site — embed an existing website as a desktop app. An admin fills in a name and URL under "System Settings → Third-party Apps → Add web app"; the icon is taken from the site favicon, and once authorized for a role the app appears in the Dock.

Two loading paths:

- **Direct iframe**: the default when the target site sets no anti-framing headers (`X-Frame-Options` / `frame-ancestors`); cross-origin isolation comes free, neither side's scripts can reach the other;
- **Unified proxy (server-side reverse proxy)**: check "app proxy" and the site loads through the OpenOS server — anti-framing response headers are stripped, every in-page URL is rewritten, and target-site cookies are scoped to the proxy path; internal systems and protected sites (e.g. GitHub) open inside the window. Optional fixed request headers (intranet systems authenticating via `Authorization`) and in-page injected scripts (a takeover pipeline for host platforms) can be configured.

Security boundary: proxy targets come only from admin configuration; the SSRF line is at the dial layer (configured targets allowed, everything else rejects intranet addresses); the OpenOS session is stripped before forwarding; access is checked per request against the role-app authorization matrix. Proxied pages execute same-origin with the desktop — **configure trusted apps only**.

### Cross-app coordination

- **Open a dialog and get a result**: `await OpenOS.openDialog('roommgmt:checkin', { roomId })` — a cross-app business dialog; Esc / backdrop-click cancels by default
- **Launch a window and get a result**: `await OpenOS.launch('roommgmt', { view: 'checkin', roomId })` — the target app replies via `launchOf` / `resolveLaunch`; closing the window cancels, and the promise never dangles
- **Cross-app realtime sync**: the backend is the data authority; frontends broadcast changes over `bus.emit('app:<name>:changed', …)` and each app subscribes and refreshes itself

## goder: AI Coding IDE and Desktop Control

OpenOS embeds goder as a built-in app following the official embedding convention (a dedicated gh server subprocess per user + identity reverse proxy + embedded frontend; full reference in the [goder guide](docs/goder.en.md)):

- **Dedicated process and data directory per user**; tokens never reach the browser; the `/goder-api/*` proxy strips forged headers and injects identity
- **Project directories live in the unified home**: `~/goder/<project>` is a real directory — the AI's project context is on the same disk as Files / the terminal
- The Docker image ships the full stack; on bare metal `make goder` builds it once (see [Quick Start](#quick-start)); missing components degrade gracefully into an in-app deployment guide without breaking startup

### Desktop control (openos MCP plugin)

The AI inside goder can operate the current user's desktop through the built-in MCP plugin (auto-deployed when goder is enabled, no configuration):

| Category | Capabilities |
|---|---|
| System & windows | version / online-user queries, open / close / arrange / snap windows, open files, toggle dark mode and wallpaper |
| Files | read / write / search the user's home (reusing REST permissions and quotas; writes broadcast a refresh) |
| Notifications & bus | send system notifications (into the notification center), publish any bus topic to the desktop (can drive declarative apps to refresh) |
| App capabilities | consume queries / actions / views declared by any app via the capability layer's `os_caps / os_app_query / os_app_call / os_app_ui` |
| Administration | app listing; the user directory and store installs are gated by permission points |

Hard boundaries: the MCP endpoint listens on loopback only + bearer token (per-user, token↔user paired validation); tools only act on the calling user; writes are audited.

For lightweight scenarios there is also a bus command channel: publish `{ "cmd": "open", "arg": "calculator" }` to `sys:cmd` to open an app (supports open / close / file / wallpaper / dark).

> The server also ships a "Code Steward" engine: GitLab / GitHub issue sync → confirmed auto-fix via a goder session → commit, push, and optional release. Its app is distributed through the app store (`OPENOS_STEWARD=off` removes it; details in [AGENTS.md](AGENTS.md)).

### Capability Exposure Layer (CAP)

What apps expose to the AI converges into **one standard manifest**. Any app declares through the same protocol (built-in app code / host `createOpenOS({ capabilities })` / runtime `OpenOS.registerCapabilities` / the `agentCaps` key of declarative apps), and the AI discovers and executes through 4 generic tools:

- `queries` read-only queries (fetch data without opening the app) · `actions` business operations · `views` visual entry points (open a window, scroll to and highlight the `data-os-cap` anchor, step-by-step guidance text)
- Execution picks one of three modes: `run` callback (executed by the desktop shell) / `http` declarative REST / `window` open a window with route props
- Permissions: a capability can declare a `perm` permission point as a UX gate, but **the authorization authority always stays with the data owner**

## App Store

The built-in "App Store" app: browse the catalog → details → admin install / update / uninstall; installing picks the authorized roles, and `sys:apps-changed` broadcasts make the app appear on every client in realtime.

- **Multi-source catalog**: the official default source + admin custom sources (up to 10) fetched concurrently and merged, custom sources shadowing the official one on id collision; the shelf lives in its own repository, versioned independently of openos releases
- **Three tiers of catalog entries**: web apps (wrap existing sites), declarative apps (pure JSON), and **component apps** (single-file ESM, ≤2MB, default-exporting a Vue component, capabilities injected via `window.OpenOS` — downloaded to disk at install, no external source at runtime)
- **Auto-upgrade + per-version rollback**: the global auto-upgrade is on by default, preserving authorizations and local historical versions; when a new version misbehaves, switching back to an older one also marks that version to be skipped
- Regular users can browse and use installed apps; install / update is admin-only; `OPENOS_MARKET=off` removes the store entirely

## Desktop Client

`openos-desktop`: drives the local Chrome via CDP and opens OpenOS in app mode; with the same-origin policy and site isolation disabled, web-app iframes **connect directly to real sites** — suited to complex sites a server-side proxy can't cover (SSO / risk control / cookie rotation). Ships a three-layer SameSite cookie bridge for cross-site login, request-level header rewriting, and version auto-update.

```bash
make desktop          # build → ./openos-desktop
./openos-desktop      # server picker on first run; -auto connects directly afterwards
```

Six-platform releases (macOS / Linux / Windows × amd64 / arm64), update channel `download.gorun.fun/openos/cli`. See the [desktop client guide](docs/desktop-client.en.md). Security boundary: `--disable-web-security` applies only to a dedicated profile, and the CDP port binds to 127.0.0.1 only — never point it at an untrusted OpenOS instance.

## Configuration Reference

Key environment variables (all optional, sensible defaults; the full list is in the [configuration reference](docs/configuration.en.md)):

| Variable | Default | Notes |
|---|---|---|
| `OPENOS_ADDR` | `127.0.0.1:8081` | listen address (`0.0.0.0:8081` in the container) |
| `OPENOS_DATA` | `./data` | data directory (SQLite, user homes, attachments, goder sessions, …) |
| `OPENOS_DIST` | embedded frontend | frontend static directory (point multi-instance setups at the same dist) |
| `OPENOS_JWT_SECRET` | auto-generated and persisted | JWT key; **must be pinned explicitly for multi-instance deployments** |
| `OPENOS_TOKEN_TTL` | `24h` | token lifetime (Go duration); the frontend renews adaptively |
| `OPENOS_SEED_DEMO` | on | `0` = no default demo accounts or sample files |
| `OPENOS_REAL_SHELL` | `off` | real terminal: `on` restricted execution / `full` lifts the directory restriction; **keep off on public deployments** |
| `OPENOS_TRUST_PROXY` | off | `1` = bucket brute-force counters by the last `X-Forwarded-For` hop (single-layer proxy semantics) |
| `OPENOS_AI_BASE_URL` / `_API_KEY` / `_MODEL` | — | AI bootstrap on first start (afterwards the online settings win) |
| `OPENOS_GODER` | on | `off` = disable goder (no subprocess, app removed) |
| `OPENOS_GODER_BIN` / `OPENOS_GODER_JS` / `OPENOS_GH_BIN` | auto-discovered | goder component paths |
| `OPENOS_GODER_ADDR` | `127.0.0.1:7711` | goder-serve gateway address (loopback enforced) |
| `OPENOS_OSMCP` / `OPENOS_OSMCP_ADDR` | on / `127.0.0.1:7712` | desktop-control MCP plugin switch and listen address (loopback enforced) |
| `OPENOS_VFS` | on | `off` = remove files/terminal apps, `/api/fs/*` returns 503 (host mode) |
| `OPENOS_MARKET` / `OPENOS_MARKET_URL` | on / official catalog | store switch / default catalog URL (`off` disables only the official source) |
| `OPENOS_PROXY` | on | `off` = remove the unified proxy routes `/app/*` and `/px/*` |
| `OPENOS_HOLIDAY_SYNC` | on | `off` = calendar uses only built-in holiday data (air-gapped intranets) |
| `OPENOS_FS_WATCH` | on | `off` = disable the fingerprint-polling watcher for writes bypassing REST |
| `OPENOS_EXCHANGE_SECRET` | empty = disabled | fully-trusted inter-host secret (token exchange / JWT introspection / `/api/host/*` / declare) |
| `OPENOS_EXCHANGE_ROLES` | empty | role whitelist mintable via exchange (strongly recommended) |
| `OPENOS_ROOM_URL` | empty | game room service URL (store game apps connect directly) |
| `OPENOS_AUDIT_RETENTION_DAYS` | `90` | audit log retention days (0 = forever) |
| `OPENOS_CHAT_RETENTION_DAYS` | `90` | chat history retention days (0 = forever) |
| `OPENOS_ATTACH_RETENTION_DAYS` | `30` | system attachment retention days (0 = forever) |

The full list (including advanced components like the Code Steward) is in [AGENTS.md](AGENTS.md).

## Production Deployment

- [ ] **Change default passwords**: admin / user / guest — or remove unneeded accounts
- [ ] **Configure the AI service**: System Settings → AI Service (or bootstrap via env on first start)
- [ ] **HTTPS**: put it behind an Nginx / Caddy reverse proxy (WebSocket needs the `Upgrade` / `Connection` headers)
- [ ] **Backups**: back up `OPENOS_DATA` regularly (SQLite WAL + the on-disk user home directories; copy while stopped, or `sqlite3 .backup` + rsync)
- [ ] **Pin `OPENOS_JWT_SECRET`** (required when instances share sessions)
- [ ] **Keep `OPENOS_REAL_SHELL=off` on public deployments** (the demo compose enables it explicitly — controlled environments only)
- Data model: system data in a single-file SQLite (WAL), user files under `homes/<user>` on disk; deployment details in [Docker deployment](docs/deploy-docker.en.md) / [self-deployment](docs/deploy-bare-metal.en.md)

Release process: commit, tag, and push (`v0.0.x`, patch-digit increments only) to trigger the CI image build and deploy — see `.gitlab-ci.yml`.

## Security

OpenOS is hardened to production-grade standards (tightened after two security audits in Oct 2026; the full write-up is in the [security doc](docs/security.en.md)). Highlights:

- **Authentication**: passwords use pbkdf2-SHA256 (600k rounds + random salt; legacy hashes upgrade transparently on next login); JWTs carry token_version, but revocation happens only on password change / reset — **identity and authorization are separated**: role / permission / disable changes take effect immediately without logging anyone out
- **Sessions**: scenarios that can't send headers (WS / attachments / store components) use 30s one-shot tickets instead of long-lived JWTs in URLs; cookies are HttpOnly + SameSite=Strict; the frontend renews tokens on a sliding window
- **Defenses**: login brute-force lockout (8 failures per username+IP per 10 minutes) + global rate limiting + username-enumeration timing flattening; slides HTML / Markdown links are sanitized with an XSS allowlist
- **Files**: strictly confined to `/home/<user>` (single file ≤8MB, 100MB quota, 20,000-file cap, symlink-escape validation); users are fully isolated from each other; writes are audited
- **Bus ACL**: `sys:*` broadcasts are reserved to the server; other users' terminal topics can't be wiretapped; client-published system topics are delivered to the same user only
- **Proxy**: the SSRF line is at the dial layer (domains resolve once, validated, then dialed by IP — closes DNS rebinding); the OpenOS session is stripped before forwarding; target-site cookies are scoped to the proxy path
- **Subprocesses**: terminal and goder subprocess environments are allowlisted and never inherit `OPENOS_*` secrets; goder-serve and the MCP endpoint enforce loopback binding
- **Data governance**: audit / chat / attachments are auto-cleaned per retention; AI keys live only on the server and are masked in responses; the goder managed provider goes through the platform's own proxy endpoint (the platform key never lands on any user's disk)
- **Honest disclosure**: the "restricted" mode of the terminal's `on` is only cd / working-directory tracking, **not a filesystem sandbox** — commands run with the server process's privileges; keep it `off` on public deployments or harden it yourself

## Project Structure

```
openos/
├── server/                  # Go backend (single main.go entry + internal/*)
│   └── internal/
│       ├── auth/            # JWT, brute-force protection, token exchange & introspection, users
│       ├── bus/             # WebSocket event bus hub (with ACL)
│       ├── vfs/             # unified home disk FS, quotas, share links, real terminal
│       ├── goder/           # goder-serve subprocess hosting + identity proxy + host admin plane
│       ├── osmcp/           # desktop-control MCP plugin (os_* tools + capability layer)
│       ├── market/          # app store (catalog, install, auto-upgrade)
│       ├── webproxy/        # unified proxy (app reverse proxy + generic forwarder, one engine)
│       ├── steward/         # Code Steward engine (issue sync → AI fix → push & release)
│       ├── ai/              # hot-reloaded AI config, SSE streaming, key masking
│       └── attach/          # system attachments (upload / retention)
├── web/                     # Vite + Vue 3 frontend
│   └── src/
│       ├── desktop/         # Desktop / MenuBar / Dock / Launchpad / WindowFrame
│       ├── ui/              # Os* base components, keyboard system, menus / dialogs / tooltips
│       ├── sdk/             # OpenOS global SDK + SchemaApp declarative renderer
│       ├── bus/  api/       # event bus client / REST client
│       ├── apps/            # built-in apps (files terminal docs sheets slides …)
│       ├── framework.ts     # createOpenOS() host shell entry (public API surface)
│       └── tooling/         # host build tooling (deps / host / app three-stage builds)
├── desktop/                 # openos-desktop desktop client (CDP-driven Chrome)
├── hostsdk/                 # standalone Go module: host integration SDK
├── Dockerfile               # multi-stage build (node → go → alpine), non-root runtime
└── docker-compose.yml       # data volume persistence → http://localhost:8091
```

## Contributing

```bash
make dev                     # local development (backend :8081 + frontend :5173)
make test                    # Go unit tests (auth/bus/vfs/osmcp/webproxy…)
cd web && npm run typecheck  # frontend type check (vue-tsc)
```

- Deep architecture docs and development conventions live in [AGENTS.md](AGENTS.md) (communication protocols, security invariants, module boundaries)
- Code comments are written in Chinese; the root README and `docs/` are bilingual (English counterparts at `*.en.md`) — please update both language versions together when changing docs
- Versioning: from `v0.0.x`, each release increments the last digit only; push a tag to trigger CI

## License

This project is open-sourced under the [MIT License](LICENSE).

External components pulled at build time ([goder](https://git.gorun.fun/share/goder) etc.) are governed by their own licenses and are not covered by this repository's LICENSE.
