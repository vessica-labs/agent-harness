.PHONY: build test verify

build:
	go build -o bin/agent-harness ./cmd/agent-harness

test:
	go test ./...

verify:
	cmp scripts/harnessctl.py ../plugins/agent-harness/scripts/harnessctl.py
	gofmt -d $$(find . -name '*.go' -type f)
	go test -race ./...
	go vet ./...
	go build -o /tmp/agent-harness-verify ./cmd/agent-harness
