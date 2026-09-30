#!/bin/sh
set -eu

version=${1:?version required}
output=${2:-dist/mobile-release}
case "$version" in
  [0-9]*.[0-9]*.[0-9]*) ;;
  *) echo "version must be semantic version without leading v" >&2; exit 2 ;;
esac

rm -rf "$output"
mkdir -p "$output/staging/computecloud-control-mobile_$version"
root="$output/staging/computecloud-control-mobile_$version"
cp -R clients/control/src "$root/src"
cp clients/control/App.tsx clients/control/app.json clients/control/eas.json clients/control/package.json clients/control/package-lock.json "$root/"
cp -R clients/control/dist-ios clients/control/dist-android "$root/"
cp docs/deployment/mobile-control.md "$root/MOBILE-DEPLOYMENT.md"
cat > "$root/RELEASE-MANIFEST.json" <<EOF
{
  "schema_version": "computecloud-control-release.v1",
  "version": "$version",
  "artifact_kind": "expo-bundle-and-source",
  "signed_native_artifacts": false,
  "platforms": ["ios", "android"],
  "server_compatibility": "computecloud server 0.4.x",
  "deep_link_scheme": "computecloud",
  "credential_storage": "expo-secure-store"
}
EOF
tar --sort=name --mtime='UTC 1970-01-01' --owner=0 --group=0 --numeric-owner   -C "$output/staging" -czf "$output/computecloud-control-mobile_$version.tar.gz"   "computecloud-control-mobile_$version"
rm -rf "$output/staging"
(
  cd "$output"
  sha256sum "computecloud-control-mobile_$version.tar.gz" > SHA256SUMS
  sha256sum -c SHA256SUMS
)
