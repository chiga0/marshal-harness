GO ?= go
BINARY ?= bin/marshal
VERSION ?= dev
COMMIT ?= unknown
BUILD_DATE ?= unknown
SELF_PROFILE ?= unprofiled
GO_FILES := $(shell find cmd internal schemas -type f -name '*.go')
LDFLAGS_BASE := -s -w \
	-X github.com/chiga0/marshal-harness/internal/buildinfo.version=$(VERSION) \
	-X github.com/chiga0/marshal-harness/internal/buildinfo.commit=$(COMMIT) \
	-X github.com/chiga0/marshal-harness/internal/buildinfo.buildDate=$(BUILD_DATE)
LDFLAGS := $(LDFLAGS_BASE) \
	-X github.com/chiga0/marshal-harness/internal/buildinfo.selfProfile=$(SELF_PROFILE)

.PHONY: format format-check architecture-check vet lint test build dist vuln check ci

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

test:
	$(GO) test -race -p 2 ./...

build:
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/marshal

# 发布资产 target 矩阵，与 scripts/install.sh 平台检测口径一致。
DIST_DIR ?= dist
DIST_TARGETS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64

# 交叉编译四个平台的静态二进制（CGO_ENABLED=0），输出 naming 与 SHA256SUMS 格式
# 遵循 docs/development.md「Release 资产命名约定」，即 scripts/install.sh 的下载约定。
# 资产与校验清单默认落在被 Git 忽略的 dist/；sha256sum 缺失时回退 shasum。
dist:
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
			$(GO) build -trimpath -ldflags "$(LDFLAGS_BASE) -X github.com/chiga0/marshal-harness/internal/buildinfo.selfProfile=$$self_profile" -o "$$out" ./cmd/marshal; \
	done
	@set -e; cd "$(DIST_DIR)"; \
	files="$$(LC_ALL=C ls marshal_* 2>/dev/null)"; \
	[ -n "$$files" ] || { echo "[dist] 错误: 未找到发布资产" >&2; exit 1; }; \
	if command -v sha256sum >/dev/null 2>&1; then \
		for f in $$files; do sha256sum "$$f"; done > SHA256SUMS; \
	elif command -v shasum >/dev/null 2>&1; then \
		for f in $$files; do shasum -a 256 "$$f"; done > SHA256SUMS; \
	else \
		echo "[dist] 错误: 缺少 sha256sum/shasum，无法生成 SHA256SUMS" >&2; exit 1; \
	fi; \
	echo "[dist] 已生成 $(DIST_DIR)/SHA256SUMS"

vuln:
	$(GO) tool govulncheck ./...

check: format-check architecture-check vet lint test build

ci: check vuln
