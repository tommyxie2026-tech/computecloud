GO ?= go
VERSION ?= 0.4.6

.PHONY: build test race vet smoke ci-flow ci-retry-flow ci-artifact-flow ci-workspace-flow ci-prepared-workspace-contract ci-prepared-workspace-recovery ci-long-run-flow ci-fair-flow ci-runtime-contract-flow ci-runtime-execution-flow ci-tool-contract-flow ci-environment-contract-flow ci-environment-execution-flow ci-agent-control-schema ci-agent-control-read ci-runtime-adapter-contract ci-agent-control-fencing ci-agent-control-negative ci-agent-control-dispatch ci-agent-control-approval ci-agent-control-resume ci-control-client-check ci-control-client-e2e ci-control-mobile-check ci-manual-retry-negative ci-container-image container-build capacity capacity-check release-package generate
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
ci-flow: build
	python3 scripts/ci_task_flow.py --binary bin/computecloud --output dist/ci-task-flow/report.json
ci-retry-flow: build
	python3 scripts/ci_retry_flow.py --binary bin/computecloud --output dist/ci-retry-flow/report.json
ci-artifact-flow:
	python3 scripts/ci_artifact_flow.py --output dist/ci-artifact-flow/report.json
ci-workspace-flow:
	python3 scripts/ci_workspace_flow.py --output dist/ci-workspace-flow/report.json
ci-prepared-workspace-contract:
	python3 scripts/ci_prepared_workspace_contract.py --output dist/ci-prepared-workspace-contract/report.json
ci-prepared-workspace-recovery:
	python3 scripts/ci_prepared_workspace_recovery.py --output dist/ci-prepared-workspace-recovery/report.json
ci-long-run-flow:
	python3 scripts/ci_long_run_flow.py --output dist/ci-long-run-flow/report.json
ci-fair-flow:
	python3 scripts/ci_fair_flow.py --output dist/ci-fair-flow/report.json
ci-runtime-contract-flow:
	python3 scripts/ci_runtime_contract.py --output dist/ci-runtime-contract/report.json
ci-runtime-execution-flow:
	python3 scripts/ci_runtime_execution.py --output dist/ci-runtime-execution/report.json
ci-tool-contract-flow:
	python3 scripts/ci_tool_contract.py --output dist/ci-tool-contract/report.json
ci-environment-contract-flow:
	python3 scripts/ci_environment_contract.py --output dist/ci-environment-contract/report.json
ci-environment-execution-flow:
	python3 scripts/ci_environment_execution.py --output dist/ci-environment-execution/report.json
ci-agent-control-schema:
	python3 scripts/ci_agent_control_schema.py --output dist/ci-agent-control-schema/report.json
ci-agent-control-read:
	python3 scripts/ci_agent_control_read.py --output dist/ci-agent-control-read/report.json
ci-runtime-adapter-contract:
	python3 scripts/ci_runtime_adapter_contract.py --output dist/ci-runtime-adapter-contract/report.json
ci-agent-control-fencing:
	python3 scripts/ci_agent_control_fencing.py --output dist/ci-agent-control-fencing/report.json
ci-agent-control-negative:
	python3 scripts/ci_agent_control_negative.py --output dist/ci-agent-control-negative/report.json
ci-agent-control-dispatch:
	python3 scripts/ci_agent_control_dispatch.py --output dist/ci-agent-control-dispatch/report.json
ci-agent-control-approval:
	python3 scripts/ci_agent_control_approval.py --output dist/ci-agent-control-approval/report.json
ci-agent-control-resume:
	python3 scripts/ci_agent_control_resume.py --output dist/ci-agent-control-resume/report.json
ci-control-client-check:
	python3 scripts/ci_control_client_check.py --output dist/control-client-check/report.json
ci-control-client-e2e: build
	python3 scripts/ci_control_client_e2e.py --binary bin/computecloud --output dist/control-client-e2e/report.json
ci-control-mobile-check:
	python3 scripts/ci_control_mobile_check.py --output dist/control-mobile-check/report.json
ci-manual-retry-negative:
	python3 scripts/ci_manual_retry_negative.py --output dist/ci-manual-retry-negative/report.json
ci-container-image:
	bash scripts/build-container-images.sh $(VERSION) dist/container
container-build: ci-container-image
capacity: build
	python3 scripts/capacity.py --binary bin/computecloud
capacity-check: build
	python3 -m unittest discover -s scripts -p 'test_*.py'
	python3 scripts/capacity.py --binary bin/computecloud --workers 1,2 --slots 1 --jobs 4 --output dist/capacity-check-$$(python3 -c 'import time; print(time.time_ns())').json
release-package:
	GO=$(GO) scripts/package-release.sh $(VERSION) dist/release
generate:
	protoc -I . --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative api/agent/v1/runtime.proto

.PHONY: ci-prepared-workspace-cache ci-prepared-workspace-benchmark
ci-prepared-workspace-cache:
	python3 scripts/ci_workspace_cache.py --output dist/prepared-workspace-cache/report.json
ci-prepared-workspace-benchmark:
	python3 scripts/ci_workspace_cache.py --benchmark --output dist/prepared-workspace-benchmark/report.json

.PHONY: ci-workspace-job-benchmark
ci-workspace-job-benchmark: build
	python3 scripts/ci_task_flow.py --binary bin/computecloud --workspace-benchmark --output dist/workspace-job-benchmark/report.json
