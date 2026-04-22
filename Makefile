.DEFAULT_GOAL := build

export NAME     ?= fleeting-plugin-scaleway
export VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
export OUT_PATH ?= out
export CGO_ENABLED := 0

REVISION  := $(shell git rev-parse --short=8 HEAD 2>/dev/null || echo unknown)
REFERENCE := $(shell git symbolic-ref --short HEAD 2>/dev/null || echo HEAD)
BUILT     := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
PKG       := github.com/codecentric/fleeting-plugin-scaleway

GO_LDFLAGS := \
	-X $(PKG).VERSION=$(VERSION) \
	-X $(PKG).REVISION=$(REVISION) \
	-X $(PKG).REFERENCE=$(REFERENCE) \
	-X $(PKG).BUILT=$(BUILT) \
	-w -s

# All OS/arch targets.
# Format: <os>/<arch> or <os>/<arch>/<variant> (variant only for ARM).
# Windows binaries get a .exe suffix via the build rule.
OS_ARCHS ?= \
	linux/amd64 \
	linux/arm64 \
	linux/arm/7 \
	linux/386 \
	darwin/amd64 \
	darwin/arm64 \
	windows/amd64 \
	windows/386

# ── build (host only) ────────────────────────────────────────────────────────

.PHONY: build
build:
	@mkdir -p $(OUT_PATH)
	go build -ldflags "$(GO_LDFLAGS)" -o $(OUT_PATH)/$(NAME) ./cmd/$(NAME)/...

# ── cross-compile all targets ─────────────────────────────────────────────────

# dist/<os>/<arch[variant]>/plugin[.exe]
# This layout is exactly what fleeting-artifact expects.
.PHONY: all
all:
	@$(MAKE) $(foreach t,$(OS_ARCHS),_build_$(subst /,_,$(t)))

# Generic rule: _build_<os>_<arch> or _build_<os>_<arch>_<variant>
_build_%:
	$(eval PARTS  := $(subst _, ,$*))
	$(eval OS     := $(word 1,$(PARTS)))
	$(eval ARCH   := $(word 2,$(PARTS)))
	$(eval GOARM  := $(word 3,$(PARTS)))
	$(eval OUTDIR := dist/$(OS)/$(if $(GOARM),$(ARCH)v$(GOARM),$(ARCH)))
	$(eval EXT    := $(if $(filter windows,$(OS)),.exe,))
	@mkdir -p $(OUTDIR)
	GOOS=$(OS) GOARCH=$(ARCH) $(if $(GOARM),GOARM=$(GOARM),) \
		go build -a -ldflags "$(GO_LDFLAGS)" \
		-o $(OUTDIR)/plugin$(EXT) \
		./cmd/$(NAME)/...
	@echo "Built $(OUTDIR)/plugin$(EXT)"

# ── test ─────────────────────────────────────────────────────────────────────

.PHONY: test
test:
	go test -v -timeout=30m ./...

# ── OCI release (requires fleeting-artifact in PATH) ─────────────────────────

# Usage: make release-oci IMAGE=ghcr.io/codecentric/fleeting-plugin-scaleway VERSION=1.2.3
.PHONY: release-oci
release-oci:
	@if [ -z "$(IMAGE)" ]; then echo "IMAGE is required, e.g. make release-oci IMAGE=ghcr.io/org/fleeting-plugin-scaleway VERSION=1.2.3"; exit 1; fi
	@if [ -z "$(VERSION)" ]; then echo "VERSION is required"; exit 1; fi
	fleeting-artifact release -dir dist $(IMAGE):$(VERSION)

# ── clean ─────────────────────────────────────────────────────────────────────

.PHONY: clean
clean:
	rm -rf $(OUT_PATH) dist
