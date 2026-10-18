SERVICE := bahi
PORT    ?= 7002

.PHONY: run dev build test vet fmt tidy docker db-create db-reset help

help:            ## Show targets
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-10s %s\n", $$1, $$2}'

run:             ## Run the service (reads .env)
	go run ./cmd/server

dev:             ## Hot reload with air (go install github.com/air-verse/air@latest)
	air --build.cmd "go build -o bin/$(SERVICE) ./cmd/server" --build.bin "bin/$(SERVICE)"

build:           ## Build bin/bahi
	CGO_ENABLED=0 go build -trimpath -o bin/$(SERVICE) ./cmd/server

test:            ## Unit tests
	go test ./...

vet:             ## go vet + gofmt check
	go vet ./... && test -z "$$(gofmt -l .)"

fmt:             ## gofmt
	gofmt -w .

tidy:            ## go mod tidy
	go mod tidy

docker:          ## Build the container image (needs ../mool)
	docker buildx build --build-context mool=../mool -t $(SERVICE):local .

db-create:       ## Create the local database (postgres from sanchalan/docker-compose)
	docker exec -i taraju-postgres psql -U taraju -c "CREATE DATABASE taraju_$(SERVICE)" || true

db-reset:        ## Drop and recreate the local database
	docker exec -i taraju-postgres psql -U taraju -c "DROP DATABASE IF EXISTS taraju_$(SERVICE)"
	$(MAKE) db-create
