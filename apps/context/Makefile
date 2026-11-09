.PHONY: build test check install
VERSION ?= dev
PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin
SWIFT ?= swift
SWIFT_FLAGS ?=
export SWIFT SWIFT_FLAGS
build:
	sh scripts/build.sh "$(VERSION)"
test:
	$(SWIFT) test $(SWIFT_FLAGS)
	$(SWIFT) build $(SWIFT_FLAGS)
	MYCTX_BINARY="$$($(SWIFT) build $(SWIFT_FLAGS) --show-bin-path)/myctx" python3 -m unittest discover -s Tests -v
check:
	$(SWIFT) format lint --strict --recursive Sources Tests Package.swift
	$(SWIFT) build $(SWIFT_FLAGS) -Xswiftc -warnings-as-errors
install:
	sh scripts/install.sh "$(BINDIR)" "$(VERSION)"
