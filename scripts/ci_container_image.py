#!/usr/bin/env python3
import argparse
import json
import pathlib
import tarfile

INDEX_MT = {
    "application/vnd.oci.image.index.v1+json",
    "application/vnd.docker.distribution.manifest.list.v2+json",
}
MANIFEST_MT = {
    "application/vnd.oci.image.manifest.v1+json",
    "application/vnd.docker.distribution.manifest.v2+json",
}


def mib(n: int) -> float:
    return n / 1024 / 1024


def load_json(tf: tarfile.TarFile, name: str):
    f = tf.extractfile(name)
    if f is None:
        raise RuntimeError(f"missing {name}")
    return json.load(f)


def blob_path(digest: str) -> str:
    algo, value = digest.split(":", 1)
    return f"blobs/{algo}/{value}"


def read_descriptor(tf, desc):
    return load_json(tf, blob_path(desc["digest"]))


def collect_manifests(tf, desc, inherited_platform=None):
    obj = read_descriptor(tf, desc)
    media = desc.get("mediaType") or obj.get("mediaType")
    if media in INDEX_MT or "manifests" in obj:
        out = []
        for child in obj.get("manifests", []):
            platform = child.get("platform") or inherited_platform
            # Ignore BuildKit attestation manifests: they normally have unknown/unknown
            # or explicit vnd.docker.reference.type annotations.
            ann = child.get("annotations", {})
            if ann.get("vnd.docker.reference.type") == "attestation-manifest":
                continue
            out.extend(collect_manifests(tf, child, platform))
        return out
    if media in MANIFEST_MT or "layers" in obj:
        return [(inherited_platform or {}, desc, obj)]
    raise RuntimeError(f"unsupported OCI descriptor media type: {media}")


def inspect_archive(path: pathlib.Path):
    with tarfile.open(path, "r:*") as tf:
        index = load_json(tf, "index.json")
        rows = []
        for top in index.get("manifests", []):
            rows.extend(collect_manifests(tf, top, top.get("platform")))
        result = {}
        for platform, desc, manifest in rows:
            os_name = platform.get("os")
            arch = platform.get("architecture")
            if os_name != "linux" or arch not in {"amd64", "arm64"}:
                continue
            key = f"{os_name}/{arch}"
            if key in result:
                raise RuntimeError(f"duplicate manifest for {key} in {path}")
            config = manifest.get("config", {})
            layers = manifest.get("layers", [])
            compressed = int(config.get("size", 0)) + sum(int(x.get("size", 0)) for x in layers)
            result[key] = {
                "manifest_digest": desc["digest"],
                "config_digest": config.get("digest", ""),
                "layer_count": len(layers),
                "compressed_bytes": compressed,
                "compressed_mib": round(mib(compressed), 3),
            }
        missing = {"linux/amd64", "linux/arm64"} - set(result)
        if missing:
            raise RuntimeError(f"{path} missing platforms: {sorted(missing)}")
        return result


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--server", required=True)
    ap.add_argument("--worker", required=True)
    ap.add_argument("--server-max-mib", type=float, default=35.0)
    ap.add_argument("--worker-max-mib", type=float, default=100.0)
    ap.add_argument("--output", required=True)
    args = ap.parse_args()

    images = {
        "server": inspect_archive(pathlib.Path(args.server)),
        "worker": inspect_archive(pathlib.Path(args.worker)),
    }
    budgets = {
        "server": args.server_max_mib,
        "worker": args.worker_max_mib,
    }
    failures = []
    for image, platforms in images.items():
        for platform, metrics in platforms.items():
            if metrics["compressed_mib"] > budgets[image]:
                failures.append(
                    f"{image} {platform}: {metrics['compressed_mib']:.3f} MiB "
                    f"> {budgets[image]:.3f} MiB"
                )

    report = {
        "schema_version": "container-image-report.v1",
        "required_platforms": ["linux/amd64", "linux/arm64"],
        "budgets_mib": budgets,
        "images": images,
        "status": "FAILED" if failures else "PASSED",
        "failures": failures,
    }
    out = pathlib.Path(args.output)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps(report, sort_keys=True))
    if failures:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
