BINARY := klodmem

.PHONY: build run test lint tidy

build:
	go build -o bin/$(BINARY) ./cmd/klodmem

run: build
	./bin/$(BINARY)

test:
	go test ./...

lint:
	go vet ./...
	golangci-lint run

tidy:
	go mod tidy
