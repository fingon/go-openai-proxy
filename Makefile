.PHONY: all
all: test install

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

.PHONY: check
check: lint test build
