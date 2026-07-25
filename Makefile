BINARY  := dnswizard
PKG     := github.com/joda32/dnswizard
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X $(PKG)/cmd.Version=$(VERSION)

# Platforms built by `make build-all`.
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

.PHONY: all build install test race lint fmt tidy build-all clean

all: lint test build

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) .

install:
	go install -trimpath -ldflags "$(LDFLAGS)" .

test:
	go test ./...

race:
	go test -race ./...

lint:
	go vet ./...
	@unformatted=$$(gofmt -l . ); \
	if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi

fmt:
	gofmt -w .

tidy:
	go mod tidy

build-all:
	@mkdir -p dist
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		ext=""; [ "$$os" = "windows" ] && ext=".exe"; \
		out="dist/$(BINARY)-$$os-$$arch$$ext"; \
		echo "building $$out"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 \
			go build -trimpath -ldflags "$(LDFLAGS)" -o "$$out" . || exit 1; \
	done

clean:
	rm -rf $(BINARY) dist
