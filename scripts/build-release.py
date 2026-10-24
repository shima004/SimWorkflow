#!/usr/bin/env python3
"""Build the six release archives without reading local connection settings."""

import argparse
import hashlib
import os
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile
import zipfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", required=True, help="release tag, e.g. v0.1.0")
    parser.add_argument("--output", required=True, type=Path, help="empty output directory")
    args = parser.parse_args()
    if not re.fullmatch(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?", args.version):
        parser.error("version must be a v-prefixed semantic version")
    root = Path(__file__).resolve().parents[1]
    commit = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    if any(output.iterdir()):
        parser.error("output directory must be empty")
    checksums = []
    with tempfile.TemporaryDirectory(prefix="simworkflow-release-") as temporary:
        for target_os in ("linux", "darwin", "windows"):
            for arch in ("amd64", "arm64"):
                name = f"simworkflow_{args.version}_{target_os}_{arch}"
                directory = Path(temporary) / name
                directory.mkdir()
                binary = directory / ("simworkflow.exe" if target_os == "windows" else "simworkflow")
                env = {**os.environ, "CGO_ENABLED": "0", "GOOS": target_os, "GOARCH": arch}
                subprocess.run([
                    "go", "build", "-mod=readonly", "-trimpath", "-buildvcs=false",
                    "-ldflags", f"-s -w -X main.version={args.version} -X main.commit={commit}",
                    "-o", str(binary), "./cmd/simworkflow",
                ], cwd=root, env=env, check=True)
                (directory / "INSTALL.md").write_bytes((root / "docs/installation.md").read_bytes())
                # Use an explicit allowlist: never archive the workspace or .env.
                members = [binary, directory / "INSTALL.md"]
                if (root / "LICENSE").is_file():
                    (directory / "LICENSE").write_bytes((root / "LICENSE").read_bytes())
                    members.append(directory / "LICENSE")
                if target_os == "windows":
                    archive = output / f"{name}.zip"
                    with zipfile.ZipFile(archive, "w", compression=zipfile.ZIP_DEFLATED) as stream:
                        for member in members:
                            stream.write(member, member.name)
                else:
                    archive = output / f"{name}.tar.gz"
                    with tarfile.open(archive, "w:gz") as stream:
                        for member in members:
                            stream.add(member, arcname=member.name)
                checksums.append(f"{hashlib.sha256(archive.read_bytes()).hexdigest()}  {archive.name}\n")
                print(archive.name, flush=True)
    (output / "checksums.txt").write_text("".join(sorted(checksums)), encoding="utf-8")


if __name__ == "__main__":
    main()
