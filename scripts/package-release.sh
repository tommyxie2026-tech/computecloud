#!/bin/sh
set -eu

version=${1:-}
output=${2:-dist/release}
go_command=${GO:-go}
host_os=$("$go_command" env GOHOSTOS)
host_arch=$("$go_command" env GOHOSTARCH)

if ! printf '%s\n' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$'; then
  echo "version must be semantic version without a leading v" >&2
  exit 2
fi
if [ -e "$output/SHA256SUMS" ]; then
  echo "$output/SHA256SUMS already exists" >&2
  exit 2
fi

mkdir -p "$output"
staging=$(mktemp -d)
cleanup() {
  rm -rf "$staging"
}
trap cleanup EXIT HUP INT TERM

for arch in amd64 arm64; do
  package="computecloud_${version}_linux_${arch}"
  archive="$output/${package}.tar.gz"
  temporary_archive="$staging/${package}.tar.gz"
  if [ -e "$archive" ]; then
    echo "$archive already exists" >&2
    exit 2
  fi
  root="$staging/$package"
  mkdir -p "$root"
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" "$go_command" build \
    -trimpath -buildvcs=true -ldflags "-s -w -X main.version=$version" \
    -o "$root/computecloud" ./cmd/computecloud
  "$go_command" version -m "$root/computecloud" | grep -q "GOARCH=$arch"
  if [ "$host_os" = linux ] && [ "$host_arch" = "$arch" ]; then
    case $("$root/computecloud" version) in
      "computecloud $version "*) ;;
      *) echo "embedded binary version check failed for $arch" >&2; exit 1 ;;
    esac
  fi
  cp README.md CHANGELOG.md "$root/"
  cp docs/deployment/production-v0.2.md "$root/DEPLOYMENT.md"
  cp -R examples "$root/"
  cp -R docs/examples/v0.2 "$root/examples/v0.2"
  tar --sort=name --mtime='UTC 1970-01-01' --owner=0 --group=0 --numeric-owner \
    -C "$staging" -czf "$temporary_archive" "$package"
  gzip -t "$temporary_archive"
  tar -tzf "$temporary_archive" >/dev/null
  mv "$temporary_archive" "$archive"
  rm -rf "$root"
done

(
  cd "$output"
  sha256sum computecloud_"$version"_linux_*.tar.gz > SHA256SUMS
  sha256sum -c SHA256SUMS
)
