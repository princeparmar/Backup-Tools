# Makefile for your Go project

# Variables
BINARY_NAME=backuptools
GO=go
MAIN_FILE=main.go

# Build the project
build:
	$(GO) build -o $(BINARY_NAME) $(MAIN_FILE)

# Run the server
run: build
	./$(BINARY_NAME)

# Clean up binary files
clean:
	go clean
	rm -f $(BINARY_NAME)

# Run tests
test:
	$(GO) test ./...

# Build for multiple platforms
build-all: build-linux build-darwin build-windows

build-linux:
	GOOS=linux GOARCH=amd64 $(GO) build -o $(BINARY_NAME)-linux-amd64 $(MAIN_FILE)

build-darwin:
	GOOS=darwin GOARCH=amd64 $(GO) build -o $(BINARY_NAME)-darwin-amd64 $(MAIN_FILE)

build-windows:
	GOOS=windows GOARCH=amd64 $(GO) build -o $(BINARY_NAME)-windows-amd64.exe $(MAIN_FILE)

.PHONY: build run clean test build-all
