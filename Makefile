VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
PKG     := github.com/KVM-X/KVM-X/internal/cli
LDFLAGS := -s -w -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT)
GOFLAGS := -trimpath
export CGO_ENABLED = 0

.PHONY: build test lint fmt vet fixtures integration dist docker clean

build:
	go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o bin/kvmx ./cmd/kvmx

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -s -w .

lint:
	@test -z "$$(gofmt -s -l .)" || { gofmt -s -l .; echo "run make fmt"; exit 1; }
	go vet ./...
	golangci-lint run
	shellcheck install.sh scripts/*.sh

fixtures:
	python3 testdata/gen_fixtures.py

# End-to-end tests: real TCG boots, shims, Docker. Needs qemu and docker; downloads ~22 MB once.
integration: build
	KVMX_BIN=$(CURDIR)/bin/kvmx go test -tags integration -count=1 -timeout 30m ./test/...

dist:
	mkdir -p dist
	for a in amd64 arm64; do \
	  GOOS=linux GOARCH=$$a go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o dist/kvmx-linux-$$a ./cmd/kvmx; \
	done
	cd dist && sha256sum kvmx-linux-* > SHA256SUMS

docker:
	docker build --build-arg VERSION=$(VERSION) -t kvmx:$(VERSION) .

clean:
	go clean
