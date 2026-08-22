# Everything runs through Docker so a build here matches a build on the Pi.
# Native Go targets exist for the fast inner loop only.

IMAGE    ?= obscura
VERSION  ?= dev
REGISTRY ?= ghcr.io/rverst

# 64-bit only, and deliberately so. Nothing here is 32-bit clean -- the EXIF
# decoder assumes a 64-bit int and does not even compile for GOARCH=arm -- and
# chasing that would buy an original Pi Zero, which was never the target. "Runs
# on a Pi" means a 64-bit OS on a Pi 4, 5 or Zero 2 W.
PLATFORMS ?= linux/arm64,linux/amd64

.PHONY: help
help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

# --- Docker (the authoritative path) ----------------------------------------

.PHONY: test
test: ## Run every check inside Docker: frontend tests, gofmt, vet, Go tests
	docker build --target test --progress=plain .

.PHONY: build
build: ## Build the runtime image for the local platform
	docker build --target runtime -t $(IMAGE):$(VERSION) --build-arg VERSION=$(VERSION) .

.PHONY: build-pi
build-pi: ## Cross-build the image for a Raspberry Pi (linux/arm64)
	docker build --target runtime --platform linux/arm64 \
		-t $(IMAGE):$(VERSION)-arm64 --build-arg VERSION=$(VERSION) .

.PHONY: build-multi
build-multi: builder ## Check that every published platform builds, without pushing
	@# Not loaded into the local daemon: it cannot hold a manifest list, and a
	@# multi-platform build has nowhere to go but a registry. cacheonly makes
	@# this a build check, which is what it is useful for before a release.
	docker buildx build --builder obscura --target runtime \
		--platform $(PLATFORMS) --build-arg VERSION=$(VERSION) \
		--output type=cacheonly .

.PHONY: builder
builder: ## Create the buildx builder that multi-arch builds need
	docker buildx inspect obscura >/dev/null 2>&1 \
		|| docker buildx create --name obscura --driver docker-container --bootstrap

.PHONY: push
push: builder ## Build and push a multi-arch image: make push VERSION=v0.1.0
	@# "dev" is the default, and a default must never become the tag everyone
	@# pulls. Publishing is the one step here that cannot be undone quietly.
	@test "$(VERSION)" != "dev" || \
		{ echo "set a version: make push VERSION=v0.1.0" >&2; exit 1; }
	docker buildx build --builder obscura --target runtime \
		--platform $(PLATFORMS) \
		-t $(REGISTRY)/$(IMAGE):$(VERSION) \
		-t $(REGISTRY)/$(IMAGE):latest \
		--build-arg VERSION=$(VERSION) \
		--push .
	@echo "pushed $(REGISTRY)/$(IMAGE):$(VERSION) for $(PLATFORMS)"

.PHONY: up
up: ## Start the stack via docker compose
	docker compose up --build -d
	@echo "obscura is on http://localhost:8080"

.PHONY: down
down: ## Stop the stack (the data volume is kept)
	docker compose down

.PHONY: logs
logs: ## Follow the container logs
	docker compose logs -f

.PHONY: backup
backup: ## Snapshot the data volume into ./backups (stops the stack for seconds)
	@mkdir -p backups
	deploy/backup.sh -f docker-compose.yml -s obscura backups

# --- Native (fast inner loop) ------------------------------------------------

.PHONY: web
web: ## Build the frontend into web/dist
	cd web && npm ci && npm run build

.PHONY: web-test
web-test: ## Run the frontend unit tests
	cd web && npm test

.PHONY: web-dev
web-dev: ## Vite dev server with hot reload, proxying /api to a local obscurad
	cd web && npm run dev

.PHONY: test-native
test-native: ## Run the Go tests directly on the host
	gofmt -l ./cmd ./internal ./web
	go vet ./...
	go test -count=1 ./...

.PHONY: run-native
run-native: ## Run the server directly on the host (needs `make web` first)
	go run ./cmd/obscurad -data ./data -dev
