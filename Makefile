SHELL := /bin/sh

COMPOSE := docker compose -f infra/docker-compose.yml
DEV_COMPOSE := $(COMPOSE) -f infra/docker-compose.override.yml
ACCESS_CORE_DIR := services/access-core
ASSET_CORE_DIR := services/asset-core

BACKFILL_REFS_ARGS ?=
BACKFILL_METADATA_ARGS ?=
ASSET_DATABASE_URL ?= postgresql://asset_user:asset_password@localhost:5433/asset_db?sslmode=disable

.DEFAULT_GOAL := help

.PHONY: help setup dev up down restart build migrate test logs clean \
	backfill-refs backfill-metadata verify

help:
	@echo "seta-dam developer commands"
	@echo "  make setup              Install dependencies and build development images"
	@echo "  make dev                Run the hot-reloading development stack in the foreground"
	@echo "  make up                 Start the hot-reloading development stack in the background"
	@echo "  make down               Stop the stack"
	@echo "  make restart            Restart the development stack"
	@echo "  make build              Build production images"
	@echo "  make migrate            Apply both databases' Flyway migrations"
	@echo "  make test               Run both services' unit tests"
	@echo "  make verify             Run formatting, static checks, tests, and builds"
	@echo "  make logs               Follow logs from the development stack"
	@echo "  make clean              Stop the stack and remove its local volumes"

setup:
	@command -v node >/dev/null || { echo "Node.js is required" >&2; exit 1; }
	@command -v npm >/dev/null || { echo "npm is required" >&2; exit 1; }
	@command -v go >/dev/null || { echo "Go is required" >&2; exit 1; }
	@command -v docker >/dev/null || { echo "Docker is required" >&2; exit 1; }
	@test -f .env || { cp .env.example .env; echo "Created .env from .env.example"; }
	npm --prefix $(ACCESS_CORE_DIR) ci
	npm --prefix $(ACCESS_CORE_DIR) run db:generate
	cd $(ASSET_CORE_DIR) && go mod download
	$(DEV_COMPOSE) build
	@echo "Setup complete. Run 'make migrate' once, then 'make dev' (foreground) or 'make up' (background)."

dev:
	$(DEV_COMPOSE) up --build

up:
	$(DEV_COMPOSE) up -d --build

down:
	$(DEV_COMPOSE) down --remove-orphans

restart: down up

build:
	$(COMPOSE) build asset-core access-core

migrate:
	$(COMPOSE) up -d asset-db access-db
	$(COMPOSE) --profile migration run --rm flyway-asset
	$(COMPOSE) --profile migration run --rm flyway-access

test:
	npm --prefix $(ACCESS_CORE_DIR) test
	cd $(ASSET_CORE_DIR) && go test ./...

logs:
	$(DEV_COMPOSE) logs -f

clean:
	$(DEV_COMPOSE) down --volumes --remove-orphans

# US6 owns the ref-sync implementation. This stable target invokes its one-shot CLI once
# that module is present, while failing with an actionable message on branches without US6.
backfill-refs:
	@test -f $(ACCESS_CORE_DIR)/src/eventing/refsyncPublisher.ts || { \
		echo "ref-sync CLI is unavailable: implement US6/T077 first" >&2; exit 2; \
	}
	npm --prefix $(ACCESS_CORE_DIR) exec -- tsx src/eventing/refsyncPublisher.ts --once $(BACKFILL_REFS_ARGS)

# Example:
# make backfill-metadata BACKFILL_METADATA_FILE=.cache/open-images/sample.json \
#   BACKFILL_ORG_ID=<uuid> BACKFILL_USER_ID=<uuid>
backfill-metadata:
	@test -n "$(BACKFILL_METADATA_FILE)" || { echo "BACKFILL_METADATA_FILE is required" >&2; exit 2; }
	@test -n "$(BACKFILL_ORG_ID)" || { echo "BACKFILL_ORG_ID is required" >&2; exit 2; }
	@test -n "$(BACKFILL_USER_ID)" || { echo "BACKFILL_USER_ID is required" >&2; exit 2; }
	cd $(ASSET_CORE_DIR) && go run ./cmd/import-sample \
		-file "$(abspath $(BACKFILL_METADATA_FILE))" \
		-org-id "$(BACKFILL_ORG_ID)" \
		-user-id "$(BACKFILL_USER_ID)" \
		-database-url "$(ASSET_DATABASE_URL)" \
		$(BACKFILL_METADATA_ARGS)

verify:
	@echo "==> Access Core: generate database client"
	npm --prefix $(ACCESS_CORE_DIR) run db:generate
	@echo "==> Access Core: static checks"
	npm --prefix $(ACCESS_CORE_DIR) run lint
	@echo "==> Access Core: tests"
	npm --prefix $(ACCESS_CORE_DIR) test
	@echo "==> Access Core: production build"
	npm --prefix $(ACCESS_CORE_DIR) run build
	@echo "==> Asset Core: formatting"
	@test -z "$$(find $(ASSET_CORE_DIR) -type f -name '*.go' -not -path '*/tmp/*' -exec gofmt -l {} +)" || { \
		echo "Go files need gofmt:" >&2; \
		find $(ASSET_CORE_DIR) -type f -name '*.go' -not -path '*/tmp/*' -exec gofmt -l {} + >&2; \
		exit 1; \
	}
	@echo "==> Asset Core: vet"
	cd $(ASSET_CORE_DIR) && go vet ./...
	@echo "==> Asset Core: tests"
	cd $(ASSET_CORE_DIR) && go test ./...
	@echo "==> Asset Core: production build"
	cd $(ASSET_CORE_DIR) && go build ./...
	@echo "Verification passed for Access Core and Asset Core."
