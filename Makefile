BIN        ?= bin/p9s
PKG        := ./...
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS    := -s -w -X github.com/yindia/p9s/cmd/p9s/app.Version=$(VERSION)
CLUSTER    ?= p9s
E2E_CONTEXT ?= kind-$(CLUSTER)

.PHONY: all build run test test-race cover golden lint fmt e2e-up e2e e2e-down tapes clean

all: lint test build

build:
	go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/p9s

run: build
	$(BIN) $(ARGS)

test:
	go test $(PKG)

test-race:
	go test -race $(PKG)

cover:
	go test -race -coverprofile=coverage.out $(PKG)
	go tool cover -func=coverage.out | tail -1

## golden: regenerate golden files after an intentional UI change
golden:
	go test ./internal/ui/... ./test/golden/... -update

lint:
	golangci-lint run

fmt:
	golangci-lint fmt

## e2e-up: kind cluster + Gatekeeper + gatekeeper-library policies
e2e-up:
	CLUSTER=$(CLUSTER) ./hack/e2e-setup.sh

## e2e: create the cluster if needed, then run the e2e suite against it
e2e: e2e-up
	E2E_CONTEXT=$(E2E_CONTEXT) go test -tags e2e -count=1 -v ./test/e2e/...

e2e-down:
	kind delete cluster --name $(CLUSTER)

## tapes: render the vhs demos (requires vhs, ttyd, ffmpeg and `make e2e-up`)
# bin/ goes on PATH here: vhs applies a tape's `Env PATH` without
# expanding $$PATH, which hides ttyd from vhs itself.
tapes: build
	@for tool in vhs ttyd ffmpeg; do command -v $$tool >/dev/null || { echo "tapes: $$tool not found (brew install vhs)"; exit 1; }; done
	@kubectl config get-contexts kind-p9s >/dev/null 2>&1 || { echo "tapes: no kind-p9s context; run make e2e-up"; exit 1; }
	@set -e; for t in docs/tapes/*.tape; do PATH="$(CURDIR)/bin:$$PATH" vhs "$$t"; done

clean:
	rm -rf bin coverage.out
