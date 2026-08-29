GO      ?= go
OUT_DIR ?= _output
BIN_DIR := $(OUT_DIR)/bin

CMDS := $(notdir $(wildcard cmd/*))
BINS := $(addprefix $(BIN_DIR)/,$(CMDS))

VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo unknown)
COMMIT    ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILDDATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS   := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILDDATE)

.PHONY: all build test vet fmt clean FORCE

all: build

build: $(BINS)

$(BIN_DIR)/%: FORCE
	@mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $@ ./cmd/$*

test:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

clean:
	rm -rf $(OUT_DIR)

FORCE:
