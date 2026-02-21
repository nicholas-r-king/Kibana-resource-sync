BINARY := kibana-resource-sync
COMPOSE ?= docker-compose
GO_FILES := $(shell find . -type f -name '*.go')

.PHONY: build test test-race vet fmt fmt-check lint check run-dry compose-test compose-test-v9 compose-test-v8 compose-down

build:
	go build -o $(BINARY) ./cmd/kibana-resource-sync

test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w $(GO_FILES)

fmt-check:
	@files=$$(gofmt -l $(GO_FILES)); \
	if [ -n "$$files" ]; then \
		echo "The following files are not gofmt-formatted:"; \
		echo "$$files"; \
		exit 1; \
	fi

lint: fmt-check vet

check: lint test test-race

run-dry:
	go run ./cmd/kibana-resource-sync -config ./config.yaml -dry-run

compose-test:
	$(COMPOSE) up --build --abort-on-container-exit --exit-code-from verify verify

compose-test-v9:
	STACK_VERSION=9.3.0 $(COMPOSE) up --build --abort-on-container-exit --exit-code-from verify verify

compose-test-v8:
	STACK_VERSION=8.15.3 $(COMPOSE) up --build --abort-on-container-exit --exit-code-from verify verify

compose-down:
	$(COMPOSE) down -v --remove-orphans
