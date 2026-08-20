# 2papi — build, test, release targets.
# Usage:
#   make build          # host binary → bin/2papi
#   make test           # go test ./... + go vet
#   make cross          # linux/darwin/windows × amd64/arm64 → dist/
#   make release        # goreleaser release --clean (requires tag + GITHUB_TOKEN)
#   make install        # curl|sh semantics via scripts

BIN      := bin/2papi
LDFLAGS  := -s -w
VERSION  ?= dev
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LD       := -X github.com/Rethinger/2papi/cmd/gateway.version=$(VERSION) \
            -X github.com/Rethinger/2papi/cmd/gateway.commit=$(COMMIT) \
            -X github.com/Rethinger/2papi/cmd/gateway.date=$(DATE)

.PHONY: build test vet cross release install clean

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS) $(LD)" -o $(BIN) ./cmd/gateway

test:
	go vet ./...
	go test -race ./...

vet:
	go vet ./...

# Explicit cross matrix (no make $$ escaping surprises).
DIST := dist

.PHONY: cross cross-linux-amd64 cross-linux-arm64 cross-darwin-amd64 cross-darwin-arm64 cross-windows-amd64

cross: cross-linux-amd64 cross-linux-arm64 cross-darwin-amd64 cross-darwin-arm64 cross-windows-amd64

cross-linux-amd64:
	@mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS) $(LD)" -o $(DIST)/2papi_linux_amd64 ./cmd/gateway
	@echo "-> linux/amd64"

cross-linux-arm64:
	@mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS) $(LD)" -o $(DIST)/2papi_linux_arm64 ./cmd/gateway
	@echo "-> linux/arm64"

cross-darwin-amd64:
	@mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS) $(LD)" -o $(DIST)/2papi_darwin_amd64 ./cmd/gateway
	@echo "-> darwin/amd64"

cross-darwin-arm64:
	@mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS) $(LD)" -o $(DIST)/2papi_darwin_arm64 ./cmd/gateway
	@echo "-> darwin/arm64"

cross-windows-amd64:
	@mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS) $(LD)" -o $(DIST)/2papi_windows_amd64.exe ./cmd/gateway
	@echo "-> windows/amd64"

release:
	goreleaser release --clean

install:
	sh install.sh

clean:
	rm -rf bin dist
