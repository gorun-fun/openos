# Unified Proxy (Web Apps Deep Dive)

[简体中文](webproxy.md) | **English**

"Web apps" wrap existing websites as desktop apps with zero code (Path 4; admins configure them under "System Settings → Third-party Apps → Add web app"). Two loading paths:

- **Direct iframe** (`kind=web`): the default when the target site sets no anti-framing headers. Cross-origin isolation comes free, and neither side's scripts can reach the other; cross-site cookies are subject to the browser's third-party-cookie policy (on strict environments like Safari, fall back to the toolbar's "open in browser" when logins misbehave).
- **Unified proxy** (`kind=proxy`): the target site loads through the OpenOS server-side reverse proxy, solving both **iframe anti-framing** (`X-Frame-Options` / CSP `frame-ancestors`) and **cross-origin** problems.

The unified proxy is one engine with two entrances (`server/internal/webproxy/`): **cleaning-rule changes go to engine.go only — never fork the two entrances**.

| Entrance | Shape | Purpose |
|---|---|---|
| `/app/<id>/<scheme>/<host>/…` | app reverse proxy (HTML/CSS URL rewriting + hook injection) | whole-page embedding of web apps |
| `/px/<scheme>/<host>/…` | generic forwarder (pure passthrough, no rewriting or injection) | the unified CORS channel for network-request apps |

## URL mapping (compact form)

The target `https://h:8443/a/b?q` maps to `/app/<id>/https/h:8443/a/b?q` — **the scheme segment carries no `://`**: Go ServeMux's cleanPath folds `//` inside paths into a 307 sanitizing redirect that breaks the address (`compactTarget`/`decodeTarget` are the single codec implementation; `/px/` reuses it).

## Three layers of URL coverage

Every request a proxied page makes keeps hitting the proxy:

1. **Server-side rewriting** (`rewrite.go`): HTML `href/src/action/srcset/style`, `<style>` blocks and CSS `url()`/`@import`, and `meta refresh` are all rewritten to proxy paths — including root-absolute paths and cross-domain CDN URLs (a `<base>` injection can't cover root-absolute paths, which is why rewriting is required);
2. **Injected hook script** (`hook.js`, right after `<head>`, before page scripts): wraps `fetch`/`XMLHttpRequest`/`WebSocket` (pumped server-side via `/app/<id>/ws/…`)/`EventSource`/`Worker`/`window.open`/`sendBeacon`/`history.pushState`/`document.cookie` (rewrites `path=/` so cookies aren't planted on the whole OpenOS origin)/URL-reflecting properties of images etc. and `setAttribute`; also blocks the target site from registering Service Workers on this origin;
3. **Relative URLs resolve naturally**: the page lives under `/app/<id>/<target-URL>`, so `fetch('api/x')` lands inside the proxy prefix without any interception.

## Sessions and redirects

- Target-site `Set-Cookie` is scoped to the proxy path (Domain stripped, `Path` mapped to `/app/<id>/…`; http deployments strip `Secure` and downgrade `SameSite=None` to `Lax`) — the login session stays a closed loop under the proxy path;
- 30x `Location` headers are rewritten too; gzip responses are decompressed and rewritten; SSE / large files / video stream through; WebSockets are pumped bidirectionally.

## Forwarding semantics (headers)

- **Referer is restored to the target site's own absolute URL** (sensitive APIs often check Referer allowlists; the restored value leaks nothing about the OpenOS host), falling back to the target's root address when unrecognizable;
- The browser's `Sec-Fetch-*` group is stripped (origin relationships go stale behind a proxy); `Origin` isn't unconditionally rebuilt (native GETs don't carry one) — strict sites are handled per-host by the origin-strict adapter;
- Proxy responses **strip `X-Content-Type-Options: nosniff`**: target-site JSONP/script endpoints are frequently MIME-sloppy (NetEase's Yidun serves application/json, Baidu's hm.js text/plain) — a hard nosniff block breaks login / risk-control initialization wholesale. This is deliberate;
- **Cache semantics of rewritten responses**: the rewritten body no longer matches upstream validators — `ETag`/`Last-Modified` stripped + `Cache-Control: no-cache`; requests hit by RewriteJS additionally strip `If-None-Match`/`If-Modified-Since` (otherwise a 304 negotiation pins the browser to the pre-rewrite cached body forever).

## Custom request headers (`config.headers`, proxy mode only)

Admins can configure a fixed set of headers for the app, sent with every proxied request (incl. WS handshakes and favicon fetches) — typically `Authorization`/`X-Api-Key` auth for intranet systems.

- Configured headers apply **after OpenOS session stripping**, with top priority (they can override the default `X-Forwarded-*`);
- The `Cookie` header merges: configured value first, preserved target-site session cookies after (the login state isn't clobbered);
- Hop-by-hop / transport-control headers (`Connection`/`Transfer-Encoding`/`Content-Length`/`Host`) are silently dropped via blocklist; invalid names or CR/LF-containing values are dropped (header-injection defense);
- `env:<NAME>` values expand environment variables.

## In-window tabs (proxy mode only)

Popups are intercepted and open as tabs inside the window: `window.open(...)` returns a real window proxy (`closed`/`close()`/`postMessage` work; named windows reuse the same tab); `target="_blank"` links are captured (⌘/Ctrl-modified clicks pass through to native windows); in-page `window.close()` closes the tab, and closing the last tab closes the window. The tab bar appears with multiple tabs; switching doesn't reload.

## SPA routing and login flows

- **SPA route restoration**: SPAs routing on `location.pathname` (react-router etc.) all mismatch under the proxy prefix — the hook `history.replaceState`s the document address back to target-site form at the earliest possible moment (network requests still map back to proxy paths); the restored address persistently carries the `__os_px=<id>~<scheme>~<host>` query mark;
- **Escape recapture (CatchEscape)**: direct assignments like `location.href='/x'` can't be wrapped by the hook and escape the proxy prefix onto OpenOS's own paths (typically an SSO jump `location.href='/api/auth/sso/login'` colliding with an OpenOS route). The recapture middleware wraps the **entire route tree**: requests carrying a proxied-page Referer (proxy responses uniformly send `Referrer-Policy: same-origin`) get a 307 back onto that app's proxy path (method and body preserved — form-POST escapes work too), and the `__os_px` mark is honored too;
- **OAuth redirect-parameter two-way rewrite**: the hook rewrites `redirect_uri`/`redirectUri` in the query to proxy paths (the bounce lands back on this origin; the callback and session complete via the proxy), and the server restores the registered original absolute URL when forwarding upstream (IdP allowlist checks are unaffected);
- **The top proxy bridge** (`web/src/apps/web/pxTop.ts`): sites like NetEase Cloud Music hoist scripts/requests to the top window (natively top = the site itself); under the proxy, top = the desktop page, which would bypass the proxy and connect directly — the bridge remaps absolute URLs dynamically constructed at the top level back onto `/app/<id>/…` by the registered proxy apps' target-domain suffixes;
- A few sites that validate their own domain (strict OAuth bounces, banking) may still not work — fall back to the toolbar's "open in browser" or the [desktop client](desktop-client.en.md).

## Site adapter layer (`adapter.go`)

Site-specific handling is decoupled from the unified pipeline, matched by target host (bare-domain suffix matching) and layered on top of four hooks: `OnRequest` / `OnResponse` / `Hook` (append injected JS) / `RewriteJS` (targeted JS-body rewrites). Built in: origin-strict (Meituan-family / Kingsoft Docs / WeChat / NetEase write APIs strictly checking Origin — backstop origin injection), weixin-double-ct (duplicate Content-Type convergence), music163-top-bridge (the NetEase top bridge and QA-environment rewrites). **New site special-casing goes in this file, not the unified pipeline.**

## The toolbar's "clear cache" (proxy mode only)

Clears all of the app's data on this origin, then rebuilds the window: storage keys prefixed `__px_<appid>::`, the server-side cookie ledger expired in batches (`POST /app/<id>/__os_px_clear__` — deep-path cookies can only be cleared via response headers; re-planting re-registers them), page-JS-written cookies, root-path cookies, SW registrations swept defensively, and hook-registered IndexedDBs deleted one by one.

## App-level injected scripts (`config.hook`)

JS source ≤256KB, injected **after** the common hook.js and adapter snippets (business scripts may rely on the already-wrapped fetch/XHR entry points for API detection); the `__PREFIX__` placeholder inside the script is likewise replaced with `/app/<id>/`. Purpose: host-platform takeover when business actions happen inside the target page — the script listens to APIs/DOM, notifies the host page via `postMessage`, and the host launches its own business app via `OpenOS.launch`. What to detect and what to do on a hit live in the host repository (updating config via `/api/host/apps` is hot-effective).

## Security boundary (read this)

- **Configured targets only**: proxy targets come solely from the admin-registered `config.url`; no URL is accepted from requesters; configured targets may be intranet addresses (an explicit admin choice).
- **SSRF defense**: hosts other than the target (cross-origin subresources, redirects, OAuth flows) are validated **at the dial layer** — domains resolve once and dial by IP (kills DNS-rebinding TOCTOU); loopback/intranet/link-local/CGNAT/reserved ranges are all rejected; the `/px/` generic forwarder has no configured allowances — **public targets only**.
- **Session isolation**: the OpenOS session is stripped before forwarding (`openos_token`, `openos_*` cookies, `Authorization`); access is validated per request against the role-app authorization matrix (same visibility as the Launchpad) — 401 when not logged in, 403 when unauthorized.
- **Trust model**: proxied pages execute **same-origin** with the desktop (the same trust level as the embedded goder); target-site JS could in theory access same-origin storage and call OpenOS APIs as the current user — **configure trusted apps only** (internal systems, your own sites); use direct iframes for untrusted public sites. Same-origin storage is shared per origin: two proxy apps using the same localStorage keys may clobber each other.

## Consumers of the generic forwarder (`/px/`)

- **Network-request apps (http-client)**: send through the proxy by default (a "proxy" toggle in the top bar switches back to direct; intranet/loopback targets go direct automatically; probing `GET /api/proxy/status` and failing falls back automatically);
- **Declarative apps (SchemaApp)**: cross-origin `dataSource.kind=rest` defaults to `OpenOS.proxyUrl()` via `/px/`, falling back to a direct browser fetch on failure (`direct: true` forces direct).

## Switch and status

`OPENOS_PROXY=off` removes the `/app/*` and `/px/*` routes (app configs are kept; iframes will get 404); `GET /api/proxy/status` reports unified-proxy availability (the settings "Web apps" section and network-request apps degrade accordingly).
