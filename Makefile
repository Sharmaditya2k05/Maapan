# Taraju monorepo — one command for every service.
#
#   make build        compile every Go service (workspace mode, fast)
#   make test         unit tests for every Go service
#   make vet          go vet + gofmt check for every Go service
#   make ci           what GitHub Actions / Cloud Build do: each service alone (GOWORK=off)
#   make up           start Postgres + Redis from sanchalan/docker-compose.yml
#   make run-local    run every Go service with `go run` (see sanchalan/scripts/run-local.sh)
#   make SVC=bahi test   limit any target to one service
#
# Per-service targets (db-create, docker, dev …) live in each service's own Makefile.

GO_SERVICES := mool dwaar pehchaan bahi kar aadesh lenden soochna aayaat bhandar nirmaan buddhi
SVC ?= $(GO_SERVICES)

.PHONY: help build test vet fmt tidy ci up down run-local dukaan-test

help:            ## Show targets
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

build:           ## go build every service
	@for s in $(SVC); do echo "== $$s: build"; (cd $$s && go build ./...) || exit 1; done

test:            ## go test every service
	@for s in $(SVC); do echo "== $$s: test"; (cd $$s && go test ./...) || exit 1; done

vet:             ## go vet + gofmt check every service
	@for s in $(SVC); do echo "== $$s: vet"; (cd $$s && go vet ./... && test -z "$$(gofmt -l .)") || exit 1; done

fmt:             ## gofmt every service
	@for s in $(SVC); do (cd $$s && gofmt -w .); done

tidy:            ## go mod tidy every service
	@for s in $(SVC); do echo "== $$s: tidy"; (cd $$s && go mod tidy) || exit 1; done

ci:              ## Build, vet and test each service in isolation (GOWORK=off), as CI does
	@for s in $(SVC); do echo "== $$s: ci"; (cd $$s && GOWORK=off go build ./... && GOWORK=off go vet ./... && GOWORK=off go test -race ./...) || exit 1; done

up:              ## Start Postgres + Redis
	cd sanchalan && docker compose up -d postgres redis

down:            ## Stop the local stack
	cd sanchalan && docker compose down

run-local:       ## Run every Go service locally (logs in sanchalan/logs)
	cd sanchalan && ./scripts/run-local.sh

dukaan-test:     ## Frontend lint + unit tests
	cd dukaan && npm run lint && npm test
