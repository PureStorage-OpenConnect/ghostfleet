MODULE  := github.com/PureStorage-OpenConnect/ghostfleet
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X $(MODULE)/internal/buildinfo.Version=$(VERSION)

.PHONY: build build-go build-web test run clean docker

build: build-go build-web

build-go:
	go build -ldflags '$(LDFLAGS)' -o bin/controller ./cmd/controller
	go build -ldflags '$(LDFLAGS)' -o bin/agent ./cmd/agent

build-web:
	cd web && npm run build

test:
	go vet ./...
	go test ./...
	cd web && npx vue-tsc -b

run: build-go
	./bin/controller -web web/dist

docker:
	GHOSTFLEET_VERSION=$(VERSION) docker compose -f deploy/docker-compose.yml build

clean:
	rm -rf bin web/dist
