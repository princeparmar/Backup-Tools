# Backup Tools Makefile

BINARY_NAME=backuptools
MAIN_FILE=cmd/main.go
GO=$(shell which go)
BUILD_FLAGS=-trimpath -ldflags="-s -w -X main.buildTime=$(shell date -u +'%Y-%m-%d_%H:%M:%S')"

# Function to check Go environment (GOBIN only)
check-go-env:
	@echo "🔍 Checking Go environment..."
	@if [ -z "$(shell $(GO) env GOBIN 2>/dev/null)" ]; then \
		echo "❌ Error: GOBIN is not set in Go environment!"; \
		echo "   Please set GOBIN by running:"; \
		echo "   export GOBIN=\$$HOME/go/bin"; \
		echo "   And add it to your ~/.bashrc or ~/.profile"; \
		exit 1; \
	fi
	@INSTALL_PATH=$(shell $(GO) env GOBIN); \
	if [ -z "$$INSTALL_PATH" ]; then \
		echo "❌ Error: Cannot determine installation path!"; \
		exit 1; \
	fi; \
	echo "✅ Go environment check passed: GOBIN=$$INSTALL_PATH"

# Function to check if .env contains POSTGRES_DSN
check-env-credentials:
	@echo "🔍 Checking environment configuration..."
	@if [ ! -f ".env" ]; then \
		echo "❌ Error: .env file not found!"; \
		echo "   Please create a .env file in the project directory."; \
		echo "   You can copy from .env.example if available."; \
		exit 1; \
	fi
	@if ! grep -q "^POSTGRES_DSN" .env; then \
		echo "❌ Error: POSTGRES_DSN not found in .env file!"; \
		echo "   Please add POSTGRES_DSN to your .env file."; \
		echo "   Example: POSTGRES_DSN=postgres://user:pass@localhost:5432/dbname"; \
		exit 1; \
	fi
	@echo "✅ Environment configuration check passed"

# Primary targets
.PHONY: build-all build-windows setup clean test deps info check-env-credentials check-go-env create-migration migrate-up migrate-down migrate-status

# Build for multiple platforms (optimized)
build-all: check-go-env check-env-credentials
	@echo "🌍 Building for multiple platforms (optimized)..."
	@GOOS=linux GOARCH=amd64 $(GO) build $(BUILD_FLAGS) -o $(BINARY_NAME)-linux-amd64 $(MAIN_FILE)
	@GOOS=darwin GOARCH=amd64 $(GO) build $(BUILD_FLAGS) -o $(BINARY_NAME)-darwin-amd64 $(MAIN_FILE)
	@GOOS=windows GOARCH=amd64 $(GO) build $(BUILD_FLAGS) -o $(BINARY_NAME)-windows-amd64.exe $(MAIN_FILE)
	@echo "✅ Cross-platform build complete!"

# Build for Windows only (optimized)
build-windows: check-go-env check-env-credentials
	@echo "🪟 Building for Windows (optimized)..."
	@GOOS=windows GOARCH=amd64 $(GO) build $(BUILD_FLAGS) -o $(BINARY_NAME)-windows-amd64.exe $(MAIN_FILE)
	@echo "✅ Windows build complete!"

# Fast build (skip some checks for development)
build-fast:
	@echo "⚡ Fast build..."
	@$(GO) build $(BUILD_FLAGS) -o $(BINARY_NAME) $(MAIN_FILE)
	@echo "✅ Fast build complete!"

# Setup/rebuild command (optimized)
setup: check-go-env
	@echo "🔧 Setting up $(BINARY_NAME)..."
	@INSTALL_PATH=$(shell $(GO) env GOBIN); \
	$(GO) build $(BUILD_FLAGS) -o $(BINARY_NAME) $(MAIN_FILE); \
	cp $(BINARY_NAME) "$$INSTALL_PATH"/; \
	if [ -f ".env" ]; then \
		cp .env "$$INSTALL_PATH"/.env; \
		echo "✅ Copied .env file"; \
	fi; \
	if [ -f "$$INSTALL_PATH/$(BINARY_NAME)" ]; then \
		echo "✅ Rebuilt and updated $(BINARY_NAME) at $$INSTALL_PATH"; \
	else \
		echo "✅ Successfully installed $(BINARY_NAME) to $$INSTALL_PATH"; \
		echo "💡 Make sure $$INSTALL_PATH is in your PATH environment variable"; \
	fi

# Clean build artifacts
clean:
	@echo "🧹 Cleaning up..."
	@$(GO) clean
	@rm -f $(BINARY_NAME) $(BINARY_NAME)-*
	@echo "✅ Clean complete!"

# Run tests
test:
	@echo "🧪 Running tests..."
	@$(GO) test -short ./...  # Use -short for faster tests

# Install dependencies
deps:
	@echo "📥 Checking dependencies..."
	@$(GO) mod download

# Remove installed binary
remove: check-go-env
	@INSTALL_PATH=$(shell $(GO) env GOBIN); \
	if [ ! -f "$$INSTALL_PATH/$(BINARY_NAME)" ]; then \
		echo "❌ Error: $(BINARY_NAME) not found at $$INSTALL_PATH/$(BINARY_NAME)"; \
		echo "   The application is not installed. Run 'make setup' first."; \
		exit 1; \
	fi; \
	rm -f "$$INSTALL_PATH/$(BINARY_NAME)"; \
	echo "✅ $(BINARY_NAME) uninstalled from $$INSTALL_PATH"

# Show project info
info: check-go-env
	@echo "📊 Project Information:"
	@echo "Binary: $(BINARY_NAME)"
	@echo "Go: $(shell $(GO) version)"
	@echo "GOBIN: $(shell $(GO) env GOBIN)"
	@INSTALL_PATH=$(shell $(GO) env GOBIN); \
	if [ -f "$$INSTALL_PATH/$(BINARY_NAME)" ]; then \
		echo "Status: ✅ Installed at $$INSTALL_PATH"; \
		ls -lh "$$INSTALL_PATH/$(BINARY_NAME)"; \
	else \
		echo "Status: ❌ Not installed"; \
		echo "Run 'make setup' to install"; \
	fi

# Development build (fastest, minimal checks)
dev:
	@echo "🚀 Development build..."
	@$(GO) build -o $(BINARY_NAME) $(MAIN_FILE)
	@echo "✅ Development build complete!"