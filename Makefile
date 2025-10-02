# Backup Tools Makefile

BINARY_NAME=backuptools
MAIN_FILE=cmd/main.go
GO=$(shell which go)
INSTALL_PATH=$(shell $(GO) env GOBIN)

# Function to check if .env contains POSTGRES_DSN
check-env-credentials:
	@if [ ! -f ".env" ]; then \
		echo "❌ Error: .env file not found!"; \
		echo "Please create a .env file in the project directory."; \
		exit 1; \
	fi
	@if ! grep -q "^POSTGRES_DSN" .env; then \
		echo "❌ Error: POSTGRES_DSN not found in .env file!"; \
		echo "Please add POSTGRES_DSN to your .env file."; \
		exit 1; \
	fi

# Primary targets
.PHONY: build-all build-windows setup clean test deps info check-env-credentials


# Build for multiple platforms
build-all: check-env-credentials
	@echo "🌍 Building for multiple platforms..."
	@GOOS=linux GOARCH=amd64 $(GO) build -o $(BINARY_NAME)-linux-amd64 $(MAIN_FILE)
	@GOOS=darwin GOARCH=amd64 $(GO) build -o $(BINARY_NAME)-darwin-amd64 $(MAIN_FILE)
	@GOOS=windows GOARCH=amd64 $(GO) build -o $(BINARY_NAME)-windows-amd64.exe $(MAIN_FILE)
	@echo "✅ Cross-platform build complete!"

# Build for Windows only
build-windows: check-env-credentials
	@echo "🪟 Building for Windows..."
	@GOOS=windows GOARCH=amd64 $(GO) build -o $(BINARY_NAME)-windows-amd64.exe $(MAIN_FILE)
	@echo "✅ Windows build complete!"


# Setup/rebuild command (install if not present, rebuild if already installed)
setup: check-env-credentials
	@echo "🔧 Setting up $(BINARY_NAME)..."
	@if [ -f "$(INSTALL_PATH)/$(BINARY_NAME)" ]; then \
		echo "🔄 Binary found, rebuilding..."; \
		$(GO) build -o $(BINARY_NAME) $(MAIN_FILE); \
		cp $(BINARY_NAME) $(INSTALL_PATH)/; \
		cp .env $(INSTALL_PATH)/.env; \
		echo "✅ Rebuilt and updated $(BINARY_NAME)"; \
	else \
		echo "📦 Binary not found, installing..."; \
		$(GO) build -o $(BINARY_NAME) $(MAIN_FILE); \
		cp $(BINARY_NAME) $(INSTALL_PATH)/; \
		cp .env $(INSTALL_PATH)/.env; \
		echo "✅ Installed $(BINARY_NAME) to $(INSTALL_PATH)"; \
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
	@$(GO) test ./...

# Install dependencies
deps:
	@echo "📥 Checking dependencies..."
	@$(GO) mod tidy

# Remove installed binary
remove:
	@if [ ! -f "$(INSTALL_PATH)/$(BINARY_NAME)" ]; then \
		echo "❌ Error: $(BINARY_NAME) not found at $(INSTALL_PATH)/$(BINARY_NAME)"; \
		echo "The application is not installed. Run 'make setup' first."; \
		exit 1; \
	fi
	@rm -f $(INSTALL_PATH)/$(BINARY_NAME)
	@echo "✅ $(BINARY_NAME) uninstalled from $(INSTALL_PATH)"

# Show project info
info:
	@echo "📊 Project Information:"
	@echo "Binary: $(BINARY_NAME)"
	@echo "Go: $(shell $(GO) version)"
	@echo "Install Path: $(INSTALL_PATH)"
	@if [ -f "$(INSTALL_PATH)/$(BINARY_NAME)" ]; then \
		echo "Status: ✅ Installed"; \
		ls -lh "$(INSTALL_PATH)/$(BINARY_NAME)"; \
	else \
		echo "Status: ❌ Not installed"; \
	fi

# Show help
help:
	@echo "Available commands:"
	@echo "  setup        - Smart install/rebuild (installs if not present, rebuilds if installed)"
	@echo "  build-all    - Build for multiple platforms (Linux, macOS, Windows)"
	@echo "  build-windows- Build for Windows only"
	@echo "  test         - Run tests"
	@echo "  clean        - Remove build artifacts"
	@echo "  remove       - Remove installed binary"
	@echo "  info         - Show project information"
	@echo "  deps         - Install/update dependencies"