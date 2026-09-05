.PHONY: all build test test-fast race coverage fmt clean run env-check

BINARY_NAME=antigravity-bot-engine
BIN_DIR=bin
BUILD_PATH=$(BIN_DIR)/$(BINARY_NAME)

all: build

build:
	@mkdir -p $(BIN_DIR)
	go build -o $(BUILD_PATH) .
	@echo "✅ Build complete: $(BUILD_PATH)"

fmt:
	gofmt -s -w .
	@echo "🎨 Code formatted with gofmt."

test:
	go test -v -race ./...

test-fast:
	go test -v ./...

race: test

coverage:
	go test -v -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out
	@rm -f coverage.out

clean:
	@rm -rf $(BIN_DIR) build/ *.test coverage.out *.out
	@echo "🧹 Cleaned build artifacts."

env-check:
	@if [ "$${ALLOW_DOTENV:-0}" != "1" ] && [ -f .env ]; then \
		echo "❌ Security Alert: .env found in working directory in production mode! Move secrets to /etc/antigravity-bot/env or export ALLOW_DOTENV=1 for local development."; \
		exit 1; \
	fi
	@echo "🔒 Environment secrets check passed."

run: build
	ALLOW_DOTENV=1 ./$(BUILD_PATH)
