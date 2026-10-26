# Desktop Client (openos-desktop)

[简体中文](desktop-client.md) | **English**

The other web-app integration route besides the reverse proxy (unified proxy): **the local Chrome opens OpenOS directly in app mode, and web-app iframes connect straight to real sites**. Suited to scenarios a server-side proxy can't cover (complex target-site SSO / risk control / cookie rotation, e.g. Meituan ebooking), at the cost of depending on the client environment (local Chrome required).

## Build and run

```bash
make desktop                     # build → ./openos-desktop at the repo root (needs local Go)
./openos-desktop                 # "choose server" page first; enter after picking
./openos-desktop -auto           # skip the picker, connect to the saved config (none = http://127.0.0.1:8081)
./openos-desktop -url http://localhost:5173   # dev mode straight to vite (vite listens on IPv6 localhost only)
./openos-desktop reset           # wipe all client-local data (interactive confirm; -y skips, -profile picks a directory)
```

Paired with the repo-root process manager: `./run.sh start|stop|restart|reset` (default target=desktop: start ensures the server first, then the client; stop does client first, then server).

## How it works

CDP (Chrome DevTools Protocol) drives the local Chrome with `--app=<url>` + a dedicated profile (`~/.openos-desktop`, login state persisted), `--disable-web-security` + site isolation off — cross-origin iframes are no longer blocked by the same-origin policy / X-Frame-Options / CSP frame-ancestors. Web apps (kind=proxy) detect the client via the injected `window.__OPENOS_DESKTOP__` marker and point their iframes straight at the real `config.url` instead of the `/app/<id>/` proxy path.

## Server picking and configuration

- Every launch opens the "choose server" page first (a loopback settings page in `?pick=1` form; the pick persists to `<profile>/config.json` and navigates the main window); direct-connect precedence: `-url` (this run only) > saved config > local default;
- Entrypoints: the frontend "System Settings → Server" section (client mode only; embeds the loopback settings page in an iframe) and "switch server" at the bottom of the login page; the settings service defaults to port 17700, incrementing (`OPENOS_DESKTOP_CONF_PORT` overrides); when all ports are taken it degrades to a direct connection;
- If the server URL won't open (chrome-error:// pages don't run injected scripts), the client flips to the settings page with an unreachable notice; after switching, guard / Referer-rewrite / cookie-normalization exemption domains follow dynamically (http-compatible endpoints don't get Secure forced onto cookies).

## The three-layer SameSite bridge (the crux of cross-site login)

Measured on Chrome 154: session cookies without an explicit SameSite attribute are rejected wholesale as Lax in third-party iframe contexts (`--disable-features=SameSiteByDefaultCookies` no longer works — it can't be turned off). The client fixes this in three layers:

1. **Injected shim**: `document.cookie` writes get `SameSite=None` added automatically (main frame via addScriptToEvaluateOnNewDocument; cross-origin subframes don't enter that channel — patched on the fly via Runtime.contextCreated + a periodic cross-realm walker as backstop);
2. **Response bridge**: rejected Set-Cookies (responseReceivedExtraInfo.blockedCookies) are written directly into the cookie jar via `Network.setCookie`;
3. **Jar normalization**: periodically rewrites non-None cookies to `None+Secure` (except local domains), fixing the send side of "logged in at the top-level first-party, but the third-party iframe request carries no cookie".

The web-app toolbar in desktop mode also has a **"login" button**: a top-level first-party `window.open` completes login/SSO with native semantics and zero loss; leaving the login page auto-closes the popup and refreshes the in-page tabs.

## Version and auto-update

- Release channel `download.gorun.fun/openos/cli`, six platforms (macos / macos-arm64 / linux / linux-arm64 / windows / windows-arm64) × version directories × the runnable file + `.sha256` + notes;
- Flow: silent check 2s after startup → the settings page "client version" block (`GET /api/version` status / `POST /api/update` trigger) → download `.new` → sha256 verify → two-step atomic rename (rollback on failure) → same-PID in-place restart (Windows spawns a new process);
- The version is injected at build time; `OPENOS_DESKTOP_UPDATE_URL` overrides the channel; `go run` temp builds refuse self-update; local e2e uses `desktop/e2e-update.sh` against a fake server.

## Known boundaries

- **Micro-frontend container adaptation** (Meituan ebooking types) needs several pieces working together: deep links with `.html` suffixes, sandbox removal, container-probe replies, Sec-Fetch stripping, the SameSite bridge, anchor re-anchoring (navigating back to the configured address when the target site's `parent.location` hijacks the container page), and `document.referrer` spoofing — all built into the client; when onboarding new sites, check these first on anomalies;
- Without site isolation, the desktop page and embedded apps **share a renderer process** — heavy apps drag the whole page (when a page stalls, check whether the renderer is hung first; screenshots / the clock tell you if it's alive);
- In-browser global shortcuts are forwarded back to the host via injected-script postMessage; cross-origin direct iframes that can't be injected aren't covered (a known boundary).

## Security boundary

`--disable-web-security` means cross-origin scripts inside the window can reach each other — use the dedicated profile only for OpenOS; the CDP port binds 127.0.0.1 only; inside the window you hold a real local browser session — **never point it at an untrusted OpenOS instance**.
