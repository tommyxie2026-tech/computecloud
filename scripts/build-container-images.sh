#!/usr/bin/env bash
set -euo pipefail

version="${1:-}"
output="${2:-dist/container}"
server_max_mib="${SERVER_IMAGE_MAX_MIB:-35}"
worker_max_mib="${WORKER_IMAGE_MAX_MIB:-100}"

if [[ -z "$version" ]]; then
  echo "usage: $0 <version> [output-dir]" >&2
  exit 2
fi

mkdir -p "$output"
rm -f "$output/server.oci.tar" "$output/worker.oci.tar" "$output/report.json"

common=(
  --platform linux/amd64,linux/arm64
  --build-arg "VERSION=$version"
  --build-arg "REVISION=${GITHUB_SHA:-local}"
  --build-arg "SOURCE=https://github.com/tommyxie2026-tech/computecloud"
  --provenance=false
)

docker buildx build "${common[@]}" \
  --target server \
  --output "type=oci,dest=$output/server.oci.tar" \
  .

docker buildx build "${common[@]}" \
  --target worker \
  --output "type=oci,dest=$output/worker.oci.tar" \
  .

python3 scripts/ci_container_image.py \
  --server "$output/server.oci.tar" \
  --worker "$output/worker.oci.tar" \
  --server-max-mib "$server_max_mib" \
  --worker-max-mib "$worker_max_mib" \
  --output "$output/report.json"

# Native smoke checks use the host architecture and do not replace the manifest gate.
docker buildx build --platform linux/amd64 --target server \
  --build-arg "VERSION=$version" --build-arg "REVISION=${GITHUB_SHA:-local}" \
  --load -t computecloud-server:ci .
docker run --rm --entrypoint /computecloud computecloud-server:ci version | grep -F "computecloud $version "

docker buildx build --platform linux/amd64 --target worker \
  --build-arg "VERSION=$version" --build-arg "REVISION=${GITHUB_SHA:-local}" \
  --load -t computecloud-worker:ci .
docker run --rm --entrypoint /usr/local/bin/computecloud computecloud-worker:ci version | grep -F "computecloud $version "
docker run --rm --entrypoint git computecloud-worker:ci --version
