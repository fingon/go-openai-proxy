.PHONY: all
all: test install

CONTAINER_IMAGE_REPO ?= localhost/go-openai-proxy
CONTAINER_IMAGE_TAG ?= local
CONTAINER_IMAGE := $(CONTAINER_IMAGE_REPO):$(CONTAINER_IMAGE_TAG)
CONTAINER_NAME ?= go-openai-proxy-local-test
CONTAINER_PORT ?= 17132
CONTAINER_MODELS ?= gpt-5.2
CODEX_HOME ?= $(HOME)/.codex

.PHONY: install
install:
	go install ./cmd/...

.PHONY: lint
lint:
	go tool golangci-lint run --fix
	shellcheck scripts/*.sh

.PHONY: test
test:
	go test ./...

.PHONY: build
build:
	go build ./...

.PHONY: container-build-local
container-build-local:
	CONTAINER_IMAGE_REPO="$(CONTAINER_IMAGE_REPO)" \
	CONTAINER_IMAGE_TAG="$(CONTAINER_IMAGE_TAG)" \
	scripts/container-build-local.sh

.PHONY: container-test-local
container-test-local: container-build-local
	CONTAINER_IMAGE="$(CONTAINER_IMAGE)" \
	CONTAINER_NAME="$(CONTAINER_NAME)" \
	CONTAINER_PORT="$(CONTAINER_PORT)" \
	CONTAINER_MODELS="$(CONTAINER_MODELS)" \
	CODEX_HOME="$(CODEX_HOME)" \
	scripts/container-test-local.sh

.PHONY: check
check: lint test build
