# Everything runs through Docker so a build here matches a build on the Pi.
# Native Go targets exist for the fast inner loop only.

IMAGE   ?= obscura
VERSION ?= dev
# The Pi is arm64; linux/arm/v7 covers a Pi 3 or Zero 2 W.
PLATFORMS ?= linux/arm64,linux/amd64

.PHONY: help
help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

# --- Docker (the authoritative path) ----------------------------------------

.PHONY: test
test: ## Run gofmt, vet and the test suite inside Docker
	docker build --target test --progress=plain .

.PHONY: build
build: ## Build the runtime image for the local platform
	docker build --target runtime -t $(IMAGE):$(VERSION) --build-arg VERSION=$(VERSION) .

.PHONY: build-pi
build-pi: ## Cross-build the image for a Raspberry Pi (linux/arm64)
	docker build --target runtime --platform linux/arm64 \
		-t $(IMAGE):$(VERSION)-arm64 --build-arg VERSION=$(VERSION) .

.PHONY: build-multi
build-multi: ## Build a multi-arch image (needs a buildx builder)
	docker buildx build --target runtime --platform $(PLATFORMS) \
		-t $(IMAGE):$(VERSION) --build-arg VERSION=$(VERSION) .

.PHONY: up
up: ## Start the stack via docker compose
	docker compose up --build -d
	@echo "obscura is on http://127.0.0.1:8080"

.PHONY: down
down: ## Stop the stack (the data volume is kept)
	docker compose down

.PHONY: logs
logs: ## Follow the container logs
	docker compose logs -f

# --- Native (fast inner loop) ------------------------------------------------

.PHONY: test-native
test-native: ## Run the tests directly on the host
	gofmt -l ./cmd ./internal
	go vet ./...
	go test -count=1 ./...

.PHONY: run-native
run-native: ## Run the server directly on the host
	go run ./cmd/obscurad -data ./data -dev
