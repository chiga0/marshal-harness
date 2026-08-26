GO ?= go
BINARY ?= bin/marshal
VERSION ?= dev
COMMIT ?= unknown
BUILD_DATE ?= unknown
SELF_PROFILE ?= unprofiled
GO_FILES := $(shell find cmd internal schemas -type f -name '*.go')
LDFLAGS := -s -w \
	-X github.com/chiga0/marshal-harness/internal/buildinfo.version=$(VERSION) \
	-X github.com/chiga0/marshal-harness/internal/buildinfo.commit=$(COMMIT) \
	-X github.com/chiga0/marshal-harness/internal/buildinfo.buildDate=$(BUILD_DATE) \
	-X github.com/chiga0/marshal-harness/internal/buildinfo.selfProfile=$(SELF_PROFILE)

.PHONY: format format-check architecture-check vet lint test build vuln check ci

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

vuln:
	$(GO) tool govulncheck ./...

check: format-check architecture-check vet lint test build

ci: check vuln
