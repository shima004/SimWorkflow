"""Offline checks for release helper selection and safe build arguments."""

import contextlib
import hashlib
import importlib.util
import io
import sys
import tarfile
import tempfile
import unittest
import zipfile
from pathlib import Path
from unittest.mock import patch

spec = importlib.util.spec_from_file_location(
    "build_release", Path(__file__).with_name("build-release.py")
)
build_release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(build_release)


class ReleaseBuildTest(unittest.TestCase):
    def test_all_archives_embed_the_selected_digest(self):
        helper = "ghcr.io/example/simworkflow-worker@sha256:" + "a" * 64
        calls = []

        def build(args, **kwargs):
            calls.append((args, kwargs["env"]))
            Path(args[args.index("-o") + 1]).write_bytes(b"test binary")

        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / "dist"
            argv = ["build-release.py", "--version", "v1.2.3", "--helper-image", helper, "--output", str(output)]
            with patch.object(sys, "argv", argv), patch.object(
                build_release.subprocess, "check_output", return_value="b" * 40
            ), patch.object(build_release.subprocess, "run", side_effect=build), contextlib.redirect_stdout(io.StringIO()):
                build_release.main()
            self.assertEqual(len(calls), 6)
            self.assertEqual({(env["GOOS"], env["GOARCH"]) for _, env in calls}, {
                (target, arch) for target in ("linux", "darwin", "windows") for arch in ("amd64", "arm64")
            })
            for args, env in calls:
                self.assertIn("-X main.defaultHelperImage=" + helper, args[args.index("-ldflags") + 1])
                self.assertEqual(env["CGO_ENABLED"], "0")
            for line in (output / "checksums.txt").read_text().splitlines():
                digest, name = line.split()
                archive = output / name
                self.assertEqual(hashlib.sha256(archive.read_bytes()).hexdigest(), digest)
                if name.endswith(".zip"):
                    with zipfile.ZipFile(archive) as stream:
                        members = stream.namelist()
                else:
                    with tarfile.open(archive) as stream:
                        members = stream.getnames()
                self.assertIn("INSTALL.md", members)
                self.assertTrue(set(members) <= {"simworkflow", "simworkflow.exe", "INSTALL.md", "LICENSE"})

    def test_invalid_helpers_fail_before_building(self):
        for helper in ["", "ghcr.io/example/worker:latest", "https://ghcr.io/example/worker@sha256:" + "a" * 64,
                       "ghcr.io/example/worker@sha256:" + "a" * 64 + " -X main.version=wrong"]:
            with self.subTest(helper=helper), patch.object(sys, "argv", ["build-release.py", "--version", "v1.2.3", "--helper-image", helper, "--output", "unused"]), patch.object(
                build_release.subprocess, "check_output"
            ) as command, contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit):
                    build_release.main()
                command.assert_not_called()


if __name__ == "__main__":
    unittest.main()
