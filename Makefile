# GhostFleet developer entry points. A plain `make` lists them.
#
# The version stamp baked into binaries and images comes from `git describe`
# (e.g. v1.0.0-4-g7211efb; "-dirty" when the tree has uncommitted changes).
# Override it per invocation: `make images VERSION=v1.2.3-rc1`.

MODULE  := github.com/PureStorage-OpenConnect/ghostfleet
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X $(MODULE)/internal/buildinfo.Version=$(VERSION)
COMPOSE := docker compose -f deploy/docker-compose.yml
API     ?= http://localhost:8080

# docker compose reads GHOSTFLEET_VERSION for the image build arg.
export GHOSTFLEET_VERSION := $(VERSION)

.DEFAULT_GOAL := help
.PHONY: help build build-go build-web test run vcsim web-dev clean \
        images docker up down restart ps logs status core tempos netboot \
        checkout deploy

help: ## List targets (this text)
	@awk 'BEGIN { FS = ":.*## " } \
	     /^##@/ { printf "\n%s\n", substr($$0, 5) } \
	     /^[a-zA-Z_-]+:.*## / { printf "  %-12s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
	@printf "\nVersion stamp: %s   (override: make <target> VERSION=x)\n" "$(VERSION)"

##@ Local build and test (laptop)

build: build-go build-web ## Build controller + agent into ./bin and the web UI into web/dist

build-go: ## Build the Go binaries only
	go build -ldflags '$(LDFLAGS)' -o bin/controller ./cmd/controller
	go build -ldflags '$(LDFLAGS)' -o bin/agent ./cmd/agent

build-web: ## Build the web UI only
	cd web && npm run build

test: ## go vet, go test, and the web type-check
	go vet ./...
	go test ./...
	cd web && npx vue-tsc -b

run: build-go ## Run the controller locally on :8080 (state in ./data)
	./bin/controller -web web/dist

vcsim: ## Start a local vSphere API simulator on https://127.0.0.1:8989/sdk (user/pass)
	go run ./tools/vcsim

web-dev: ## Vite dev server with hot reload, proxying /api to :8080
	cd web && npm run dev

clean: ## Remove build outputs
	rm -rf bin web/dist

##@ Container stack from source (dev controller VM)

images: ## Build all three images (core, netboot, tempos) from this checkout
	$(COMPOSE) build

docker: images ## Alias for images

up: ## Start the stack (builds missing images)
	$(COMPOSE) up -d

down: ## Stop the stack; state volumes are kept
	$(COMPOSE) down

restart: ## Recreate all containers from the current images (re-runs tempos)
	$(COMPOSE) up -d --force-recreate

ps: ## Container status
	$(COMPOSE) ps

logs: ## Follow logs; narrow with SVC=core|netboot|tempos
	$(COMPOSE) logs -f --tail=100 $(SVC)

status: ## Running version, temp OS result, container state
	@$(COMPOSE) ps --format 'table {{.Name}}\t{{.Status}}'
	@printf 'tempos:  '; $(COMPOSE) logs --no-log-prefix tempos 2>/dev/null | tail -1
	@printf 'version: '; curl -sf $(API)/api/v1/version || echo "API not reachable at $(API)"; echo

core: ## Rebuild only the controller image and recreate it (internal/, cmd/controller/, web/)
	$(COMPOSE) build core
	$(COMPOSE) up -d --no-deps core

tempos: ## Rebuild the temp OS image (cmd/agent/, deploy/tempos-init.sh), re-run it, restart core
	$(COMPOSE) build tempos
	$(COMPOSE) up -d --force-recreate tempos core

netboot: ## Rebuild the netboot image (iPXE, dnsmasq) and recreate it
	$(COMPOSE) build netboot
	$(COMPOSE) up -d --no-deps netboot

##@ Switching code (REF = branch/tag from origin, or pr/N from upstream if present; REMOTE= overrides)

checkout: ## Fetch and check out REF, e.g. make checkout REF=pr/6
	@test -n "$(REF)" || { echo "usage: make checkout REF=<branch|tag|pr/N> [REMOTE=<remote>]"; exit 2; }
	@sh tools/checkout-ref.sh "$(REF)" $(REMOTE)

deploy: checkout ## Check out REF, rebuild all images, recreate the stack, show status
	@$(MAKE) --no-print-directory images restart status
