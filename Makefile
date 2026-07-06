BINARY  := esphome-mcp
VERSION ?= dev
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build run install test clean tidy lint

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/esphome-mcp

run: build
	ESPHOME_URL=$(ESPHOME_URL) ./$(BINARY)

install: build
	go install -ldflags "$(LDFLAGS)" ./cmd/esphome-mcp

test:
	go test ./...

tidy:
	go mod tidy

lint:
	go vet ./...

clean:
	rm -f $(BINARY)
