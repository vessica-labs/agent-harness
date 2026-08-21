.PHONY: build install test verify

VERSION ?= $(shell git -C .. describe --tags --always --dirty 2>/dev/null || printf 'dev')
PREFIX ?= $(HOME)/.local
BINARY := bin/agent-harness
VERSION_PACKAGE := github.com/vessica-labs/agent-harness/cloud-runner/internal/cli.Version
LDFLAGS := -s -w -X $(VERSION_PACKAGE)=$(VERSION)

build:
	mkdir -p bin
	go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/agent-harness

install: build
	install -d "$(DESTDIR)$(PREFIX)/bin"
	install -m 0755 $(BINARY) "$(DESTDIR)$(PREFIX)/bin/agent-harness"
	@"$(DESTDIR)$(PREFIX)/bin/agent-harness" version

test:
	go test ./...

verify:
	cmp scripts/harnessctl.py ../plugins/agent-harness/scripts/harnessctl.py
	@test -z "$$(gofmt -l $$(find . -name '*.go' -type f))" || \
		{ gofmt -l $$(find . -name '*.go' -type f); exit 1; }
	go test -race ./...
	go vet ./...
	go build -trimpath -ldflags="$(LDFLAGS)" -o /tmp/agent-harness-verify ./cmd/agent-harness
