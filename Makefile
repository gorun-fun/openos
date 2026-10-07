# 版本号：优先当前提交的精确 tag，其次最近 tag+提交数，均无则 dev（注入系统信息/健康检查）
VERSION ?= $(shell git describe --tags --exact-match 2>/dev/null || git describe --tags 2>/dev/null || echo dev)

# 桌面客户端版本：同源去 v 前缀（v0.0.60 → 0.0.60，与发布通道 openos/cli 的版本
# 目录命名一致，自动更新比较用；非 tag 构建为 <版本>-N-g<hash> 或 dev）
DESKTOP_VERSION ?= $(shell (git describe --tags --exact-match 2>/dev/null || git describe --tags 2>/dev/null || echo dev) | sed 's/^v//')

.PHONY: dev server web build release docker test goder desktop desktop-release desktop-upload clean

# 准备 goder 组件：源码自动拉取自远端仓库，构建 goder-serve 网关 + 嵌入式 goder.js。
#   GODER_REPO 源码仓库地址；GODER_SRC 本地克隆目录（可改指已有克隆，免拉取）
#   依赖：go、node/npm（goder.js 由上游 ./build.sh embed 构建）、git
GODER_REPO ?= https://git.gorun.fun/share/goder.git
GODER_SRC ?= .goder-src
goder:
	@if [ -d "$(GODER_SRC)/.git" ]; then \
		echo "→ 更新 goder 源码（$(GODER_SRC)）..."; \
		git -C "$(GODER_SRC)" pull --ff-only || echo "⚠ 拉取失败，沿用已有本地源码"; \
	else \
		echo "→ 克隆 $(GODER_REPO) → $(GODER_SRC)"; \
		git clone --depth 1 "$(GODER_REPO)" "$(GODER_SRC)"; \
	fi
	@cd "$(GODER_SRC)" && ./build.sh embed
	@cd "$(GODER_SRC)" && go build -o "$(CURDIR)/server/data/bin/goder-serve" ./cmd/goder-serve
	@mkdir -p server/data/goder
	@cp "$(GODER_SRC)/frontend/dist-embed/goder.js" server/data/goder/goder.js
	@echo "goder 组件就绪（server/data/bin/goder-serve + server/data/goder/goder.js）"

# 同时启动后端与前端（开发模式）
dev:
	@trap 'kill 0' EXIT; \
	(cd server && go run .) & \
	(cd web && npm run dev) & \
	wait

server:
	cd server && go run .

web:
	cd web && npm run dev

# Go 单元测试
test:
	cd server && go test ./...

# 生产构建：前端产物由 Go 托管
build:
	cd web && npm install && npm run build
	cd server && go build -ldflags "-X openos/server/internal/store.Version=$(VERSION)" -o openos-server .
	@echo "完成（版本 $(VERSION)）：cd server && ./openos-server  →  http://127.0.0.1:8081"

# 发布多平台二进制（darwin/linux × amd64/arm64），含前端产物
release: build
	@mkdir -p release/pkg
	@cp -r web/dist release/pkg/web-dist
	@cd server && for os_arch in "darwin amd64" "darwin arm64" "linux amd64" "linux arm64"; do \
		set -- $$os_arch; \
		echo "→ 构建 openos-$$1-$$2"; \
		mkdir -p ../release/pkg/openos-$$1-$$2; \
		CGO_ENABLED=0 GOOS=$$1 GOARCH=$$2 go build -trimpath -ldflags="-s -w -X openos/server/internal/store.Version=$(VERSION)" -o ../release/pkg/openos-$$1-$$2/openos .; \
	done
	@cd release && for d in pkg/openos-*; do \
		tar -czf "$$d.tar.gz" -C pkg "$$(basename $$d)" && echo "✓ $$d.tar.gz"; \
	done && rm -rf pkg
	@echo "发布包输出至 release/"

docker:
	docker build -t openos:latest .

# 桌面客户端：CDP 驱动 Chrome 以 app 模式打开 OpenOS（关闭同源策略与站点隔离，
# 网页应用 iframe 直连真实站点），解决网页应用的跨域问题；版本注入供自动更新比较
desktop:
	cd desktop && CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=$(DESKTOP_VERSION)" -o ../openos-desktop .

# ---- 桌面客户端发布通道（download.gorun.fun/openos/cli，goder 同款 平台/版本/运行文件）----
# desktop-release：交叉编译全部 6 平台（darwin/linux/windows × amd64/arm64，平台
# 目录沿用 goder 约定：macos=amd64、macos-arm64、linux、linux-arm64、windows、
# windows-arm64）→ release/cli/<版本>/<平台>/openos-desktop-<平台>[.exe] + 同名
# .sha256 + notes.txt（最近提交信息）。仅接受精确 tag（dev 拒绝发布）。
# desktop-upload：整目录上送下载服务器（桌面客户端自动更新消费同一通道，
# 见 desktop/update.go；CI 的 publish-cli job 在每次打 tag 时自动做同样的事）。
CLI_VERSION ?= $(DESKTOP_VERSION)
CLI_DIR = release/cli/$(CLI_VERSION)
CLI_HOST ?= root@10.8.0.5
CLI_ROOT ?= /data/download/openos/cli

desktop-release:
	@if [ "$(CLI_VERSION)" = dev ] || [ "$(CLI_VERSION)" = "" ]; then \
		echo "✗ 当前提交无精确 tag（版本 dev），发版请先打 tag（如 v0.0.60）"; exit 1; \
	fi
	@mkdir -p $(CLI_DIR)
	@for t in "darwin amd64 macos" "darwin arm64 macos-arm64" "linux amd64 linux" "linux arm64 linux-arm64" "windows amd64 windows" "windows arm64 windows-arm64"; do \
		set -- $$t; goos=$$1; goarch=$$2; plat=$$3; ext=""; \
		if [ "$$goos" = windows ]; then ext=".exe"; fi; \
		echo "→ 构建 openos-desktop-$$plat$$ext ($$goos/$$goarch)"; \
		mkdir -p $(CLI_DIR)/$$plat; \
		(cd desktop && CGO_ENABLED=0 GOOS=$$goos GOARCH=$$goarch go build -trimpath \
			-ldflags "-s -w -X main.version=$(CLI_VERSION)$$([ $$goos = windows ] && echo ' -H windowsgui')" \
			-o ../$(CLI_DIR)/$$plat/openos-desktop-$$plat$$ext .) || exit 1; \
	done
	@cd $(CLI_DIR) && for f in */openos-desktop-*; do \
		case "$$f" in *.sha256) continue ;; esac; \
		if command -v sha256sum >/dev/null 2>&1; then sha256sum "$$f" > "$$f.sha256"; \
		else shasum -a 256 "$$f" > "$$f.sha256"; fi; \
	done && git log -1 --pretty=%B > notes.txt && echo "✓ 发布产物就绪：$(CLI_DIR)/（版本 $(CLI_VERSION)）"

# 上传桌面客户端发布产物（download.gorun.fun = 10.8.0.5:/data/download；用户名
# 必须显式 root@——~/.ssh/config 将 10.8.0.5 默认用户设为 wenjun，免密会拒）
desktop-upload: desktop-release
	@for plat in macos macos-arm64 linux linux-arm64 windows windows-arm64; do \
		echo "→ 上传 $$plat/$(CLI_VERSION)"; \
		ssh $(CLI_HOST) "mkdir -p $(CLI_ROOT)/$$plat/$(CLI_VERSION)" || exit 1; \
		scp $(CLI_DIR)/$$plat/openos-desktop-$$plat* $(CLI_HOST):$(CLI_ROOT)/$$plat/$(CLI_VERSION)/ || exit 1; \
		scp $(CLI_DIR)/notes.txt $(CLI_HOST):$(CLI_ROOT)/$$plat/$(CLI_VERSION)/notes.txt || exit 1; \
	done
	@echo "✓ 已上传：https://download.gorun.fun/openos/cli/<平台>/$(CLI_VERSION)/"

clean:
	rm -rf web/dist server/openos-server release openos-desktop
