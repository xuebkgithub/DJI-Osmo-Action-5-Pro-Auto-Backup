# action-backup Makefile。开发机 macOS，目标 OEC-Turbo ARM64 Linux。
SHELL := /bin/bash
MODULE := action-backup
BIN := action-backup
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
  -X $(MODULE)/internal/version.Version=$(VERSION) \
  -X $(MODULE)/internal/version.Commit=$(COMMIT) \
  -X $(MODULE)/internal/version.BuildTime=$(BUILD_TIME)
DEV_CONFIG ?= configs/config.dev.yaml
DIST := dist

.PHONY: tools fixtures dev web-dev web-build lint fmt vet test test-integration test-recovery \
        build build-arm64 build-arm64-test package deploy check-config clean

tools: ## 检查工具链
	@go version
	@node -v 2>/dev/null || echo "node: 未安装（仅 Web UI 需要）"
	@pnpm -v 2>/dev/null || echo "pnpm: 未安装（仅 Web UI 需要）"
	@golangci-lint --version 2>/dev/null || echo "golangci-lint: 未安装，brew install golangci-lint"

fixtures: ## 生成模拟相机卷 tests/fixtures/cam1
	bash scripts/gen-fixtures.sh --out tests/fixtures/cam1

dev: ## mac 上以 simulation 模式运行（测试构建）
	@mkdir -p dev/nas
	go run -tags faultinject ./cmd/$(BIN) --config $(DEV_CONFIG)

check-config: ## 校验配置
	go run ./cmd/$(BIN) --config $(DEV_CONFIG) --check

web-dev: ## Vite 开发服务器（V0.5）
	pnpm --dir web dev

web-build: ## 构建前端到 web/dist（V0.5）
	pnpm --dir web build

fmt:
	gofmt -l -w ./cmd ./internal ./tests ./web

vet:
	go vet ./...

lint: ## 静态检查
	golangci-lint run ./...

test: ## 单元测试（含 faultinject 构建下的注入器与应用测试）
	go test -race -count=1 ./internal/... ./web/...
	go test -race -count=1 -tags faultinject ./internal/faultinject/... ./internal/app/...

test-integration: ## simulation 模式端到端
	go test -race -count=1 -tags faultinject,integration ./tests/integration/...

test-recovery: ## 故障注入 + 进程重启
	go test -race -count=1 -timeout 10m -tags faultinject,recovery ./tests/recovery/...

build: ## 本机构建
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/$(BIN) ./cmd/$(BIN)

build-arm64: ## production ARM64 构建，不含 faultinject
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/$(BIN)-linux-arm64 ./cmd/$(BIN)

build-arm64-test: ## 测试 ARM64 构建，含 faultinject，文件名带 -test
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -tags faultinject -ldflags "$(LDFLAGS)" -o $(DIST)/$(BIN)-test-linux-arm64 ./cmd/$(BIN)

package: build-arm64 ## 产出安装包 dist/action-backup-<version>-linux-arm64.tar.gz
	VERSION=$(VERSION) bash scripts/package.sh

deploy: ## 预留：OEC-Turbo 可 SSH 后实现
	@echo "deploy 尚未实现：设备可 SSH 后在 scripts/deploy.sh 中补充 scp + install.sh + systemctl restart"; exit 1

clean:
	rm -rf $(DIST) dev
