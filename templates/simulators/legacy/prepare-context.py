#!/usr/bin/env python3
"""Extract a local legacy server into a new, isolated Docker build context."""

import argparse
import hashlib
import json
from pathlib import Path
import shutil
import subprocess


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source-image", default="tob213/rrs-sim:latest")
    parser.add_argument("--source-path", default="/opt/rrs/rcrs-server")
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    destination = args.output.resolve()
    if destination.exists():
        parser.error("output already exists; choose a new build context")
    # Inspect first: extraction must not implicitly pull a missing image.
    image = json.loads(subprocess.check_output(
        ["docker", "image", "inspect", args.source_image,
         "--format", '{"id":{{json .Id}},"digests":{{json .RepoDigests}}}'], text=True))
    destination.mkdir(parents=True)
    container = None
    try:
        container = subprocess.check_output(
            ["docker", "create", "--network", "none", image["id"]], text=True
        ).strip()
        subprocess.run(["docker", "cp", container + ":" + args.source_path,
                        str(destination / "rcrs-server")], check=True)
        shutil.copyfile(Path(__file__).with_name("java-wrapper.sh"),
                        destination / "java-wrapper.sh")
        checksums = {}
        for tree in ("jars", "lib"):
            for entry in sorted((destination / "rcrs-server" / tree).rglob("*")):
                if entry.is_file():
                    with entry.open("rb") as source:
                        checksums[str(entry.relative_to(destination / "rcrs-server"))] = (
                            hashlib.file_digest(source, "sha256").hexdigest())
        (destination / "source.json").write_text(json.dumps({
            "sourceImage": args.source_image, "sourceImageId": image["id"],
            "sourceDigests": image["digests"],
            "sourcePath": args.source_path, "sha256": checksums,
        }, indent=2) + "\n")
    except BaseException:
        shutil.rmtree(destination)
        raise
    finally:
        if container:
            subprocess.run(["docker", "rm", container], check=True,
                           stdout=subprocess.DEVNULL)
    print("Prepared", destination)


if __name__ == "__main__":
    main()
