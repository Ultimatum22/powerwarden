MODULE  := github.com/Ultimatum22/powerwarden
BINARY  := labpower
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# Never build on the Pi: cross-compile a static arm64 binary from here.
GOOS   ?= linux
GOARCH ?= arm64

DIST := bin/$(BINARY)-$(VERSION)-$(GOOS)-$(GOARCH)

.PHONY: build test lint vuln check release clean

build:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build \
		-ldflags "-s -w -X main.version=$(VERSION)" \
		-o bin/$(BINARY) ./cmd/labpower

test:
	@fmtout=$$(mktemp); gofmt -l . > "$$fmtout"; \
		if [ -s "$$fmtout" ]; then echo "gofmt -l found unformatted files:"; cat "$$fmtout"; rm -f "$$fmtout"; exit 1; fi; \
		rm -f "$$fmtout"
	go vet ./...
	go test -race ./...

lint:
	staticcheck ./...

vuln:
	govulncheck ./...

# Everything CLAUDE.md requires before finishing a task.
check: test lint vuln

# Release artifact: version- and platform-named binary plus its checksum,
# the two files the Forgejo Actions workflow attaches to a tagged release.
release: check
	mkdir -p bin
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build \
		-ldflags "-s -w -X main.version=$(VERSION)" \
		-o $(DIST) ./cmd/labpower
	sha256sum $(DIST) > $(DIST).sha256
	@echo "release artifact: $(DIST)"
	@echo "checksum:         $(DIST).sha256"

clean:
	rm -rf bin
