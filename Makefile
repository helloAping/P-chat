# P-Chat Cross-Platform Build Makefile
#
# Windows targets use PowerShell; Linux targets use bash.
# Wails GUI binary must be built on the target OS (CGo + webview).
# The Go server binary and Vue SPA can be cross-compiled from anywhere.
#
#   make build-server          # Go server only (any OS)
#   make build-gui             # Wails GUI (builds for current OS)
#   make package               # Assemble distribution bundle
#   make package-linux         # Assemble Linux distribution bundle
#   make install               # Install (current OS)
#   make uninstall             # Uninstall (current OS)
#   make clean                 # Remove build artifacts

.PHONY: build-server build-gui build-frontend package package-linux install uninstall clean

GO        ?= go
NPM       ?= npm
WAILS     ?= wails
POWERSHELL ?= powershell

# --- Build targets ---

build-frontend:
	cd cmd/pchat-gui/frontend && $(NPM) install && $(NPM) run build

build-server:
	$(GO) build -o bin/pchat-server ./cmd/pchat-server
	$(GO) build -o bin/pchat ./cmd/pchat

build-server-linux:
	GOOS=linux GOARCH=amd64 $(GO) build -o bin/pchat-server-linux ./cmd/pchat-server

build-gui:
	cd cmd/pchat-gui && $(WAILS) build -platform windows/amd64 -s

build-gui-linux:
	cd cmd/pchat-gui && $(WAILS) build -platform linux/amd64 -s

# --- Package (Windows) ---

package: build-frontend build-server build-gui
	$(POWERSHELL) -NoProfile -ExecutionPolicy Bypass -File scripts/package-gui.ps1

# --- Package (Linux) ---

package-linux: build-frontend build-server build-gui-linux
	cp -f bin/pchat-server build/bin/pchat-server 2>/dev/null || true
	mkdir -p build/bin
	cp -f cmd/pchat-gui/build/bin/pchat-gui build/bin/pchat-gui 2>/dev/null || true
	cp -rf web/ build/bin/web/
	cp -f scripts/install-linux.sh build/bin/install.sh
	cp -f scripts/uninstall-linux.sh build/bin/uninstall.sh
	chmod +x build/bin/install.sh build/bin/uninstall.sh
	chmod +x build/bin/pchat-gui build/bin/pchat-server 2>/dev/null || true
	@echo "[package-linux] bundle ready at build/bin/"

# --- Install (current OS) ---

install:
ifeq ($(OS),Windows_NT)
	$(POWERSHELL) -NoProfile -ExecutionPolicy Bypass -File cmd/pchat-gui/build/bin/install.ps1
else
	bash build/bin/install.sh
endif

# --- Uninstall (current OS) ---

uninstall:
ifeq ($(OS),Windows_NT)
	$(POWERSHELL) -NoProfile -ExecutionPolicy Bypass -File cmd/pchat-gui/build/bin/uninstall.ps1
else
	bash build/bin/uninstall.sh
endif

# --- Clean ---

clean:
	rm -rf bin/ build/bin/ web/ cmd/pchat-gui/build/bin/
