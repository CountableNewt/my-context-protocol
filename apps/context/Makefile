.PHONY: build test check install
VERSION ?= dev
PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin
GO ?= go
export GO
build:
	sh scripts/build.sh "$(VERSION)"
test:
	$(GO) test -race ./...
	$(GO) build -o bin/myctx .
	MYCTX_BINARY="$(CURDIR)/bin/myctx" python3 -m unittest discover -s Tests -v
check:
	@test -z "$$($(GO) fmt ./...)" || (echo 'Run go fmt ./...'; exit 1)
	$(GO) vet ./...
install:
	sh scripts/install.sh "$(BINDIR)" "$(VERSION)"
