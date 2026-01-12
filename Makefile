APP=gomap
PKG=./cmd/gomap

# Default VERSION comes from the latest git tag (strip leading 'v'),
# falling back to 'dev' if no tags exist. Can be overridden: `make build VERSION=1.2.3`.
VERSION ?= $(shell (git describe --tags --abbrev=0 2>/dev/null || echo dev) | sed 's/^v//')
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

# .PHONY: release
# release: check-v check-clean tidy vet test
# 	@if [ -n "$(rc)" ]; then \
# 		$(MAKE) tag-rc v=$(v) rc=$(rc); \
# 	else \
# 		$(MAKE) tag v=$(v); \
# 	fi
# 	@echo "Release initiated for version v$(v)$(if $(rc),-rc.$(rc),)."

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

# ===== Debian/Ubuntu packaging (auto-run inside `make release`) =====
# Defaults (kannst du beim Aufruf überschreiben):
PPA            ?= pepperpark/gomap      # dein Launchpad PPA: ppa:pepperpark/gomap
DIST           ?= jammy                 # Zielserie (Zorin 17.3 = Ubuntu 22.04 = jammy)
DEBSIGN_KEYID  ?=                       # optional: GPG KeyID zum Signieren; leer = default key
DEBVERSION     ?= $(VERSION)-1          # Debian-Revision

# export DEBEMAIL  ?= $(shell git config user.email)
# export DEBFULLNAME ?= $(shell git config user.name)

# Maintainer identity for Debian packaging
DEBFULLNAME ?= 'Steve Rakebrandt'
DEBEMAIL    ?= '0@00x0.dev'
export DEBFULLNAME
export DEBEMAIL

define _ensure_debian_files
	@mkdir -p debian debian/source
	@[ -f debian/source/format ] || echo "3.0 (quilt)" > debian/source/format
	@[ -f debian/control ] || cat > debian/control <<'EOF'
Source: $(APP)
Section: utils
Priority: optional
Maintainer: $(DEBFULLNAME) <$(DEBEMAIL)>
Standards-Version: 4.7.0
Rules-Requires-Root: no
Build-Depends: debhelper-compat (= 13), golang-any

Package: $(APP)
Architecture: any
Depends: ${shlibs:Depends}, ${misc:Depends}
Description: IMAP CLI to copy, send and receive messages
 A small CLI tool written in Go.
EOF
	@[ -f debian/rules ] || { \
		cat > debian/rules <<'EOF'; \
#!/usr/bin/make -f
%:
	dh $@

override_dh_auto_build:
	GO111MODULE=on go build -ldflags '$(LDFLAGS)' -o $(APP) $(PKG)

override_dh_auto_install:
	install -D -m 0755 $(APP) debian/$(APP)/usr/bin/$(APP)
EOF
		chmod +x debian/rules; }
endef

.PHONY: deb-changelog
deb-changelog:
	@command -v dch >/dev/null || { echo "Missing 'devscripts' (dch). Install: sudo apt install devscripts"; exit 1; }
	@rm -f debian/changelog
	@dch --create --package $(APP) --newversion $(DEBVERSION) --distribution $(DIST) "Release v$(VERSION)"
	@# Wenn git-buildpackage vorhanden ist, Commits seit letztem Tag in den Changelog mergen:
	@if command -v gbp >/dev/null 2>&1; then \
		PREV_TAG=$$(git describe --tags --abbrev=0 --exclude "v$(VERSION)" 2>/dev/null || true); \
		if [ -n "$$PREV_TAG" ]; then \
			gbp dch --since $$PREV_TAG --release --distribution $(DIST) --spawn-editor=never; \
		else \
			gbp dch --release --distribution $(DIST) --spawn-editor=never; \
		fi \
	fi


.PHONY: deb-src
deb-src: tidy vet test deb-changelog
	@echo ">> Building Debian source package for $(DIST) ..."
	# -S: source only, -sa: include orig tarball, -us -uc: do not sign (Launchpad signiert selbst)
	dpkg-buildpackage -S -sa -us -uc
	@echo ">> Done."
	@ls -1 ../$(APP)_*_source.changes | tail -n1

# robusten Pfad aus debian/changelog ableiten
CHANGES_LATEST = $(shell dpkg-parsechangelog -SVersion >/dev/null 2>&1 && \
                          v=$$(dpkg-parsechangelog -SVersion); \
                          ls -1 ../$(APP)_$${v}_source.changes 2>/dev/null || true)

.PHONY: ppa-upload
ppa-upload: deb-src
	@chg="$(CHANGES_LATEST)"; \
	if [ -z "$$chg" ]; then \
	  echo "No .changes file found. Did deb-src succeed?"; exit 1; \
	fi; \
	echo ">> Uploading $$chg to ppa:$(PPA) ..."; \
	dput ppa:$(PPA) "$$chg"

# === EIN einziges release-Target, das Git-Tag + PPA macht ===
.PHONY: release
release: check-v check-clean tidy vet test
	@if [ -n "$(rc)" ]; then \
		$(MAKE) tag-rc v=$(v) rc=$(rc); \
	else \
		$(MAKE) tag v=$(v); \
	fi
	@echo ">> Git tag pushed: v$(v)"
	# Versionen für Paketierung setzen
	$(eval VERSION := $(v))
	$(eval DEBVERSION := $(VERSION)-1)
	$(eval LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE))
	$(call _ensure_debian_files)
	$(MAKE) deb-changelog DIST=$(DIST)
	$(MAKE) ppa-upload DIST=$(DIST)
	@echo ">> Release complete: v$(v) uploaded to ppa:$(PPA)"

.PHONY: deb-clean
deb-clean:
	rm -rf debian/.debhelper debian/files ../$(APP)_*
