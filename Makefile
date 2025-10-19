APP=gomap
PKG=./cmd/gomap

VERSION ?= 1.0.0
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo local)
DATE    := $(shell date -u +%Y-%m-%d)
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: build
build:
	go build -ldflags "$(LDFLAGS)" -o $(APP) $(PKG)

.PHONY: run
run: build
	./$(APP) --version
