# Copyright (c) 2026 Federico Pereira <lord.basex@gmail.com>
#
# signalhub: build, test, run locally and deploy to a server.
#   make help

SHELL := /bin/bash

# Local stack (deploy/.env, from deploy/.env.example).
ENV_FILE   ?= deploy/.env
COMPOSE    := docker compose --env-file $(ENV_FILE) -f deploy/docker-compose.yml

# Server: make deploy SERVER=root@my-server [SSH_KEY=~/.ssh/key.pem] [REMOTE_DIR=/opt/signalhub]
SERVER     ?=
SSH_KEY    ?=
REMOTE_DIR ?= /opt/signalhub
SSH        := ssh $(if $(SSH_KEY),-i $(SSH_KEY),)
REMOTE_COMPOSE := docker compose --env-file .env -f docker-compose.yml -f docker-compose.prod.yml

IMAGE      ?= signalhub
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: help test fmt vet vuln build docker up down restart logs ps health prod-up prod-down prod-ps prod-logs update deploy deploy-ps deploy-logs deploy-health check-server

help: ## this list
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

# --- code -------------------------------------------------------------------

test: fmt vet ## format check, vet and every test with the race detector
	go test -race ./...

fmt: ## fail if a Go file is not gofmt-formatted
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)

vet: ## go vet
	go vet ./...

vuln: ## known vulnerabilities in the code and its dependencies
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

build: ## the binary in bin/signal (static, no cgo)
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/signal ./cmd/signal

docker: ## the Docker image signalhub:<version> for this machine
	docker build -t $(IMAGE):$(VERSION) -t $(IMAGE):latest .

# --- local stack (on your computer) ------------------------------------------

$(ENV_FILE):
	@sh deploy/setup-env.sh local

up: $(ENV_FILE) ## on your computer: start signalhub + coturn (rebuilds signalhub)
	$(COMPOSE) up -d --build

down: $(ENV_FILE) ## stop the local stack
	$(COMPOSE) down

restart: down up ## stop and start the local stack

logs: $(ENV_FILE) ## follow the local logs
	$(COMPOSE) logs -f --tail=100

ps: $(ENV_FILE) ## local containers
	$(COMPOSE) ps

health: ## local health check
	@curl -fsS http://$$(grep -E '^SIGNAL_BIND=' $(ENV_FILE) | cut -d= -f2 || echo 127.0.0.1:8090)/healthz && echo

# --- on the server (in its clone of this repository, e.g. /opt/signalhub) ---

PROD_COMPOSE := $(COMPOSE) -f deploy/docker-compose.prod.yml

prod-up: ## on the server: build and start signalhub, coturn and Caddy (production)
	@sh deploy/setup-env.sh production
	$(PROD_COMPOSE) up -d --build
	$(PROD_COMPOSE) ps

prod-down: ## on the server: stop the production stack (volumes and certificates stay)
	$(PROD_COMPOSE) down

prod-ps: ## on the server: production containers
	$(PROD_COMPOSE) ps

prod-logs: ## on the server: follow the production logs
	$(PROD_COMPOSE) logs -f --tail=100

update: ## on the server: git pull and restart with the new code
	git pull --ff-only
	$(MAKE) prod-up

# --- from your computer to a server over SSH ----------------------------------

check-server:
	@test -n "$(SERVER)" || (echo "Set SERVER, e.g. make deploy SERVER=root@my-server" && exit 1)

deploy: check-server ## copy this commit to SERVER:REMOTE_DIR and restart the stack there
	@git diff --quiet HEAD -- || (echo "Commit your changes first: deploy sends the last commit" && exit 1)
	@echo "Deploying $(VERSION) to $(SERVER):$(REMOTE_DIR)"
	git archive --format=tar HEAD | $(SSH) $(SERVER) 'mkdir -p $(REMOTE_DIR) && tar -x -C $(REMOTE_DIR)'
	$(SSH) $(SERVER) 'cd $(REMOTE_DIR)/deploy && sh setup-env.sh production && $(REMOTE_COMPOSE) up -d --build && $(REMOTE_COMPOSE) ps'

deploy-ps: check-server ## containers on the server
	$(SSH) $(SERVER) 'cd $(REMOTE_DIR)/deploy && $(REMOTE_COMPOSE) ps'

deploy-logs: check-server ## last logs on the server
	$(SSH) $(SERVER) 'cd $(REMOTE_DIR)/deploy && $(REMOTE_COMPOSE) logs --tail=100'

deploy-health: check-server ## health check on the server
	$(SSH) $(SERVER) 'curl -fsS http://127.0.0.1:8090/healthz && echo'
