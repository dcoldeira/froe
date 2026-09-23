GO      ?= $(HOME)/.local/go/bin/go
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-X main.version=$(VERSION)"

.PHONY: build test vet fmt install clean doctor

build:
	$(GO) build $(LDFLAGS) -o froe ./cmd/froe

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

install: build
	install -Dm755 froe $(HOME)/.local/bin/froe

doctor: build
	./froe doctor

clean:
	rm -f froe
