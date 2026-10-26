# Self-deployment (Bare Metal / Binary)

[简体中文](deploy-bare-metal.md) | **English**

Run OpenOS from source or directly on a server, without Docker. For custom environments, intranet deployments, or when you want fine-grained control.

## Prerequisites

| Dependency | Version | Purpose |
|---|---|---|
| Go | ≥ 1.26 | build the server |
| Node.js + npm | ≥ 20 (22 recommended) | build the frontend |
| git | — | fetch sources; needed by goder's auto-fix feature |
| gh executable | — | **optional**, only for the goder AI IDE (discovered on PATH, or pointed to via `OPENOS_GH_BIN`) |

> The SQLite driver is pure Go (modernc) — **no CGO dependency**, compiles and runs on any platform with `CGO_ENABLED=0`.

## Build

```bash
git clone https://github.com/gorun-fun/openos.git && cd openos

# Production build: frontend dist + single server binary
make build        # → server/openos-server + web/dist

# (optional) build the goder AI IDE components
make goder        # → server/data/bin/goder-serve + server/data/goder/goder.js
                  #   sources cloned automatically from git.gorun.fun/share/goder.git; needs go/npm/git
                  #   runtime additionally needs gh on PATH (GODER_REPO/GODER_SRC override the source location)

# Cross-platform release archives (darwin/linux × amd64/arm64)
make release      # → release/openos-<os>-<arch>.tar.gz
```

> The `make release` tarball contains only the server binary; the frontend build lives in `web/dist` — ship it together (see OPENOS_DIST below).

## Run

**The frontend is not embedded**: the server serves static assets from `OPENOS_DIST` (default `web/dist`, resolved relative to the process working directory). Pick one of three ways to start:

### Option 1: run.sh (recommended, local / single machine)

```bash
./run.sh start server    # build (if needed) → start → wait for readiness; logs in server/openos-server.log
./run.sh stop server     # stop
./run.sh restart server  # restart (with build)
```

run.sh injects an absolute `OPENOS_DIST` automatically; data lands under `server/data/`.

### Option 2: manual

```bash
cd server
OPENOS_DIST=$PWD/../web/dist ./openos-server
# → http://127.0.0.1:8081
```

### Option 3: systemd (production)

```ini
# /etc/systemd/system/openos.service
[Unit]
Description=OpenOS server
After=network.target

[Service]
Type=simple
User=openos
WorkingDirectory=/opt/openos/server
Environment=OPENOS_ADDR=127.0.0.1:8081
Environment=OPENOS_DATA=/opt/openos/server/data
Environment=OPENOS_DIST=/opt/openos/web/dist
# Environment=OPENOS_JWT_SECRET=change-me-multi-instance-required
# Environment=OPENOS_GODER_BIN=/opt/openos/server/data/bin/goder-serve
# Environment=OPENOS_GODER_JS=/opt/openos/server/data/goder/goder.js
ExecStart=/opt/openos/server/openos-server
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable --now openos
```

Suggested deployment layout (gather the repo build outputs under `/opt/openos`): the `server/openos-server` binary, the `web/dist` frontend, and the `server/data` data directory (created automatically on first run).

## Data directory

`OPENOS_DATA` (default `data`, relative to the working directory; with run.sh that's `server/data/`):

```
data/
├── openos.db          # SQLite (users/apps/roles/audit/settings; WAL mode)
├── homes/<user>/      # per-user on-disk home (shared by files/terminal/goder)
├── bin/goder-serve    # goder gateway (make goder output)
├── goder/goder.js     # goder embed library
├── attachments/       # system attachments
├── steward/           # Code Steward state (when enabled)
└── market/bundles/    # store component apps
```

**Deleting this directory = losing all data**; back it up the same way as in the [Docker deployment guide](deploy-docker.en.md#backup).

## Upgrade

```bash
git pull
make build && make goder     # when components have updates
./run.sh restart server      # or systemctl restart openos
```

The data directory is untouched by builds and preserved across versions (the SQLite schema migrates automatically at startup).

## HTTPS

Bare-metal deployments should also sit behind an Nginx/Caddy reverse proxy (same config as the [Nginx example in the Docker guide](deploy-docker.en.md#https-reverse-proxy), with `proxy_pass` pointing at `127.0.0.1:8081`); or one line of Caddy:

```
os.example.com {
    reverse_proxy 127.0.0.1:8081
}
```

## Common startup configuration

```bash
OPENOS_ADDR=0.0.0.0:8081 \        # listen externally
OPENOS_JWT_SECRET=<random-long-string> \  # pin the key (keeps logins across instances / process rebuilds)
OPENOS_SEED_DEMO=0 \              # no demo accounts
OPENOS_REAL_SHELL=on \            # enable the real terminal (keep the default off on public deployments)
OPENOS_AI_BASE_URL=... OPENOS_AI_API_KEY=... OPENOS_AI_MODEL=... \
./openos-server
```

The full variable list is in the [configuration reference](configuration.en.md).
