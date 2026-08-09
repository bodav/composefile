BINARY    := composefile
PKG       := ./cmd/composefile
OUT       ?= bin

# Where `make install` places the binary. ~/.local/bin is on the user's PATH.
GOBIN     ?= $(HOME)/.local/bin

VERSION   ?= dev
LDFLAGS   := -s -w -X main.version=$(VERSION)

.PHONY: all build vet test test-race fmt fmt-check clean install

all: build

build:
	go build -trimpath -ldflags '$(LDFLAGS)' -o $(OUT)/$(BINARY) $(PKG)

install:
	GOBIN=$(GOBIN) go install -trimpath -ldflags '$(LDFLAGS)' $(PKG)

vet:
	go vet ./...

test:
	go test ./...

test-race:
	go test -race -count=1 ./...

fmt:
	go fmt ./...

fmt-check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed on:"; echo "$$out"; exit 1; fi

clean:
	rm -rf $(OUT) .bundle