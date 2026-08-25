GO ?= go
BINARY ?= bin/heartbridge-server
IMAGE ?= heartbridge-matchmaking
PLATFORM ?= linux/amd64

export GOTOOLCHAIN := local

.PHONY: help
help:
	@echo "build        compile every package"
	@echo "binary       build the server binary into $(BINARY)"
	@echo "run          run the server with the local defaults"
	@echo "test         run the whole test suite once"
	@echo "race         run the whole test suite with the race detector"
	@echo "vet          run go vet over every package"
	@echo "fmt          rewrite every file with gofmt"
	@echo "fmt-check    fail when a file is not gofmt clean"
	@echo "verify       fmt-check, vet, build, test and race in one go"
	@echo "docker       build the container image for PLATFORM=$(PLATFORM)"

.PHONY: build
build:
	$(GO) build ./...

.PHONY: binary
binary:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="-s -w" -o $(BINARY) ./cmd/server

.PHONY: run
run:
	$(GO) run ./cmd/server

.PHONY: test
test:
	$(GO) test ./... -count=1

.PHONY: race
race:
	$(GO) test -race ./... -count=1

.PHONY: vet
vet:
	$(GO) vet ./...

.PHONY: fmt
fmt:
	gofmt -w .

.PHONY: fmt-check
fmt-check:
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt required for:"; echo "$$unformatted"; exit 1; \
	fi

.PHONY: verify
verify: fmt-check vet build test race

.PHONY: docker
docker:
	docker buildx build --platform $(PLATFORM) --load -t $(IMAGE):$(subst /,-,$(PLATFORM)) .
