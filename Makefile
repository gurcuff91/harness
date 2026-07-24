# Makefile for Harness
# A minimal AI agent harness built in pure Go.

.PHONY: build install clean test vet lint run help

# Binary name and paths
BINARY_NAME=harness
BINARY_DIR=.
INSTALL_DIR=$(HOME)/go/bin
GO=go

# Build flags
VERSION=v0.73.24
LDFLAGS=-ldflags "-s -w -X github.com/gurcuff91/harness/internal/version.Version=$(VERSION)"

# Default target
help:
	@echo "Harness - Make targets:"
	@echo ""
	@echo "  make build     - Build the binary (harness)"
	@echo "  make install   - Build and install to ~/go/bin"
	@echo "  make run       - Build and run immediately"
	@echo "  make clean    - Remove built artifacts"
	@echo "  make test      - Run all tests"
	@echo "  make vet       - Run go vet"
	@echo "  make fmt       - Format code"
	@echo "  make deps      - Download dependencies"
	@echo ""
	@echo "Usage:"
	@echo "  make build          # builds ./harness"
	@echo "  make install        # builds and installs to ~/go/bin"
	@echo "  HARNESS_MODEL=anthropic/claude-sonnet-4-20250514 make run"

# Build the binary
build:
	@echo "Building $(BINARY_NAME)..."
	$(GO) build $(LDFLAGS) -o $(BINARY_DIR)/$(BINARY_NAME) ./cmd/harness
	@echo "✅ Built: ./$(BINARY_NAME)"
	@ls -lh $(BINARY_DIR)/$(BINARY_NAME)

# Build with race detector
build-race:
	@echo "Building with race detector..."
	$(GO) build -race $(LDFLAGS) -o $(BINARY_DIR)/$(BINARY_NAME) ./cmd/harness
	@echo "✅ Built with -race: ./$(BINARY_NAME)"

# Build for multiple platforms
build-all:
	@echo "Building for all platforms..."
	@mkdir -p dist
	GOOS=darwin GOARCH=amd64 $(GO) build $(LDFLAGS) -o dist/$(BINARY_NAME)-darwin-amd64 ./cmd/harness
	GOOS=darwin GOARCH=arm64 $(GO) build $(LDFLAGS) -o dist/$(BINARY_NAME)-darwin-arm64 ./cmd/harness
	GOOS=linux GOARCH=amd64 $(GO) build $(LDFLAGS) -o dist/$(BINARY_NAME)-linux-amd64 ./cmd/harness
	GOOS=windows GOARCH=amd64 $(GO) build $(LDFLAGS) -o dist/$(BINARY_NAME)-windows-amd64.exe ./cmd/harness
	@echo "✅ Binaries in ./dist/"
	@ls -lh dist/

# Install to ~/go/bin
install: build
	@echo "Installing to $(INSTALL_DIR)/..."
	@mkdir -p $(INSTALL_DIR)
	cp $(BINARY_DIR)/$(BINARY_NAME) $(INSTALL_DIR)/$(BINARY_NAME)
	@codesign -f -s - $(INSTALL_DIR)/$(BINARY_NAME) 2>/dev/null || true
	@echo "✅ Installed to $(INSTALL_DIR)/$(BINARY_NAME)"
	@echo "Run 'harness' to start"

# Build and run
run: build
	@echo "Running $(BINARY_NAME)..."
	@./$(BINARY_NAME)

# Build and run with environment
run-model: build
	@echo "Running with model: $(HARNESS_MODEL)..."
	HARNESS_MODEL=$(HARNESS_MODEL) ./$(BINARY_NAME)

# Clean artifacts
clean:
	@echo "Cleaning..."
	@rm -f $(BINARY_DIR)/$(BINARY_NAME)
	@rm -rf dist/
	@echo "✅ Cleaned"

# Run tests
test:
	@echo "Running tests..."
	$(GO) test -v ./...

# Run tests with coverage
test-coverage:
	@echo "Running tests with coverage..."
	$(GO) test -v -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "✅ Coverage report: coverage.html"

# Run go vet
vet:
	@echo "Running vet..."
	$(GO) vet ./...
	@echo "✅ Vet passed"

# Format code
fmt:
	@echo "Formatting code..."
	$(GO) fmt ./...
	@echo "✅ Formatted"

# Tidy dependencies
deps:
	@echo "Tidying dependencies..."
	$(GO) mod tidy
	@echo "✅ Dependencies tidied"

# Download dependencies
get:
	@echo "Getting dependencies..."
	$(GO) get ./...
	$(GO) mod download
	@echo "✅ Dependencies ready"

# Build documentation
docs:
	@echo "Building docs..."
	@go doc -all > docs/api.md 2>/dev/null || true
	@echo "✅ Docs generated"

# Full build cycle (deps, vet, test, build, install)
all: deps vet test build install

# Quick build (no tests)
quick: deps vet build

# Development workflow
dev: vet build run

# Shell completion
completion-bash:
	@echo "Generating bash completion..."
	./$(BINARY_NAME) --completion-bash > harness-completion.bash
	@echo "✅ Source with: source harness-completion.bash"

completion-zsh:
	@echo "Generating zsh completion..."
	./$(BINARY_NAME) --completion-zsh > harness-completion.zsh
	@echo "✅ Source with: source harness-completion.zsh"