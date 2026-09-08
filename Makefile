BINARY := klodmem
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build run test lint tidy

build:
	go build -ldflags="-X main.version=$(VERSION)" -o bin/$(BINARY) ./cmd/klodmem

run: build
	./bin/$(BINARY)

test:
	go test ./...

lint:
	go vet ./...
	golangci-lint run

tidy:
	go mod tidy
