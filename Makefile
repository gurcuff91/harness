# Makefile for Harness
# A minimal AI agent harness built in pure Go.

.PHONY: build install clean test vet lint run help release release-push docs docs-generate docs-build docs-serve docs-clean docs-rpc docs-api

# Binary name and paths
BINARY_NAME=harness
BINARY_DIR=.
INSTALL_DIR=$(HOME)/go/bin
GO=go

# Build flags
VERSION=v0.133.0
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
	@echo "  make release   - Tag HEAD as \$$(VERSION) and push it (vet+test first)"
	@echo "  make docs       - EVERYTHING: regenerate the reference pages, then build the site"
	@echo "  make docs-build - Build only the site (docs/page → docs/page/dist)"
	@echo "  make docs-serve - Run "make docs", then serve it on http://localhost:8000"
	@echo "  make docs-clean - Remove the built site"
	@echo "  make docs-rpc   - Regenerate the RPC protocol reference page from its schema"
	@echo "  make docs-api   - Regenerate reference/http-api.md from the server's OpenAPI document"
	@echo ""
	@echo "Usage:"
	@echo "  make build          # builds ./harness"
	@echo "  make install        # builds and installs to ~/go/bin"
	@echo "  make run            # builds and runs ./harness"

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

# Build for multiple platforms — kept in sync with .github/workflows/release.yml,
# which builds the exact same 5 targets for GitHub Release assets.
build-all:
	@echo "Building for all platforms..."
	@mkdir -p dist
	GOOS=darwin GOARCH=amd64 $(GO) build $(LDFLAGS) -o dist/$(BINARY_NAME)-darwin-amd64 ./cmd/harness
	GOOS=darwin GOARCH=arm64 $(GO) build $(LDFLAGS) -o dist/$(BINARY_NAME)-darwin-arm64 ./cmd/harness
	GOOS=linux GOARCH=amd64 $(GO) build $(LDFLAGS) -o dist/$(BINARY_NAME)-linux-amd64 ./cmd/harness
	GOOS=linux GOARCH=arm64 $(GO) build $(LDFLAGS) -o dist/$(BINARY_NAME)-linux-arm64 ./cmd/harness
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

# Cut a release: tag HEAD as $(VERSION) and push the tag.
# Guards, in order — each aborts before anything is tagged/pushed:
#   1. working tree must be clean (no uncommitted changes)
#   2. VERSION must not already exist as a tag (never overwrite a release)
#   3. CHANGELOG.md must have an entry for this version
#   4. vet + tests must pass
# The annotated tag's message is this version's CHANGELOG section.
release: vet test
	@echo "Preparing release $(VERSION)..."
	@if [ -n "$$(git status --porcelain)" ]; then \
		echo "❌ Working tree is dirty — commit or stash first:"; \
		git status --short; \
		exit 1; \
	fi
	@if git rev-parse -q --verify "refs/tags/$(VERSION)" >/dev/null; then \
		echo "❌ Tag $(VERSION) already exists — bump VERSION in the Makefile first."; \
		exit 1; \
	fi
	@if ! grep -q "\[$(VERSION:v%=%)\]" CHANGELOG.md; then \
		echo "❌ No CHANGELOG.md entry for $(VERSION:v%=%) — document the release first."; \
		exit 1; \
	fi
	@echo "Tagging $(VERSION) at $$(git rev-parse --short HEAD)..."
	@awk '/^## \[$(VERSION:v%=%)\]/{f=1;print;next} /^## \[/{f=0} f' CHANGELOG.md \
		| git tag -a "$(VERSION)" -F -
	@echo "✅ Tagged $(VERSION). Push it with: make release-push"

# Push the release tag to origin (separate step so tagging can be reviewed first).
release-push:
	@if ! git rev-parse -q --verify "refs/tags/$(VERSION)" >/dev/null; then \
		echo "❌ Tag $(VERSION) doesn't exist yet — run 'make release' first."; \
		exit 1; \
	fi
	@echo "Pushing $(VERSION) to origin..."
	git push origin "$(VERSION)"
	@echo "✅ Released $(VERSION)"

# Build and run
run: build
	@echo "Running $(BINARY_NAME)..."
	@./$(BINARY_NAME)

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

# ── Documentation site (docs/page) ───────────────────────────────────────────
# Markdown in docs/page/content → static HTML in docs/page/dist. The generator
# (docs/page/build) uses only the standard library. DOCS_BASE is the URL prefix
# the site is served under: empty for a local server, /harness on GitHub Pages
# (the Pages workflow sets it).
DOCS_DIR=docs/page
DOCS_OUT=$(DOCS_DIR)/dist
DOCS_BASE=
DOCS_PORT=8000

docs-build:
	@echo "Building the documentation site..."
	cd $(DOCS_DIR)/build && $(GO) run . -content ../content -static ../static -out ../dist -version $(VERSION) -base "$(DOCS_BASE)"

# The whole documentation in one step: the two generated reference pages (the HTTP
# API from the server's OpenAPI, the RPC protocol from its schema) and then the
# site itself. This is what to run before committing a change to the code that
# the docs describe.
docs: docs-generate docs-build

# Regenerate every page that is generated from code.
docs-generate: docs-api docs-rpc

docs-serve: docs
	@echo "Serving http://localhost:$(DOCS_PORT) (Ctrl+C to stop)"
	cd $(DOCS_OUT) && python3 -m http.server $(DOCS_PORT)

docs-clean:
	rm -rf $(DOCS_OUT)

docs-api:
	$(GO) run ./docs/page/genapi

# The RPC reference page is generated from rpc.Schema() (the schema the TS SDK's
# types come from), so it cannot drift from the code.
docs-rpc:
	$(GO) run ./rpc/gen
	$(GO) run ./docs/page/genrpc
