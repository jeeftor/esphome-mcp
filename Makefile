BINARY   := esphome-mcp
VERSION  ?= dev
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(DATE)

.PHONY: build run serve install test tidy lint clean docker docker-run help

help:
	@printf 'Available targets:\n'
	@printf '  make build      Build the binary\n'
	@printf '  make run        Run the MCP server over stdio\n'
	@printf '  make serve      Run the MCP server over HTTP (port 3333)\n'
	@printf '  make install    Install the binary to $$GOBIN\n'
	@printf '  make test       Run tests\n'
	@printf '  make lint       Run go vet\n'
	@printf '  make docker     Build a local Docker image\n'
	@printf '  make docker-run Run the local Docker image\n'
	@printf '  make clean      Remove build artifacts\n'

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/esphome-mcp

run: build
	./$(BINARY)

serve: build
	./$(BINARY) serve --http-addr 0.0.0.0:3333

install: build
	go install -ldflags "$(LDFLAGS)" ./cmd/esphome-mcp

test:
	go test ./...

tidy:
	go mod tidy

lint:
	go vet ./...

docker: build
	docker build -t esphome-mcp:local .

docker-run:
	docker run --rm -p 3333:3333 \
	  -e ESPHOME_URL=$${ESPHOME_URL:-http://host.docker.internal:6052} \
	  esphome-mcp:local

clean:
	rm -f $(BINARY)
