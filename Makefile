BINARY = mail
MODULE = github.com/MiyuLabs/mail
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS = -ldflags "-X main.version=$(VERSION) -s -w"
INSTALL_DIR ?= $(HOME)/bin

# ── Build targets ─────────────────────────────────────────────────────────────

.PHONY: build
build:
	@mkdir -p bin
	go build $(LDFLAGS) -o bin/$(BINARY) ./cmd/mail

.PHONY: install
install: build
	@mkdir -p $(INSTALL_DIR)
	cp bin/$(BINARY) $(INSTALL_DIR)/$(BINARY)
	@echo "✅ Installed to $(INSTALL_DIR)/$(BINARY)"
	@echo "   Make sure $(INSTALL_DIR) is in your PATH."

.PHONY: package
package: build
	@echo "Packaging app with Fyne..."
	@mkdir -p dist
	$(shell go env GOPATH)/bin/fyne package -os $(shell go env GOOS) -icon $(PWD)/assets/icon.png -name "MiyuMail" -appID "in.miyulabs.mail" -executable bin/$(BINARY)
	@mv MiyuMail.tar.xz dist/ 2>/dev/null || true
	@mv MiyuMail.app dist/ 2>/dev/null || true
	@mv MiyuMail.exe dist/ 2>/dev/null || true

.PHONY: setup
setup: build
	@echo "Running first-time setup wizard..."
	./bin/$(BINARY) setup

.PHONY: run
run:
	go run ./cmd/mail

.PHONY: tidy
tidy:
	go mod tidy

.PHONY: test
test:
	go test ./internal/...

# ── Cross-platform builds (requires fyne-cross or direct GOOS/GOARCH) ─────────

.PHONY: build-linux
build-linux:
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o dist/$(BINARY)-linux-amd64 ./cmd/mail

.PHONY: build-mac
build-mac:
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o dist/$(BINARY)-darwin-arm64 ./cmd/mail

.PHONY: build-windows
build-windows:
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o dist/$(BINARY)-windows-amd64.exe ./cmd/mail

.PHONY: dist
dist: build-linux build-mac build-windows

# ── Auth helpers ──────────────────────────────────────────────────────────────

.PHONY: auth-gmail
auth-gmail:
	go run ./cmd/mail auth gmail

.PHONY: auth-resend
auth-resend:
	@read -p "Resend API key: " key && go run ./cmd/mail auth resend $$key

.PHONY: auth-token
auth-token:
	@read -p "Sync API token: " tok && go run ./cmd/mail auth token $$tok

.PHONY: auth-clear
auth-clear:
	go run ./cmd/mail auth clear

# ── Utilities ─────────────────────────────────────────────────────────────────

.PHONY: clean
clean:
	rm -rf bin/ dist/

.PHONY: lint
lint:
	golangci-lint run ./...

.PHONY: fmt
fmt:
	gofmt -w ./internal ./cmd
