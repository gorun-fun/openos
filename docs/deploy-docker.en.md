# Docker Deployment

[简体中文](deploy-docker.md) | **English**

OpenOS ships as a multi-stage Docker image (node → go → alpine); the runtime image runs as non-root with all data under the `/data` volume. This guide covers the whole journey from a clean build to upgrades and backups.

## Prerequisites

- Docker 20.10+ (with BuildKit — the Dockerfile uses cache mounts; older versions need `DOCKER_BUILDKIT=1`) and docker compose v2
- The build machine must be able to reach:
  - `git.gorun.fun` (clone goder sources)
  - `download.gorun.fun` (download the gh executable)
  - `registry.npmmirror.com` / `goproxy.cn` / `mirrors.aliyun.com` (China mirrors pinned inside the image; they work from outside China too, just a bit slower)

## Step 1: prepare the build context (critical)

The image build does **not** clone goder / download gh inside the Dockerfile (RUN-layer caching would pin stale snapshots). Instead, the builder fetches them into `.run/` first and the Dockerfile consumes them via `COPY`. **Skipping this step and building directly fails with `COPY failed`**:

```bash
mkdir -p .run

# 1) goder sources (AI IDE components)
git clone --depth 1 https://git.gorun.fun/share/goder.git .run/goder-src

# 2) the gh executable (goder runtime dependency)
VER=$(sh .run/goder-src/scripts/version.sh gh)   # resolve the latest release
PLAT=linux                                        # switch to linux-arm64 on arm64 machines
curl -fsSL -o .run/gh "https://download.gorun.fun/grf-harness/${PLAT}/${VER}/gh"
chmod +x .run/gh && .run/gh --version
```

> Can it run without the built-in AI? Yes — but then the goder app only shows a deployment guide. `.run/` is a build-time dependency; all runtime components end up inside the image, so deployment machines need nothing extra.

## Step 2: start

```bash
# Local / single machine (data persisted to host ./data; macOS can't write /app by
# default, so make sure to set OPENOS_DATA_DIR)
OPENOS_DATA_DIR=./data docker compose up -d --build

# Server (defaults to the host's /app/docker/openos)
docker compose up -d --build

# → http://localhost:8091   (host 8091 → container 8081)
```

First start seeds the admin `admin/admin123` — **log in immediately and change the password under "System Settings → Accounts"** (or set `OPENOS_SEED_DEMO=0` to disable demo accounts, see below).

## compose environment variables

`docker-compose.yml` presets the following (edit as needed, then `docker compose up -d`):

| Variable | compose default | Notes |
|---|---|---|
| `OPENOS_DATA_DIR` | `/app/docker/openos` | host data directory (bind mount → container `/data`) |
| `OPENOS_AI_BASE_URL` / `_API_KEY` / `_MODEL` | empty | AI bootstrap on first start; configuring it in "System Settings → AI Service" after startup is recommended instead |
| `OPENOS_JWT_SECRET` | empty (auto-generated and persisted) | must be pinned explicitly to keep sessions across instances / container rebuilds |
| `OPENOS_REAL_SHELL` | `on` | **demo configuration**: enables the real terminal. Set `off` for public deployments (see [security notes](security.en.md)) |
| `OPENOS_EXCHANGE_SECRET` | empty | host-integration secret (leave empty = disabled if you don't use host integration) |
| `OPENOS_ROOM_URL` | empty | game room service URL (leave empty if you don't use the games) |

Pinned inside the image: `OPENOS_ADDR=0.0.0.0:8081`, `OPENOS_DATA=/data`, `OPENOS_DIST=/app/web/dist`, goder component paths — usually no need to override.

## What's inside the image

- The OpenOS server (single binary) + frontend static assets
- **The full goder stack**: goder-serve gateway, goder.js embed library, gh executable — the AI IDE works out of the box
- Runtime dependencies: ca-certificates, tzdata, git, bash (gh tool contract)
- Runs as the `openos` user (uid 1000); the entrypoint fixes `/data` volume ownership as root first, then drops privileges

## Upgrade

```bash
git pull
# Re-prepare the build context (goder/gh may have been updated)
rm -rf .run && mkdir .run
git clone --depth 1 https://git.gorun.fun/share/goder.git .run/goder-src
VER=$(sh .run/goder-src/scripts/version.sh gh) && \
  curl -fsSL -o .run/gh "https://download.gorun.fun/grf-harness/linux/${VER}/gh" && chmod +x .run/gh

OPENOS_DATA_DIR=./data docker compose up -d --build
```

All data lives in the volume (SQLite + user homes + attachments + goder sessions); container rebuilds lose nothing.

## Backup

The data volume is just a host directory (e.g. `./data` or `/app/docker/openos`):

```bash
# Option 1: stop-and-copy (simplest, consistent data)
docker compose stop && tar -czf openos-backup-$(date +%F).tar.gz ./data && docker compose start

# Option 2: online backup (SQLite via .backup for consistency, rsync for the rest)
sqlite3 ./data/openos.db ".backup /tmp/openos-backup.db"
rsync -a ./data/ backup-host:/backup/openos/
```

## HTTPS reverse proxy

The WebSocket event bus needs Upgrade headers; Nginx example:

```nginx
server {
    listen 443 ssl http2;
    server_name os.example.com;
    ssl_certificate     /etc/nginx/cert.pem;
    ssl_certificate_key /etc/nginx/cert.key;

    location / {
        proxy_pass http://127.0.0.1:8091;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;      # required for WebSocket
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_read_timeout 3600s;                    # long-lived connections
        client_max_body_size 32m;                    # file uploads (8MB per-file cap, with headroom)
    }
}
```

Behind a proxy, consider setting `OPENOS_TRUST_PROXY=1` (brute-force counters bucket by the last `X-Forwarded-For` hop; single-layer proxy semantics).

## Health checks and troubleshooting

- `GET /api/health`: public health endpoint (includes the version) — ready for load balancers / liveness probes
- `docker compose logs -f openos`: service logs (prints the listen address, AI config status, goder status at startup)
- Frontend won't load (404/blank): usually a broken web-stage build artifact; rebuild everything with `docker compose build --no-cache && docker compose up -d`
- goder app shows the deployment guide: shouldn't happen with a healthy image; usually missing `.run/` components at build time or the wrong architecture (an arm64 machine fetching the `linux` gh)
