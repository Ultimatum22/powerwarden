MODULE  := github.com/Ultimatum22/powerwarden
BINARY  := labpower
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# Never build on the Pi: cross-compile a static arm64 binary from here.
GOOS   ?= linux
GOARCH ?= arm64

.PHONY: build test lint vuln check release clean

build:
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build \
		-ldflags "-s -w -X main.version=$(VERSION)" \
		-o bin/$(BINARY) ./cmd/labpower

test:
	gofmt -l . | tee /tmp/gofmt-out; test ! -s /tmp/gofmt-out
	go vet ./...
	go test -race ./...

lint:
	staticcheck ./...

vuln:
	govulncheck ./...

# Everything CLAUDE.md requires before finishing a task.
check: test lint vuln

release: check build

clean:
	rm -rf bin
