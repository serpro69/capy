MODULE := github.com/serpro69/capy
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-X $(MODULE)/internal/version.Version=$(VERSION)"
BUILD_TAGS := -tags "fts5"

.PHONY: build test vet clean

build:
	CGO_ENABLED=1 go build $(BUILD_TAGS) $(LDFLAGS) -o capy ./cmd/capy/

test:
	CGO_ENABLED=1 go test $(BUILD_TAGS) ./...

vet:
	go vet $(BUILD_TAGS) ./...

clean:
	rm -f capy
