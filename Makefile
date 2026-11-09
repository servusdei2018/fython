.PHONY: all build test lint format clean

export GOTOOLCHAIN ?= go1.27.0

BIN_DIR := bin
BINARY := $(BIN_DIR)/fython
SRC := ./cmd/fython

all: lint test build

build:
	@mkdir -p $(BIN_DIR)
	go build -o $(BINARY) $(SRC)

test:
	go test ./...

lint:
	golangci-lint run ./...

format:
	go fmt ./...
	@go tool goimports -local github.com/servusdei2018/fython -w cmd internal pkg

clean:
	rm -rf $(BIN_DIR)
