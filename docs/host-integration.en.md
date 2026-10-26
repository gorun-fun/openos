# Host Integration Guide

[简体中文](host-integration.md) | **English**

Business systems can consume OpenOS as a desktop base framework **without forking the shell code**: the host frontend assembles the desktop with `createOpenOS()`, injecting business apps, branding, and SSO; the servers interoperate over a shared secret for the user system. This is "Path 3", aimed at full business systems (reference implementations: the grf-hotel property-management system, grf-os).

The companion standalone Go module `hostsdk/` wraps all machine-to-machine calls (subprocess hosting / SSO / BootstrapPage / user-role-app declare / AI sync) — host backends can import it instead of hand-writing HTTP.

## Frontend shell assembly

```ts
// Host app entry (vite alias: @openos → the web/src of an openos snapshot)
import { createOpenOS } from '@openos/framework'

createOpenOS({
  // business apps (lazily loaded components)
  apps: { rooms: () => import('./apps/Rooms.vue') },

  // optional injections
  icons: { rooms: { svg: '<svg …>' } } as const,     // business icons
  brand: { name: 'Business System', logo: '🏨' },    // branding (login page / menu bar)
  login: { mode: 'sso', url: '/api/auth/sso/login' }, // login goes through host SSO; 401 bounces back
  theme: { accent: '#e5484d' },                       // global accent color (derived shades computed)
  wallpapers: [...],                                  // wallpaper list injection
  dockOrder: ['rooms', 'welcome'],                    // initial Dock order (until the user customizes)
  menubarExtras: MyMenuBarExtras,                     // menu-bar right-side extensions (arrays for multiple slots)
  appMenus: {...},                                    // app menu injection
  widgets: {...},                                     // custom desktop widgets (auto-appear in the context menu)
  i18n: { locale: 'zh-CN', messages: {...} },         // message injection / pin the initial locale
  dialogs: {...},                                     // cross-app dialog registration
  capabilities: {...},                                // business capabilities exposed to the AI (see the goder guide)
  remoteApps: { market, hostVersion },                // independent app release channel (below)
})
```

`createOpenOS` is also the public API surface: hosts can import `bus` / `api` / `fsApi` / all Os* components / Pinia stores directly, without reaching into internal paths.

Apps can be marked `hidden: true`: openable programmatically but absent from Launchpad/Dock (ideal for detail windows).

## Server interop

### Token exchange (host SSO → OpenOS identity)

After the host authenticates a user, the identity is swapped server-to-server for an OpenOS one; first-seen users are provisioned automatically (random password; login only via the host):

```bash
# OpenOS side (open only within the same machine / trusted network as the host):
OPENOS_EXCHANGE_SECRET=<shared-secret> ./openos-server

# Host side (hand the token to the frontend via the URL fragment #os-token=<jwt> after the bounce):
curl -X POST http://127.0.0.1:8081/api/auth/exchange \
  -H 'X-Exchange-Secret: <shared-secret>' -H 'Content-Type: application/json' \
  -d '{"username":"zhang","role":"front","displayName":"Zhang, Front Desk","perms":["room.view"]}'
# → { "token": "<openos JWT>", "user": {...} }
```

- The secret is compared in constant time; the `OPENOS_EXCHANGE_ROLES` whitelist (e.g. `front,boss,manager`) is strongly recommended — a leaked key still can't mint admin (startup logs warn when the key is set but the whitelist is empty).
- `perms[]` optional: the user's permission union computed by the host's authoritative store, written to users.perms (changes apply immediately, no revocation); omitted → derived from the role-permission matrix.

### Token self-healing

`GET /api/auth/refresh` returns a new token and re-seeds the cookie. A host's 401 recovery can call it directly to renew (e.g. the openos token is still valid but the host session has expired) instead of bouncing back through SSO for a fresh exchange. In standalone deployments the frontend already renews every 12h + on returning to the foreground.

### Reverse interop (business backend validating OpenOS JWTs)

`POST /api/auth/introspect` (RFC 7662 style, guarded by the same secret; 503 when unconfigured):

```go
// openosToken = the OpenOS token submitted by frontend business apps with their
// API calls (apps inside the shell get it via OpenOS.authToken(),
// Authorization: Bearer <openos-jwt>)
func introspect(openosToken string) (id struct {
    Active      bool   `json:"active"`
    Username    string `json:"username"`
    Role        string `json:"role"`
    DisplayName string `json:"displayName"`
    Exp         int64  `json:"exp"`
}, err error) {
    body, _ := json.Marshal(map[string]string{"token": openosToken})
    req, _ := http.NewRequest("POST", "http://127.0.0.1:8081/api/auth/introspect", bytes.NewReader(body))
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("X-Exchange-Secret", os.Getenv("OPENOS_EXCHANGE_SECRET"))
    resp, err := http.DefaultClient.Do(req)
    if err != nil { return id, err }
    defer resp.Body.Close()
    if err = json.NewDecoder(resp.Body).Decode(&id); err != nil { return id, err }
    if !id.Active { return id, errors.New("openos session invalid") }
    return id, nil
}
```

- Always 200: valid `{"active":true,"sub","username","role","displayName","exp","iat"}`, invalid `{"active":false}`; validation shares the source with REST/WS (revocations apply instantly).
- A per-request hop has a cost — backends should cache verdicts (suggested TTL = `min(exp-now, 60s)`).
- High-trust same-machine deployments can verify HS256 locally (claims `sub/role/tv/exp/iat`, both sides sharing `OPENOS_JWT_SECRET`) — but holding the signing key means being able to forge any token; across trust boundaries, always introspect.

## Machine-to-machine API group (`/api/host/*`)

A server-to-server endpoint group guarded by the same `X-Exchange-Secret` as declare. Hosts **don't need** to mint an admin service account — just call with the key:

| Endpoint | Notes |
|---|---|
| `GET /api/host/users` | full-field user list (incl. perms snapshot / token version / disabled state) |
| `POST /api/host/users` | explicit user creation (empty password → random value; login only via exchange) |
| `POST /api/host/users/sync` | bulk idempotent sync (same semantics as "user sync" below) |
| `PUT /api/host/users/{name}` | field-level update (role/displayName/disabled/perms/password) |
| `DELETE /api/host/users/{name}` | delete with cascade cleanup |
| `GET /api/host/roles` | role catalog + permission-point catalog (reconciliation / display) |
| `POST /api/host/apps` / `DELETE /api/host/apps/{id}` | app registration (always trusted, locked) / forced deletion |
| `GET /api/host/apps` | full app list (reconciliation / retiring old apps) |
| `GET`/`PUT /api/host/apps/access`, `PUT /api/host/apps/role-labels` | role-app authorization and display names |
| `GET`/`PUT /api/host/ai/user-providers` | per-user AI providers |
| `POST /api/host/goder/tasks*` | goder headless tasks (run AI sessions in the background as a given user; see the goder guide) |

Audited with operator=`host`. hostsdk's `Manager` (SyncUsers / CreateUser / RoleAppMap / UpsertApp…) wraps this whole group.

**Bulk user sync**: `POST /api/users/sync` (users.manage; hosts use the /api/host variant) idempotently pushes a user list — unknown users get provisioned (random password), known users update by field (role / display name / disabled state / perms snapshot — immediate effect, no revocation); demoting or disabling the last admin is skipped and reported in the response `skipped`. Combined with exchange's lazy provisioning, hosts choose "provision on login" or "full sync at startup".

**This is a fully-trusted channel**: a holder of `OPENOS_EXCHANGE_SECRET` can manage the entire user system (including assigning the admin role) — the key equals admin credentials; keep it between the host backend ↔ OpenOS on the same machine / trusted network.

## Role catalog and permission points

A permission-point model layered on top of app-level authorization (role_apps), with two coexisting modes:

- **system mode (standalone deployment)**: the `roles`/`role_perms`/`perm_defs` tables are authoritative; admins edit them under "System Settings → Roles & Permissions".
- **host mode (integrated)**: the **authoritative store of roles and permissions stays in the host's own database**; the host projects it declaratively via `POST /api/host/declare`:

  ```json
  {
    "roles": [
      { "key": "boss", "label": "Owner", "perms": ["order.view"], "apps": ["welcome", "calendar"] }
    ],
    "perms": [
      { "code": "order.view", "group": "Business", "label": "View orders" }
    ],
    "roleWriteback": "http://127.0.0.1:8082/api/v1/openos/role-write"
  }
  ```

  Idempotent convergence: host roles outside the list are removed (**those still bound to users are kept** and reported in the response `unresolved` — the host syncs those users' roles away first, then re-declares to converge); saving broadcasts `sys:apps-changed`, and permissions take effect immediately.

- **roleWriteback**: once a writeback URL is declared, host roles unlock for editing in the settings page — `PUT /api/roles/{key}` forwards the whole package to that address (carrying X-Exchange-Secret and the X-OS-User operator); the host persists authoritatively and re-declares to converge. Without one, host roles are read-only and DELETE is always refused (the role lifecycle belongs to the host).
- **Frontend gating**: `can('room.view')` / the `v-perm` directive (admin bypass, `*` wildcard, exact match — same semantics as the server's RequirePerm).
- Roles aren't limited to the three built-ins: hosts inject business roles as safe-charset custom values; the built-in permission points `users.manage` / `apps.manage` / `market.manage` / `ai.manage` / `system.manage` actually guard their admin endpoints (admin always passes).

## Trimmed deployment (data boundary)

When the host's goal is "SQLite holds only demo data + per-user UI settings":

```bash
OPENOS_VFS=off OPENOS_GODER=off OPENOS_SEED_DEMO=0 \
OPENOS_EXCHANGE_SECRET=<secret> OPENOS_EXCHANGE_ROLES=front,boss,manager \
./openos-server
```

- The VFS/terminal and goder built-in apps are removed entirely (authorization matrix reclaimed too); `/api/fs/*` returns 503 — business files belong to the host;
- No demo accounts, no sample files; users arrive exclusively via exchange / users-sync;
- Disabling a user takes effect immediately (REST 401; online bus connections kicked within 30s; tokens aren't revoked — sessions recover on re-enable);
- Deleting a user cascades through all their data (settings / files / chat / shares / feedback / AI providers / attachments + the disk home + goder session directories).

## Independent app release (remoteApps + shelf)

A release channel decoupling host business apps from the host image: an app change builds just that app's artifact and uploads it to the shelf; users get it on page refresh — no host release, no service restart. Coexists with the app store (store = admin installs third-party apps; this channel = hot release of the host's own apps).

```ts
createOpenOS({ apps, remoteApps: { market: 'https://…/hotel/market', hostVersion: 'v0.0.84' } })
```

Each app prefers its shelf-catalog remote artifact; shelf unreachable / no entry / `minHostVersion` unsatisfied / import failure all fall back to the local `apps` bundles (chunks baked into the host dist). The shelf protocol and the three build pieces (`web/tooling/`: deps library build / host externalization + importmap injection / single-file app artifacts + catalog merge) are documented in the `web/src/remote.ts` file header and the `web/tooling/` source comments.

Hard constraints: **bare names registered in deps-manifest must never be bundled** (dual Vue/pinia instances break toasts/stores in subtle ways); host projects need an esbuild devDep and a postcss CSS pipeline declared in their vite config (Lightning CSS collapses the backdrop-filter prefix pair and kills the glass effect — proven the hard way).

## Micro-frontend and third-party site takeover (config.hook)

The config of a kind=proxy web app may declare a `hook` field (JS source ≤256KB, injected into proxied pages after the common hook.js): the host platform listens to the target site's APIs/DOM, notifies the host page via `postMessage`, then launches its own business app via `OpenOS.launch` — detection criteria and hit logic live in the host repository, hot-effective by updating config via `/api/host/apps`. See the [unified proxy guide](webproxy.en.md).
