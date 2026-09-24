GO ?= go
VERSION ?= 0.2.0

.PHONY: build test race vet smoke capacity capacity-check release-package generate
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
capacity: build
	python3 scripts/capacity.py --binary bin/computecloud
capacity-check: build
	python3 -m unittest discover -s scripts -p 'test_*.py'
	python3 scripts/capacity.py --binary bin/computecloud --workers 1,2 --slots 1 --jobs 4 --output dist/capacity-check-$$(date +%s%N).json
release-package:
	GO=$(GO) scripts/package-release.sh $(VERSION) dist/release
generate:
	protoc -I . --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative api/agent/v1/runtime.proto
