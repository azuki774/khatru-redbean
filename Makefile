BINARY_NAME ?= khatru-redbean
BUILD_DIR ?= bin
PKG ?= ./
GOOS ?= linux
GOARCH ?= amd64
VERSION ?= $(shell git describe --tag --abbrev=0 2>/dev/null || echo 'dev')
REVISION ?= $(shell git rev-list -1 HEAD 2>/dev/null || echo 'unknown')
BUILD ?= $(shell git describe --tags 2>/dev/null || echo 'dev')
LDFLAGS ?= -s -w -extldflags '-static' \
	-X 'github.com/azuki774/khatru-redbean/internal/config.Version=$(VERSION)' \
	-X 'github.com/azuki774/khatru-redbean/internal/config.Revision=$(REVISION)' \
	-X 'github.com/azuki774/khatru-redbean/internal/config.Build=$(BUILD)'

.PHONY: bin build clean tidy fmt test staticcheck check setup

bin:
	mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME) $(PKG)

build:
	docker build -t azuki774/khatru-redbean:dev -f Dockerfile .

clean:
	rm -rf $(BUILD_DIR)
	go clean -modcache

fmt:
	@fmt_files=$$(gofmt -l .); \
	if [ -n "$$fmt_files" ]; then \
		echo "gofmt needed on:"; \
		echo "$$fmt_files"; \
		exit 1; \
	fi

test:
	go test -v ./...

staticcheck:
	staticcheck ./...

check: fmt test staticcheck

setup:
	go install honnef.co/go/tools/cmd/staticcheck@latest
	go install github.com/spf13/cobra-cli@latest
