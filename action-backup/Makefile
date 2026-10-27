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
# 发布目标平台。OEC-Turbo 是 linux-arm64，其余供 mac 开发与 x86 机器使用。
PLATFORMS := linux-arm64 linux-amd64 darwin-arm64 darwin-amd64

.PHONY: tools fixtures dev web-install web-dev web-build lint fmt vet test test-integration test-recovery \
        build build-arm64 build-arm64-test build-all package package-all tag deploy check-config clean
# 注意：build-% / package-% 的具体目标不能列进 .PHONY，GNU make 不会为 phony 目标套用模式规则。

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

web-install: ## 安装前端依赖（需 node / pnpm）
	pnpm --dir web install --frozen-lockfile

web-dev: web-install ## Vite 开发服务器，/api 代理到 127.0.0.1:8080（先 make dev）
	pnpm --dir web dev

web-build: web-install ## 构建前端到 web/dist；产物已入库，改动前端后必须重新执行并一起提交
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

build-%: ## production 交叉编译，不含 faultinject：make build-darwin-arm64 → dist/action-backup-darwin-arm64
	CGO_ENABLED=0 GOOS=$(word 1,$(subst -, ,$*)) GOARCH=$(word 2,$(subst -, ,$*)) \
	  go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/$(BIN)-$* ./cmd/$(BIN)

build-arm64: build-linux-arm64 ## 兼容别名，等同 build-linux-arm64

build-all: $(addprefix build-,$(PLATFORMS)) ## 四个发布平台全部交叉编译

build-arm64-test: ## 测试 ARM64 构建，含 faultinject，文件名带 -test
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -tags faultinject -ldflags "$(LDFLAGS)" -o $(DIST)/$(BIN)-test-linux-arm64 ./cmd/$(BIN)

package-%: build-% ## 打单个平台的包：make package-darwin-arm64
	VERSION=$(VERSION) TARGET=$* bash scripts/package.sh

package: package-linux-arm64 ## 默认产出 OEC-Turbo 安装包 dist/action-backup-<version>-linux-arm64.tar.gz

package-all: $(addprefix package-,$(PLATFORMS)) ## 四个发布平台全部打包

tag: ## 打发布 tag 并推送，触发 GitHub Release 工作流：make tag V=0.6.0
	@[ -n "$(V)" ] || { echo "用法: make tag V=0.6.0"; exit 2; }
	@git diff --quiet HEAD || { echo "工作区有未提交改动，先提交再打 tag"; exit 2; }
	git tag -a "v$(V)" -m "action-backup $(V)"
	git push origin "v$(V)"

deploy: ## 部署到 OEC-Turbo：打包 + scp + install.sh；HOST= 指定主机，RESTART=1 安装后重启
	HOST=$(HOST) RESTART=$(RESTART) bash scripts/deploy.sh

clean:
	rm -rf $(DIST) dev
