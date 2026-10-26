# goder: AI Coding IDE and Desktop Control

[简体中文](goder.md) | **English**

OpenOS embeds [goder](https://git.gorun.fun/share/goder.git) as a built-in app following the official embedding convention (`goder/docs/embedding.md`): a full AI coding workbench (projects / sessions / chat / code review / event console), plus two OpenOS-specific capabilities — the **desktop-control MCP plugin** and the **Capability Exposure Layer (CAP)**.

## Architecture and isolation

- **One dedicated gh server process per user** (own port / data directory / token; tokens never reach the browser);
- The OpenOS server hosts the `goder-serve` gateway subprocess (header identity mode): `/goder-api/*` strips externally forged `X-GRF-*` headers before injecting `X-GRF-UID/X-GRF-Name`; SSE passes through; `/vendor/goder.js` serves the self-contained embed library;
- Auth: after OpenOS login an `openos_token` cookie (httpOnly) is issued; the goder embed component's same-origin requests carry it automatically;
- Frontend: the goder app window dynamically imports the embed library → renders `<goder-app-shell panels="sessions,chat,sidebar,console">`;
- **goder-serve enforces loopback binding** (`OPENOS_GODER_ADDR` non-loopback refuses to start).

## Deployment

**Docker (recommended)**: all goder components are baked into the image (goder-serve + goder.js + gh), ready on deploy — see [Docker deployment](deploy-docker.en.md).

**Local / bare metal**: `make goder` does it (see [self-deployment](deploy-bare-metal.en.md)):

```bash
# make goder is equivalent to:
cd <goder sources> && ./build.sh embed         # build the embed library (needs npm)
cd <goder sources> && go build -o <openos>/server/data/bin/goder-serve ./cmd/goder-serve
cp <goder sources>/frontend/dist-embed/goder.js <openos>/server/data/goder/
# prerequisite: the gh executable on PATH (OPENOS_GH_BIN overrides)
```

Missing components degrade gracefully — the goder app shows a deployment guide, **never a startup error**. `OPENOS_GODER=off` disables it entirely (recommended for pure business hosts).

**Project directories live in the unified home**: goder's "new project" creates a real directory at `~/goder/<project>` in the user's home as that project session's workspace — the AI's project context is **on the same disk** as Files / the real terminal (via the goder embedding convention's `newProject` hook); existing projects stay where they are, unmigrated.

## Provider model

An admin configures any OpenAI-compatible API under "System Settings → AI Service"; saving declares it to the goder-serve host admin plane as the managed `OpenOS` provider (`PUT /goder-api/admin/users/{uid}/providers`) and sets it as default — **the platform key never lands in any user's goder config file**; the `OpenOS` provider seen inside goder forwards through OpenOS's own proxy endpoint (`/api/ai/v1/*`, per-user proxy keys). Clearing the config removes the managed entry. When goder isn't deployed, the settings page says to contact the admin.

## Desktop control (openos MCP plugin)

The AI inside goder (the gh agent) can operate the current user's desktop through the built-in `openos` MCP plugin. Auto-deployed when goder is enabled (installed declaratively via the goder-serve host admin plane), no configuration; existing users self-heal on server restart.

```
gh server (one per user)
  └─ auto-discovers the openos plugin (wrapper script)
       └─ exec openos-server mcp-shim <endpoint> <token> <user>   ← stdio MCP plugin
            ↕ HTTP JSON-RPC (127.0.0.1:7712/mcp, bearer token + X-OS-User)
openos-server in-process osmcp package (os_* tools, full name mcp__openos__os_* on the gh side)
  ├─ answered server-side: apps/user settings/VFS files/bus publish/shares/store (permission-gated)
  └─ frontend bridge: PublishTo(user,"sys:agent") ↓ executed in the browser
              bus.remote("agent:resp") ↑ replies (15s timeout fallback)
```

### Tool inventory

| Category | Tools |
|---|---|
| System | `os_info` (version/user/goder status/online users), `os_apps_list`, `os_users_list` (needs `users.manage`) |
| Windows/desktop | `os_desktop_state`, `os_window_open/close/do` (focus/minimize/maximize/half-screen/move-resize/retitle), `os_app_open_file`, `os_desktop_do` (wallpaper/dark/Dock), `os_notify` (into the notification center) |
| Data | `os_dock_do`, `os_settings_get/put`, `os_fs_list/read/write/mkdir/delete/search/usage` (own home only, ≤8MB, 100MB quota; writes broadcast `fs:changed`); notes/calendar data goes through the capability layer |
| Sharing | `os_share_list/create/revoke` (same owner validation / audit as REST) |
| Store | `os_market_list/install/uninstall` (needs `market.manage` — permission-point gated) |
| Generic channel | `os_bus_publish` (publish any topic to one's own desktop; can drive declarative apps to refresh) |
| Capability layer | `os_caps` (full capability manifest), `os_app_query` (read-only fetch), `os_app_call` (invoke an action), `os_app_ui` (open window + anchor highlight + step guidance) |

**Security boundary**: the MCP endpoint listens on loopback only + bearer token (`<data>/goder/osmcp.json`, 0600, per-user, token↔X-OS-User paired validation); tools validate that the user exists and isn't disabled before executing; **tools only act on the calling user**; writes are audited as `goder.tool`.

**When it takes effect**: installed when a new user first hits `/goder-api` (before that user's gh lazy-starts); self-heals on server restart. Already-running gh servers don't hot-load the plugin — restart openos-server after deploying.

## Capability Exposure Layer (CAP)

What apps expose to the AI converges into **one standard manifest protocol**: any app declares through the same protocol, the AI discovers and executes through 4 generic tools, and **the server never adds tools for new apps**.

### Three capability classes

- `queries`: read-only data queries, **executable without opening the app** (an fn reads the store directly, or a declarative http fetch);
- `actions`: business operations;
- `views`: visual operation entry points (open to a given view + `data-os-cap` anchor highlight + `steps` guidance text the AI can relay to the user).

### Three execution modes (each capability picks one; precedence run > http > window)

- `run`: an fn callback executed by the desktop shell (the app window need not exist);
- `http`: declarative REST — GET/DELETE map args to query parameters, POST/PUT as a JSON body; relative `/api/…` carries the OpenOS login session, absolute URLs do a bare fetch (`auth:true` forces the session along);
- `window`: open/focus a window carrying props (the app routes views by props).

### Four sources (one protocol)

1. **Built-in app code registration** (calendar/notes as the first showcases);
2. **Host declaration** `createOpenOS({ capabilities: { rooms: {...} } })` — zero-intrusion for business systems;
3. **Runtime registration** `OpenOS.registerCapabilities` / `registerApp({ capabilities })` (store bundle apps via `window.OpenOS`);
4. **Declarative apps** via the top-level `agentCaps` config key (pure JSON, http/window modes only).

```ts
// Host declaration example (grf-hotel "query vacant rooms + open the room grid")
createOpenOS({
  capabilities: {
    rooms: {
      label: 'Rooms',
      queries: [{ name: 'vacant', title: 'Query vacant rooms today', http: { url: '/api/rooms/vacant' } }],
      actions: [{ name: 'block', title: 'Block a room type', run: (args) => hotelApi.post('/rooms/block', args) }],
      views: [{ name: 'grid', title: 'Room grid', props: { view: 'grid' }, anchor: 'room-grid',
        steps: ['Pick a floor on the left', 'Click a room-type card for details'] }],
    },
  },
})
```

**AI usage pattern**: `os_caps` lists the manifest → `os_app_query({appId:'rooms', query:'vacant'})` fetches → `os_app_call({appId:'rooms', capability:'block', args:{…}})` acts → `os_app_ui({appId:'rooms', view:'grid'})` guides the user.

**Constraints**: a capability may declare a `perm` permission point as a UX gate, but **the authorization authority always stays with the data owner** (openos REST / the host backend) — the bridge is just a call channel; unknown manifest fields are ignored in both directions (forward-compatible); the frontend round-trip is bounded by a 15s bridge timeout — long tasks should go async inside run (acknowledge first, deliver results via `os_notify` / the bus); iframe web apps can't have inner elements highlighted (focus the window and notify instead).

## goder headless tasks (host machine-to-machine)

`/api/host/goder/tasks*` (X-Exchange-Secret guarded, see the [host integration guide](host-integration.en.md)): host business systems create a gh server session as a given user and run it in the background (fixed write-workspace + auto_approve); after creation the session is appended to the goder-serve server-side session index (group / project id / display name as request params); models resolve to fully-qualified names via the admin plane (default provider first; ambiguity across providers and plan-excluded models fail fast with readable errors — never silently rerouted). The GoderStudio frontend can deep-link `windows.open('goder', {session: <sid>})` to select the session for follow-up.

## Lightweight command channel (sys:cmd)

Any source (AI replies, the terminal, the event bus) can control the desktop by publishing `sys:cmd` — handy for scripts and simple scenarios:

| Command | Effect |
|---|---|
| `{ "cmd": "open", "arg": "calculator" }` | open an app |
| `{ "cmd": "close", "arg": "docs" }` | close all windows of an app |
| `{ "cmd": "file", "arg": "/home/alice/Documents/a.pdf" }` | open a file with the right app |
| `{ "cmd": "wallpaper", "arg": "3" }` | switch wallpaper (1-6) |
| `{ "cmd": "dark", "arg": "on" }` | toggle dark mode |

It complements the openos MCP plugin: `sys:cmd` is a one-way instruction (no reply); the MCP is the AI-facing full control plane (with state queries and structured replies).

## Host briefing and environment passthrough

- **Host briefing (host_context)**: desktop state is assembled into a runtime-environment briefing by the goder app (a generic OpenOS section + business sections declared by the host via `createOpenOS({ goderContext })`), injected into every gh session; headless tasks inject a fixed unattended briefing (no ask_user, approvals auto-granted). Older goder ignores unknown fields — mixed versions coexist fine.
- **Turn event outbound**: `GODER_GH_ENV_GRF_TURN_WEBHOOK_URL/_SECRET` enables outbound turn-settled events from gh (HMAC-signed header `X-GRF-Signature`) — host headless tasks go from polling to event-driven; the receiving end is provided by the deployer.
- Other passthrough variables (`GODER_IDLE_TTL` / `GODER_WEBHOOK_URL/_SECRET` / `GODER_ADMIN_TOKEN`) are in the [configuration reference](configuration.en.md).
