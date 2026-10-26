# App Development Guide

[简体中文](app-development.md) | **English**

OpenOS offers third parties four integration paths, from "zero code" to "full frontend project", all sharing the same desktop capabilities (windows, bus, login session, permissions). This doc is the complete reference for the first two (declarative / component SDK) plus cross-app coordination; host framework integration is in the [host integration guide](host-integration.en.md), web apps and the unified proxy in the [unified proxy guide](webproxy.en.md).

- **Path 1: declarative app** — one JSON registration yields a new app, ideal for data dashboards
- **Path 2: component SDK** — `OpenOS.registerApp()` registers a Vue component with full frontend power
- **Path 3: host framework** — `createOpenOS()` builds a business system on OpenOS as a desktop framework
- **Path 4: web app** — wrap an existing website with zero code (direct iframe / server-side proxy)

## Path 1: declarative app (zero frontend code)

An admin pastes a JSON schema under "System Settings → Third-party Apps" (sample templates are one click away); after registration the app is immediately usable from the Dock and can be authorized for any role (authorization changes broadcast to all online clients in realtime).

```jsonc
{
  "title": "Team Bulletin Board",
  "dataSource": {
    "kind": "static",                    // or "rest" (fields below)
    "items": [{ "title": "v1.0 shipped", "author": "admin", "date": "Monday" }]
  },
  "views": [
    { "type": "stats", "fields": [{ "key": "title", "label": "Latest" }] },
    { "type": "cards", "title": "Cards", "fields": [
      { "key": "title", "label": "Title", "primary": true }, { "key": "author", "label": "Author" }] },
    { "type": "table", "title": "List", "columns": [
      { "key": "title", "label": "Title" }, { "key": "date", "label": "Date" }] }
  ]
}
```

Full fields of `dataSource.kind = "rest"`:

| Field | Notes |
|---|---|
| `url` | Request URL. **Relative paths (`/api/...`) carry the current login session by default** (via the unified API client, incl. global 401 handling); cross-origin absolute URLs are forwarded through the unified proxy by default to avoid token leakage, `auth: true` forces the login session along, `direct: true` forces a direct browser connection |
| `method` / `body` | GET by default; `POST` + `body` (JSON object) sends a request body |
| `headers` | Custom request headers (key-value object) |
| `path` | Response value path, dot paths supported (e.g. `"data.items"`) |
| `interval` | Polling interval in seconds (min 5s), for legacy APIs without push |
| `topic` | **Bus-push refresh**: re-fetch immediately when a message on this topic arrives (local or cross-device), wildcards supported (e.g. `"app:rooms"`); stacks with `interval` |

Declarative apps can also declare a top-level `agentCaps` key to expose capabilities to the AI (pure JSON, http/window modes only, see the [goder guide](goder.en.md)).

## Path 2: component SDK (full frontend power)

Third-party scripts (console, injected, or store bundle apps) register apps through the global SDK:

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

> App root element convention: the window content area is a flex row container that doesn't force-stretch its children, so the root element needs `width: 100%` (height fills in via flex). Without it the content shrinks to max-content and won't expand when the window is enlarged.

### SDK surface (`window.OpenOS`)

| API | Notes |
|---|---|
| `OpenOS.bus` | event bus client (`on` / `emit` / `remote`, wildcards supported) |
| `OpenOS.api` | REST client with login session (global 401 logout); `api.upload()` does multipart file uploads |
| `OpenOS.ui` | base component library facade (Os* components) |
| `OpenOS.open(id, props?, opts?)` / `OpenOS.close(id)` | open / close an app window |
| `OpenOS.launch(appId, props, opts)` | **awaitable** open: the target app reports a result when done (below) |
| `OpenOS.resolveLaunch(reqId, result)` | target-side reply (pairs with `launchOf`) |
| `OpenOS.openDialog(id, params, opts?)` | cross-app system-level dialog (`Promise`) |
| `OpenOS.openFile(path)` | open a VFS file with the app matching its extension (md→docs, csv→sheets, html→slides, pdf→PDF, images→quick look) |
| `OpenOS.toast(msg, opts)` | light toast (type: success/error) |
| `OpenOS.notify({title, body, appId, broadcast})` | system notification (into the notification center; `broadcast` crosses devices) |
| `OpenOS.me()` | current user (username / role / permissions) |
| `OpenOS.authToken()` | current JWT (for building `<img>`/`<a>` tag URLs) |
| `OpenOS.can(perm)` | permission-point check (admin bypass / `*` wildcard / exact match — same semantics as the server) |
| `OpenOS.proxyUrl(url)` | build a unified-proxy `/px/` forward URL (encoding follows the compactTarget rules) |
| `OpenOS.calendar` / `OpenOS.registerCapabilities` / `OpenOS.registerAgentTool` | calendar read/write and AI capability declaration (see the [goder guide](goder.en.md)) |
| `OpenOS.vue` | Vue named exports (for store bundle apps writing component logic) |
| `OpenOS.registerDialog(id, def)` / `OpenOS.registerApp` | runtime registration of dialogs / apps |

## Cross-app coordination

Business systems constantly need "app A, mid-flow, needs a capability or a piece of data from app B". OpenOS builds in three channels:

### ① Cross-app dialog — pop app B's business dialog without opening its main window

Dialogs are registered by the providing app (clear ownership, lazy-loadable); callers launch with one line and `await` the business result:

```ts
// Room management registers a "check-in" dialog (the createOpenOS dialogs option
// and runtime OpenOS.registerDialog are equivalent)
createOpenOS({
  dialogs: { 'roommgmt:checkin': { component: () => import('./dialogs/CheckInDialog.vue'), title: 'Check in', width: 520 } },
})

// The orders app: pop the dialog, continue this app's flow with the result
const checkedIn = await OpenOS.openDialog('roommgmt:checkin', { roomId, orderId })
if (checkedIn) toast('Checked in', { type: 'success' })
```

Dialog components take three props: `params` (caller input), `close(result)` (reply; becomes the Promise value), and `cancel()`. Esc / backdrop-click default to cancel (adjustable via the third `openDialog` argument); rendered as a system-level modal overlay, stacking supported.

### ② Launch a window and await the result — app B opens a full window to do the work

When B's capability needs a standalone window (browsing / multi-step flows), use `OpenOS.launch` — the awaitable version of `open()`; any app (store bundle apps included) can be the target:

```ts
// The orders app: launch the room-management window to check in
const stay = await OpenOS.launch('roommgmt', { view: 'checkin', roomId: 42 },
                                 { title: 'Check in', width: 760, from: 'orders' })
if (stay) toast(`Checked in: room ${stay.roomNo}`, { type: 'success' })
```

```vue
<!-- Target app (room management main window): detect launch mode via the reserved
     __launch key in window props -->
<script setup lang="ts">
import { launchOf, resolveLaunch } from '@/ui'
const props = defineProps<{ win: WinState }>()
const launched = launchOf(props.win.props)   // null when opened via plain open(); behavior unchanged
// …check-in flow; on user confirmation:
resolveLaunch(launched.reqId, { roomNo: '8203', stayId: 's-1024' })
// user cancels: resolveLaunch(launched.reqId, null)
</script>
```

Protocol essentials: `resolveLaunch` is idempotent and fires once; **closing the window cancels** (the framework resolves null for unanswered launches — promises never dangle); targets unaware of the protocol degrade to "wait for window close"; each launch gets a unique reqId (repeated launches with identical args each open their own window — no `open()` dedup); detail-window-style apps with `hidden: true` are typical targets (absent from Launchpad/Dock, launched only).

### ③ Cross-app data sync — the backend is the authority, the bus does realtime refresh

```ts
// Room management: broadcast after an amount changes (the app:* prefix auto-subscribes
// remotely — multi-tab / multi-window sync)
bus.emit('app:hotel.finance:changed', { date: '2026-10-02', income: 4820 })

// The finance app: subscribe and refresh its own stats view (bus.on returns an
// unsubscribe function; call it on component unmount)
const off = bus.on('app:hotel.finance:changed', (p) => refresh(p.date))
```

Host apps in the same bundle may share Pinia stores directly (pick either per your coupling taste); between independently deployed / declarative apps it's always "backend REST + bus push".

## Event bus cheat sheet

```ts
import { bus } from '@/bus/bus'   // or window.OpenOS.bus for third parties
bus.on('app:calc', (payload, from, topic) => {})   // subscribe (* wildcards supported)
bus.emit('app:calc', { a: 1 })                     // local + remote publish
bus.emit('app:calc', { a: 1 }, { remote: false })  // local only
bus.remote('term:admin', 'pwd')                    // send to server handlers only
```

Subscribing to any business topic automatically sends a remote subscription to the server (subscribe-and-receive); when the last local consumer goes away it unsubscribes automatically. Reconnects are automatic (exponential backoff, 20 tries max); before reconnecting the session is probed, and a revoked token drops straight back to the login page — the bus never silently plays dead on a dead session.

Common built-in topics: `sys:notify` (notifications) · `sys:presence` (online status, with the `GET /api/presence` snapshot) · `sys:apps-changed` (app list changes) · `fs:changed` (file changes) · `chat:room` (chat rooms) · `sys:key` (unconsumed key presses, local only).

## Background app notifications

Store bundle apps have no code present once uninstalled, so "a message arrived while the app was closed" is backstopped by a resident desktop-shell rule engine (topic / appId / active checks / title-body formatting). **Don't build resident listeners inside bundle app components** — they go deaf on uninstall; onboarding a new app is one rule in the desktop shell.

## System attachment service (platform-level file sharing)

The upload channel shared by chat attachments and bundle apps:

- `POST /api/attachments` (multipart field `file`, ≤10MB per file, login required) → `{id, name, mime, size}`; the id is a 128-bit random, unenumerable value; mime is decided by content sniffing (declarations can be forged).
- `GET /api/attachments/{id}?token=<jwt>` download (tags can't carry an Authorization header, so auth goes via the query parameter like `/ws`; `api.upload()` carries the session automatically).
- Security boundary: only images are inline-whitelisted for Content-Type; everything else downloads as `octet-stream` + `nosniff` — the attachment service can't become an HTML injection vector. Retention: `OPENOS_ATTACH_RETENTION_DAYS` (30 days by default).
