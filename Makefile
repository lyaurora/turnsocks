GO ?= go
TARGET ?= linux-amd64
TARGETS ?= linux-amd64 linux-arm64
BINDIR ?= bin/$(TARGET)
DIST_DIR ?= dist
CGO_ENABLED ?= 0
LDFLAGS ?= -s -w

.PHONY: build release check clean

build:
	@case "$(TARGET)" in \
		linux-amd64) GOOS=linux; GOARCH=amd64 ;; \
		linux-arm64) GOOS=linux; GOARCH=arm64 ;; \
		*) echo "Unsupported target: $(TARGET)" >&2; exit 1 ;; \
	esac; \
	mkdir -p "$(BINDIR)"; \
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$$GOOS GOARCH=$$GOARCH $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o "$(BINDIR)/turnsocks" .; \
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$$GOOS GOARCH=$$GOARCH $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o "$(BINDIR)/turnsocks-panel" ./panel

release:
	@rm -rf "$(DIST_DIR)"
	@mkdir -p "$(DIST_DIR)"
	@for target in $(TARGETS); do \
		case "$$target" in \
			linux-amd64) GOOS=linux; GOARCH=amd64 ;; \
			linux-arm64) GOOS=linux; GOARCH=arm64 ;; \
			*) echo "Unsupported target: $$target" >&2; exit 1 ;; \
		esac; \
		CGO_ENABLED=0 GOOS=$$GOOS GOARCH=$$GOARCH $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o "$(DIST_DIR)/turnsocks-$$target" .; \
		CGO_ENABLED=0 GOOS=$$GOOS GOARCH=$$GOARCH $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o "$(DIST_DIR)/turnsocks-panel-$$target" ./panel; \
	done
	@(cd "$(DIST_DIR)" && sha256sum turnsocks-linux-* turnsocks-panel-linux-* | sort > SHA256SUMS)

check:
	@sh -n install.sh
	@$(GO) test ./...
	@if [ -f "$(DIST_DIR)/SHA256SUMS" ]; then cd "$(DIST_DIR)" && sha256sum -c SHA256SUMS; fi
	@if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then git diff --check; fi

clean:
	rm -rf bin build dist
