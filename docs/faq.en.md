# FAQ

[简体中文](faq.md) | **English**

## Login and accounts

**What are the default accounts?**
`admin/admin123` (admin), `user/user123` (regular), `guest/guest123` (guest). `OPENOS_SEED_DEMO=0` disables seeding. Change the passwords immediately after deploying to any reachable environment (enforced policy: ≥8 chars with letters and digits).

**Forgot the admin password?**
There's no mail dependency, hence no recovery channel. In test environments just delete `data/openos.db` and start over (all data lost). Production recovery: on another instance create a user with the same name and a known password, then copy that `users`-table password hash over the admin row in the target DB (pbkdf2 hashes are portable across instances) — or keep a second admin account ready in advance.

**Constantly told "session expired" after login?**
A multi-instance deployment without a pinned `OPENOS_JWT_SECRET` — different keys per instance mean each rejects the other's tokens. Set one explicit key and restart.

## Deployment

**Docker build fails with `COPY failed: ... .run/goder-src`?**
The image build needs the `.run/` context prepared first (goder sources + the gh binary) — see [Docker deployment · Step 1](deploy-docker.en.md).

**Blank page / 404 on localhost:8081?**
On bare metal the frontend isn't embedded: make sure `OPENOS_DIST` points at `web/dist` (run.sh injects it; for manual runs see [self-deployment](deploy-bare-metal.en.md)). In dev mode, visit the frontend port 5173, not 8081.

**WebSocket won't connect (notifications / online status dead)?**
The reverse proxy lacks the Upgrade headers — Nginx needs `proxy_set_header Upgrade $http_upgrade; proxy_set_header Connection "upgrade";`, see [Docker deployment · HTTPS reverse proxy](deploy-docker.en.md#https-reverse-proxy).

**Dev-mode frontend can't reach the backend?**
The Vite proxy points at `127.0.0.1:8081` — make sure the backend is up (`make dev` starts both). The desktop client connects to dev mode with `-url http://localhost:5173` (vite listens on IPv6 localhost only).

## Features

**The terminal says "not enabled"?**
The real terminal is off by default (`OPENOS_REAL_SHELL=off`, for safety). Controlled environments may set `on`; note that `on`'s "restricted" is only cd tracking, not a filesystem sandbox — see [security notes](security.en.md).

**The goder app shows a deployment guide?**
goder components are missing: the Docker image ships them; bare metal needs `make goder` + gh on PATH — see the [goder guide](goder.en.md).

**The AI doesn't reply / replies oddly?**
An admin configures an OpenAI-compatible API under "System Settings → AI Service" (with one-click "test connection"); unconfigured means local demo mode. The provider must support the model name you configured.

**An added web app opens blank?**
The target site sets `X-Frame-Options` / `frame-ancestors` anti-framing headers — switch to the "app proxy" form (check app proxy), or use the [desktop client](desktop-client.en.md).

**Sites like NetEase Cloud Music won't log in / requests misbehave?**
These sites have complex login systems (top-window execution, strict risk control); the unified proxy ships adapters and the top bridge. If still broken: ① clear the login state with the app toolbar's "clear cache" and retry; ② finish logging in via the toolbar's "open in browser", then come back and refresh; ③ the desktop client form is the most reliable (same-origin policy disabled).

**Share links won't open in SSO mode?**
Known limitation: the full-page redirects of SSO mode can drop the `#share=` fragment; local-account logins are unaffected.

**Shortcuts don't work?**
Browsers can't capture OS-level combos (macOS ⌘Tab / ⌘Space are taken by the OS): the switcher is most reliable on Alt+Tab, Spotlight search on Alt+Space. Global shortcuts inside iframe apps are forwarded by injected scripts (except cross-origin direct iframes).

## Data

**Where are my files stored?**
In real on-disk directories at `data/homes/<username>/` on the server (shared by files / terminal / goder). Back up the whole `OPENOS_DATA` directory (see the [deployment docs](deploy-docker.en.md#backup)).

**Do notes and the Dock layout survive switching browsers / devices?**
Yes — user-level data is always authoritative in server-side `user_settings` with local caching, consistent across devices.

**How do I wipe and reinstall?**
Stop the service → delete the `data/` directory → restart (demo data is re-seeded). The desktop client additionally has `openos-desktop reset` for client-side data.
