.PHONY: build test lint fmt clean vet install

BIN_DIR ?= $(GOPATH)/bin

build:
	go build -o acpclaw ./cmd/acpclaw/
	go build -o mcp ./cmd/mcp/

install: build
	cp acpclaw $(BIN_DIR)/
	cp mcp $(BIN_DIR)/

test:
	go test -race -coverprofile=coverage.out ./...

lint:
	golangci-lint run ./...

fmt:
	gofmt -w .
	golangci-lint fmt

vet:
	go vet ./...

clean:
	rm -f acpclaw mcp coverage.out
