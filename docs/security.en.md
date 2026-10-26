# Security Notes

[简体中文](security.md) | **English**

OpenOS targets publicly deployable, production-grade standards (tightened after two security audits in Oct 2026). This doc is the full overview of the security model; code conventions for the affected areas are in [AGENTS.md](../AGENTS.md).

## Authentication and sessions

- **Passwords**: pbkdf2-SHA256, 600,000 rounds + random salt; legacy 20,000/4,096-round hashes upgrade transparently on the user's next login. Password policy: ≥8 chars with letters and digits.
- **JWT**: HS256, 24h by default (`OPENOS_TOKEN_TTL`), carries token_version; the key is auto-generated and persisted in a separate `system_kv` table (isolated from user settings data).
- **Identity / authorization separation**: token_version revocation happens **only on password change / reset**; role, permission, display-name, disable, and delete changes use **realtime checks** — authorization changes take effect immediately **without logging anyone out** (REST validates per request; established bus connections are re-checked on a 30s cycle and kicked offline). New users get token_version initialized to the current time, closing the "old token resurrects after delete-and-recreate" hole.
- **One-shot tickets where headers can't be sent**: `POST /api/auth/ws-ticket` issues a 30-second burn-after-use ticket; `/ws`, attachment downloads, and store component fetches use `?ticket=` instead of putting long-lived JWTs into URLs (`?token=` remains during a compatibility window).
- **Cookies**: sessions are uniformly HttpOnly + SameSite=Strict; Secure is added automatically on direct HTTPS or when the proxy declares https.
- **Frontend sliding renewal**: every 12h and when the page returns to the foreground, the frontend silently calls `/api/auth/refresh` for a new token; 401 triggers a global logout (REST and the bus share one notice channel).

## Login brute-force protection and rate limiting

- 8 failures per "username+IP" within 10 minutes locks that pair for 15 minutes; global 600 req/min/IP (covers `/api`, `/ws`, `/goder-api`, `/app/*`).
- The user-not-found path runs an equal-cost dummy hash, flattening username-enumeration timing differences.
- Behind proxies/NAT (compose port mappings etc.) all external users share one source IP and can get collectively locked — `OPENOS_TRUST_PROXY=1` buckets by the last `X-Forwarded-For` hop (single-layer proxy semantics, off by default).

## Event bus ACL

- **Subscribe whitelist** = public topics (`sys:*`, `fs:changed`, `app:*`, `chat:*`, `notes:*`) + own topics (`term:<self>`); `agent:*` and other users' terminal topics cannot be wiretapped.
- **Publish whitelist** = public business prefixes (`app:` / `chat:` / `notes:`) + a few exact system topics + own terminal topics; `sys:*` broadcasts are reserved to the server (no spoofed system notifications / presence).
- Client-published `sys:open` / `sys:notify` / `fs:changed` are delivered to the **same user only** (semantics: the user's own cross-device sync); `sys:key` stays local and never hits the network.

## Filesystem (VFS)

- Strictly confined to `/home/<user>` (mapped to the disk directory `<data>/homes/<user>`); REST reads/writes resolve symlinks and reject escapes (a terminal `ln -s` to outside is not a privilege-escalation channel).
- Single file ≤8MB (Write/Touch bodies hard-capped at 9MB, upload bodies at quota+8MB — oversized bodies aren't even spooled), 100MB per-user quota, 20,000-file cap; file writes/deletes/renames/uploads/shares are all audited.
- REST writes enforce the quota; terminal shell writes don't (sandbox semantics).
- Share links: a 128-bit random id is the authorization credential, login is the identity gate — both are required; downloads are always `attachment` + `nosniff`, never rendered inline.

## App proxy (webproxy)

- **The SSRF line is at the dial layer**: target hostnames resolve once, are validated, then dialed by IP (kills DNS-rebinding TOCTOU); admin-configured targets are allowed (intranet included — an explicit admin choice), everything else rejects loopback/intranet/link-local/CGNAT/reserved ranges.
- The OpenOS session is stripped before forwarding (`openos_token`, `openos_*` cookies, Authorization) — OpenOS credentials never leak outward.
- Target-site `Set-Cookie` is scoped to the `/app/<id>/` proxy path (Domain stripped), isolated under the proxy prefix.
- Access is validated per request against the role-app matrix (`role_apps`); the `/px/` generic forwarder requires login and allows public targets only.
- **Trust model**: proxied pages execute **same-origin** with the desktop — target-site JS could in theory access same-origin storage, so **configure trusted apps only**; use direct iframes (cross-origin isolation) for untrusted sites.

## Subprocesses and secret management

- Terminal and goder subprocess environments go through the `safeenv` allowlist and **never inherit `OPENOS_*` secrets**; git credentials are injected one-shot via `GIT_CONFIG_*` env vars — tokens never appear in argv/URLs/logs.
- The goder-serve gateway and the desktop-control MCP endpoint **enforce loopback binding** (non-loopback refuses to start); MCP tokens are per-user with token↔user paired validation, tools only act on the calling user, and writes are audited.
- AI keys live only on the server and are masked in API responses; the goder managed provider goes through OpenOS's own proxy endpoint (`/api/ai/v1/*`, per-user proxy keys) — **the platform key never lands on any user's disk**.

## XSS defenses

- The slides app sanitizes HTML with an allowlist (scripts / embeds / event attributes / styles stripped — no stored cross-user XSS).
- Markdown rendering allowlists link/image schemes (rejects `javascript:`).
- Attachment service: the Content-Type inline allowlist covers images only (png/jpeg/gif/webp/bmp/avif); everything else (incl. SVG/HTML) is forced to `octet-stream` + `attachment` + `nosniff` — attachments and share links can't become HTML injection vectors.

## Machine-to-machine channel (host integration)

- A holder of `OPENOS_EXCHANGE_SECRET` can manage the entire user system (including assigning the admin role) — **the key equals admin credentials**; use it only between the host backend and OpenOS on the same machine / trusted network; `OPENOS_EXCHANGE_ROLES` constrains only the user-token minting face of exchange.
- Reverse interop `POST /api/auth/introspect`: validation shares the same source as REST/WS (revocations apply instantly), always 200 + an active boolean (RFC 7662 style); callers should cache the verdict.
- All host administrative calls are audited (operator=`host`).

## Users and permissions

- Dual-mode role catalog: standalone deployments edited by admins (system) / host-declared projection (host, authoritative store in the host); permission points like `users.manage` actually guard the admin endpoints (admin always passes).
- Anti-privilege-escalation: non-admins holding `users.manage` cannot touch admin users or grant the admin role.
- Disabled users are blocked across the whole chain (login 403 / REST 401 / WS kick); the last enabled admin cannot be disabled / demoted / deleted.
- User deletion cascades: settings / files / chat / shares / feedback / AI providers / attachments (rows + disk files) + the disk home + goder session directories.

## The honest truth about the terminal

The "restricted sandbox" of `OPENOS_REAL_SHELL=on` **only constrains cd / working-directory tracking — it is not a filesystem sandbox**. Commands run with the server process's privileges and can read/write local server data (other users' homes, the SQLite DB, attachments, …). Public-facing deployments should keep it `off`, or harden it themselves (chroot / container volume isolation). Guardrail comparisons are `/`-boundary aware; commands are audited, time-limited, and output-truncated.

## Data governance

- Audit logs / AI conversations / chat history / system attachments are auto-cleaned per retention (defaults 90 / 90 / 30 days, 0 = forever), preventing unbounded growth.
- User-level data (notes / notification history / calendar / Dock / window session / …) persists server-side in `user_settings`, in a table separate from system secrets (`system_kv`) — user data can be exported / purged safely without touching system keys.
