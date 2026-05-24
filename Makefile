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

.PHONY: test
test:
	go test ./...

.PHONY: build
build:
	go build ./...

.PHONY: container-test-local
container-test-local:
	@test -f "$(CODEX_HOME)/auth.json" || (echo "missing $(CODEX_HOME)/auth.json; set CODEX_HOME to a directory containing auth.json" >&2; exit 1)
	@set -e; \
	tarball_base=$$(mktemp /tmp/go-openai-proxy-ko.XXXXXX); \
	tarball="$$tarball_base.tar"; \
	rm -f "$$tarball_base"; \
	container_name="$(CONTAINER_NAME)"; \
	cleanup() { podman rm -f "$$container_name" >/dev/null 2>&1 || true; rm -f "$$tarball_base" "$$tarball"; }; \
	trap cleanup EXIT; \
	podman rm -f "$$container_name" >/dev/null 2>&1 || true; \
	KO_DOCKER_REPO="$(CONTAINER_IMAGE_REPO)" ko build --bare --push=false --tags "$(CONTAINER_IMAGE_TAG)" --tarball "$$tarball" ./cmd/go-openai-proxy; \
	podman load --input "$$tarball"; \
	podman run --detach --name "$$container_name" \
		--publish "127.0.0.1:$(CONTAINER_PORT):17132" \
		--env CODEX_HOME=/codex \
		--env GO_OPENAI_PROXY_HOST=0.0.0.0 \
		--env GO_OPENAI_PROXY_MODELS="$(CONTAINER_MODELS)" \
		--env GO_OPENAI_PROXY_PORT=17132 \
		--volume "$(CODEX_HOME):/codex" \
		"$(CONTAINER_IMAGE)"; \
	for _ in 1 2 3 4 5 6 7 8 9 10; do \
		if curl -fsS "http://127.0.0.1:$(CONTAINER_PORT)/health" >/dev/null; then \
			curl -fsS "http://127.0.0.1:$(CONTAINER_PORT)/health"; \
			exit 0; \
		fi; \
		sleep 1; \
	done; \
	podman logs "$$container_name"; \
	exit 1

.PHONY: check
check: lint test build
