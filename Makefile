.PHONY: all build test race coverage clean run

BINARY_NAME=antigravity-bot-engine
BIN_DIR=bin
BUILD_PATH=$(BIN_DIR)/$(BINARY_NAME)

all: build

build:
	@mkdir -p $(BIN_DIR)
	go build -o $(BUILD_PATH) .
	@echo "✅ Build complete: $(BUILD_PATH)"

test:
	go test -v ./...

race:
	go test -v -race ./...

coverage:
	go test -v -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out
	@rm -f coverage.out

clean:
	@rm -rf $(BIN_DIR) build/ *.test coverage.out *.out
	@echo "🧹 Cleaned build artifacts."

run: build
	./$(BUILD_PATH)
