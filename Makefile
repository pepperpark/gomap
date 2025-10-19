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

.PHONY: tidy
tidy:
	go mod tidy

.PHONY: vet
vet:
	go vet ./...

.PHONY: test
test:
	go test ./...

.PHONY: check-v
check-v:
	@if [ -z "$(v)" ]; then echo "Usage: make release v=1.2.3 [rc=1]"; exit 1; fi

.PHONY: check-clean
check-clean:
	@if [ "x$(ALLOW_DIRTY)" != "x1" ]; then \
		git diff-index --quiet HEAD -- || { echo "Working tree not clean. Commit or stash changes, or set ALLOW_DIRTY=1 to override."; exit 1; }; \
	fi

.PHONY: release
release: check-v check-clean tidy vet test
	@if [ -n "$(rc)" ]; then \
		$(MAKE) tag-rc v=$(v) rc=$(rc); \
	else \
		$(MAKE) tag v=$(v); \
	fi
	@echo "Release initiated for version v$(v)$(if $(rc),-rc.$(rc),)."

.PHONY: tag
tag:
	@if [ -z "$(v)" ]; then echo "Usage: make tag v=1.2.3"; exit 1; fi
	@git tag -a v$(v) -m "Release v$(v)"
	@git push origin v$(v)
	@echo "Tagged and pushed v$(v). GitHub Action will build and release via GoReleaser."

.PHONY: tag-rc
tag-rc:
	@if [ -z "$(v)" ] || [ -z "$(rc)" ]; then echo "Usage: make tag-rc v=1.2.3 rc=1"; exit 1; fi
	@git tag -a v$(v)-rc.$(rc) -m "Release candidate v$(v)-rc.$(rc)"
	@git push origin v$(v)-rc.$(rc)
	@echo "Tagged and pushed v$(v)-rc.$(rc)."

.PHONY: release-dry
release-dry:
	@which goreleaser >/dev/null 2>&1 || { echo "GoReleaser not installed. Install or rely on CI."; exit 1; }
	@goreleaser release --clean --skip=publish
