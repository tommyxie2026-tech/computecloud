GO ?= go
VERSION ?= 0.1.0

.PHONY: build test race vet smoke generate
build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '-s -w -X main.version=$(VERSION)' -o bin/computecloud ./cmd/computecloud
test:
	$(GO) test ./... -count=1
race:
	$(GO) test -race ./... -count=1
vet:
	$(GO) vet ./...
smoke: build
	python3 scripts/smoke.py --binary bin/computecloud
generate:
	protoc -I . --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative api/agent/v1/runtime.proto
