.PHONY: dev server web build release docker test goder clean

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
	cd server && go build -o openos-server .
	@echo "完成：cd server && ./openos-server  →  http://127.0.0.1:8420"

# 发布多平台二进制（darwin/linux × amd64/arm64），含前端产物
release: build
	@mkdir -p release/pkg
	@cp -r web/dist release/pkg/web-dist
	@cd server && for os_arch in "darwin amd64" "darwin arm64" "linux amd64" "linux arm64"; do \
		set -- $$os_arch; \
		echo "→ 构建 openos-$$1-$$2"; \
		mkdir -p ../release/pkg/openos-$$1-$$2; \
		CGO_ENABLED=0 GOOS=$$1 GOARCH=$$2 go build -trimpath -ldflags="-s -w" -o ../release/pkg/openos-$$1-$$2/openos .; \
	done
	@cd release && for d in pkg/openos-*; do \
		tar -czf "$$d.tar.gz" -C pkg "$$(basename $$d)" && echo "✓ $$d.tar.gz"; \
	done && rm -rf pkg
	@echo "发布包输出至 release/"

docker:
	docker build -t openos:latest .

clean:
	rm -rf web/dist server/openos-server release
