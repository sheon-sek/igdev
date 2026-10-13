# igdev build and validation entry points: this repository is the CLI's home, so
# these are the project commands its own igdev.toml dispatches (see AGENTS.md).
GO      ?= go
VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)

.PHONY: all build test test-quick gates soak vet fmt fmt-check package goldens reference reference-check clean

all: fmt-check vet test

build:
	$(GO) build -trimpath -ldflags "-s -w" -o bin/igdev ./cmd/igdev

# The whole suite, resource and hygiene gates included. It needs no network and
# no docker: the rig shims docker and serves the release endpoint on loopback.
test:
	$(GO) test ./...

# Behaviour only: skip the timing, packaging, and installer gates.
test-quick:
	$(GO) test ./... -skip 'TestGate|TestPackage|TestInstaller'

gates:
	$(GO) test ./itest/ -run 'TestGate' -v

# The opt-in real-Gateway soak: real docker, a person's Consent record, and one
# disposable Instance it removes again. Never part of `all`, `test` or CI; its
# workload, knobs and guarantees are in docs/lifecycle-matrix.md.
soak:
	IGDEV_SOAK=1 $(GO) test ./itest/ -run 'TestSoakRealGateway' -count=1 -v -timeout 180m

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

fmt-check:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed on:"; echo "$$out"; exit 1; fi

# Release artifacts: dist/<version>/igdev-<version>-<os>-<arch>.tar.gz plus
# dist/<version>/checksums.txt.
package:
	packaging/package.sh $(VERSION)

# Rewrite the goldens after an intentional contract change, then read the diff:
# they are the frozen agent contract, so never commit one unseen.
goldens:
	IGDEV_UPDATE_GOLDENS=1 $(GO) test ./itest/

# Regenerate the public command reference (docs/reference/) from the command
# definitions. The committed copy is checked by `make reference-check`, by the
# igdev-ci step, and by the drift test in internal/docsgen.
reference:
	$(GO) run ./cmd/igdev-docs

reference-check:
	$(GO) run ./cmd/igdev-docs -check

clean:
	rm -rf bin dist
