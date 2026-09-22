# igdev build and validation entry points. The legacy bash foundation keeps its
# own ./devctl dispatcher; this Makefile covers only the Go toolchain (ticket 13
# retires the bash side).
GO      ?= go
VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)

.PHONY: all build test test-quick gates vet fmt fmt-check package goldens clean

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

clean:
	rm -rf bin dist
