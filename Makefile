# NexoStream — common tasks (Linux/macOS; on Windows use the go/npm commands directly).
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X nexostream/internal/version.Version=$(VERSION) -X nexostream/internal/version.Commit=$(COMMIT)
BINS    := nexostream-api nexostream-controller nexostream-worker nexostream-dev

.PHONY: all build build-linux test test-short test-race test-pg lint fmt panel panel-dev dev infra-up infra-down clean

all: lint test build panel

build:
	@mkdir -p bin
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ ./cmd/...

build-linux:
	@mkdir -p bin
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ ./cmd/...

test:
	go test ./...

test-short:
	go test -short ./...

test-race:
	go test -race ./...

# Runs the store conformance suite against PostgreSQL (docker compose infra).
test-pg:
	NEXOSTREAM_TEST_DATABASE_URL=postgres://nexostream:nexostream-dev-only@127.0.0.1:5432/nexostream?sslmode=disable \
		go test -count=1 ./control-plane/store/postgres/

lint:
	gofmt -l . | (! grep .) || (echo "run make fmt" && exit 1)
	go vet ./...
	golangci-lint run ./...
	cd web-panel && npm run lint && npm run typecheck && npm run format:check

fmt:
	gofmt -w .
	cd web-panel && npm run format

panel:
	cd web-panel && npm ci && npm run build

panel-dev:
	cd web-panel && npm run dev

# All-in-one, in memory, no infrastructure. Add ARGS=-demo for test sources.
dev: panel
	go run ./cmd/nexostream-dev -panel web-panel/dist $(ARGS)

infra-up:
	docker compose -f deploy/docker-compose.dev.yml up -d

infra-down:
	docker compose -f deploy/docker-compose.dev.yml down

clean:
	rm -rf bin web-panel/dist var
