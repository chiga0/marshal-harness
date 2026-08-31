GO ?= go
BINARY ?= bin/marshal
VERSION ?= dev
COMMIT ?= unknown
BUILD_DATE ?= unknown
SELF_PROFILE ?= unprofiled
GO_FILES := $(shell find cmd internal schemas -type f -name '*.go')
GO_BUILD_FLAGS := -trimpath -buildvcs=false -mod=readonly
LDFLAGS_BASE := -s -w -buildid= \
	-X github.com/chiga0/marshal-harness/internal/buildinfo.version=$(VERSION) \
	-X github.com/chiga0/marshal-harness/internal/buildinfo.commit=$(COMMIT) \
	-X github.com/chiga0/marshal-harness/internal/buildinfo.buildDate=$(BUILD_DATE)
LDFLAGS := $(LDFLAGS_BASE) \
	-X github.com/chiga0/marshal-harness/internal/buildinfo.selfProfile=$(SELF_PROFILE)
RC1_LDFLAGS := -w -buildid= \
	-X github.com/chiga0/marshal-harness/internal/buildinfo.version=$(VERSION) \
	-X github.com/chiga0/marshal-harness/internal/buildinfo.commit=$(COMMIT) \
	-X github.com/chiga0/marshal-harness/internal/buildinfo.buildDate=$(BUILD_DATE) \
	-X github.com/chiga0/marshal-harness/internal/buildinfo.selfProfile=darwin-local-dogfood

.PHONY: format format-check architecture-check vet lint test build dist dist-rc1 vuln release-check check ci

format:
	gofmt -w $(GO_FILES)

format-check:
	@test -z "$$(gofmt -l $(GO_FILES))"

architecture-check:
	python3 -B scripts/architecture_check_test.py
	python3 -B scripts/architecture_check.py --go "$(GO)"

vet:
	$(GO) vet ./...

lint:
	$(GO) tool staticcheck ./...

# Test binaries must carry the same bound source head as release builds: the
# Darwin supervisor identity contract rejects a binary whose commit is not the
# exact 40-hex source head.
GIT_COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)

test:
	$(GO) test -race -p 2 -ldflags "$(LDFLAGS_BASE) -X github.com/chiga0/marshal-harness/internal/buildinfo.commit=$(GIT_COMMIT)" ./...

build:
	$(GO) build $(GO_BUILD_FLAGS) -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/marshal

# 发布资产 target 矩阵，与 scripts/install.sh 平台检测口径一致。
DIST_DIR ?= dist
DIST_TARGETS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64

# 交叉编译四个平台的静态二进制（CGO_ENABLED=0），输出 naming 与 SHA256SUMS 格式
# 遵循 docs/development.md「Release 资产命名约定」，即 scripts/install.sh 的下载约定。
# 资产与校验清单默认落在被 Git 忽略的 dist/；sha256sum 缺失时回退 shasum。
dist:
	@required="$$(sed -n -E 's/^toolchain[[:space:]]+(go[0-9]+\.[0-9]+\.[0-9]+)[[:space:]]*$$/\1/p' go.mod)"; \
	actual="$$($(GO) env GOVERSION)"; \
	[ -n "$$required" ] && [ "$$actual" = "$$required" ] || { \
		echo "[dist] 错误: release Go toolchain 漂移：期望 $$required，实际 $$actual" >&2; exit 1; \
	}
	@rm -rf "$(DIST_DIR)"
	@mkdir -p "$(DIST_DIR)"
	@set -e; for t in $(DIST_TARGETS); do \
		os="$${t%/*}"; arch="$${t#*/}"; \
		case "$$os" in \
			darwin) self_profile="darwin-local-dogfood" ;; \
			linux) self_profile="unprofiled" ;; \
			*) echo "[dist] 错误: 未配置 $$os 的 self profile" >&2; exit 1 ;; \
		esac; \
		out="$(DIST_DIR)/marshal_$(VERSION)_$${os}_$${arch}"; \
		echo "[dist] $$os/$$arch ($$self_profile) -> $$out"; \
		CGO_ENABLED=0 GOOS="$$os" GOARCH="$$arch" \
			$(GO) build $(GO_BUILD_FLAGS) -ldflags "$(LDFLAGS_BASE) -X github.com/chiga0/marshal-harness/internal/buildinfo.selfProfile=$$self_profile" -o "$$out" ./cmd/marshal; \
	done
	@bash scripts/release-contract.sh create-manifest "$(DIST_DIR)" "v$(VERSION)" "$(COMMIT)" "$(BUILD_DATE)" "$$($(GO) env GOVERSION)"
	@set -e; cd "$(DIST_DIR)"; \
	files="$$(LC_ALL=C ls RELEASE-MANIFEST marshal_* 2>/dev/null)"; \
	[ -n "$$files" ] || { echo "[dist] 错误: 未找到发布资产" >&2; exit 1; }; \
	if command -v sha256sum >/dev/null 2>&1; then \
		for f in $$files; do sha256sum "$$f"; done > SHA256SUMS; \
	elif command -v shasum >/dev/null 2>&1; then \
		for f in $$files; do shasum -a 256 "$$f"; done > SHA256SUMS; \
	else \
		echo "[dist] 错误: 缺少 sha256sum/shasum，无法生成 SHA256SUMS" >&2; exit 1; \
	fi; \
	echo "[dist] 已生成 $(DIST_DIR)/SHA256SUMS"

# ADR 0068 RC1 是与 stable dist 相互隔离的单资产合同。它只接受
# v1.0.0-rc1 + darwin/arm64 + darwin-local-dogfood；不得借用 dist 的
# Linux/amd64 资产集合，也不为 stable 提供任何发布权限。
dist-rc1:
	@required="$$(sed -n -E 's/^toolchain[[:space:]]+(go[0-9]+\.[0-9]+\.[0-9]+)[[:space:]]*$$/\1/p' go.mod)"; \
	actual="$$($(GO) env GOVERSION)"; \
	[ -n "$$required" ] && [ "$$actual" = "$$required" ] || { \
		echo "[dist-rc1] 错误: release Go toolchain 漂移：期望 $$required，实际 $$actual" >&2; exit 1; \
	}; \
	bash scripts/release-contract.sh validate-rc1-inputs \
		"v$(VERSION)" "$(COMMIT)" "$(BUILD_DATE)" "$$actual"
	@if [ -e "$(DIST_DIR)" ] || [ -L "$(DIST_DIR)" ]; then \
		echo "[dist-rc1] 错误: build-once 输出目录必须不存在：$(DIST_DIR)" >&2; exit 1; \
	fi
	@mkdir "$(DIST_DIR)"
	@out="$(DIST_DIR)/marshal_$(VERSION)_darwin_arm64"; \
	echo "[dist-rc1] darwin/arm64 (darwin-local-dogfood) -> $$out"; \
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 \
		$(GO) build $(GO_BUILD_FLAGS) -ldflags "$(RC1_LDFLAGS)" -o "$$out" ./cmd/marshal; \
	[ -x /usr/bin/codesign ] || { \
		echo "[dist-rc1] 错误: 缺少固定 /usr/bin/codesign，无法冻结 Darwin candidate 身份" >&2; exit 1; \
	}; \
	/usr/bin/codesign --force --sign - --identifier dev.marshal.cli --timestamp=none "$$out"; \
	/usr/bin/codesign --verify --strict --verbose=2 "$$out"
	@GO_BIN="$$($(GO) env GOROOT)/bin/go" bash scripts/release-contract.sh create-rc1-manifest \
		"$(DIST_DIR)" "v$(VERSION)" "$(COMMIT)" "$(BUILD_DATE)" "$$($(GO) env GOVERSION)"
	@set -e; cd "$(DIST_DIR)"; \
	files="RELEASE-MANIFEST marshal_$(VERSION)_darwin_arm64"; \
	if command -v sha256sum >/dev/null 2>&1; then \
		for f in $$files; do sha256sum "$$f"; done > SHA256SUMS; \
	elif command -v shasum >/dev/null 2>&1; then \
		for f in $$files; do shasum -a 256 "$$f"; done > SHA256SUMS; \
	else \
		echo "[dist-rc1] 错误: 缺少 sha256sum/shasum，无法生成 SHA256SUMS" >&2; exit 1; \
	fi
	@GO_BIN="$$($(GO) env GOROOT)/bin/go" bash scripts/release-contract.sh verify-rc1-dist \
		"$(DIST_DIR)" "v$(VERSION)" "$(COMMIT)" "$(BUILD_DATE)" \
		"$$($(GO) env GOVERSION)" darwin arm64 darwin-local-dogfood >/dev/null
	@echo "[dist-rc1] 已冻结唯一 Darwin arm64 candidate"

vuln:
	$(GO) tool govulncheck ./...

# Local convenience only. CI release authority invokes the fixed Python checker
# before any candidate Make/script execution and does not trust this target.
release-check:
	bash scripts/release-contract_test.sh
	bash scripts/release-ci-gate_test.sh
	bash scripts/dist-profile_test.sh
	bash scripts/install_test.sh
	bash scripts/release-canary_test.sh
	/usr/bin/python3 -I -B scripts/rc1-carrier-check_test.py

check: format-check architecture-check vet lint test build

ci: check vuln
