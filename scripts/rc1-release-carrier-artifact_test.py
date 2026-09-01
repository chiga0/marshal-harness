#!/usr/bin/env python3
"""Hostile regression tests for RC1 carrier admission and payload restore."""

from __future__ import annotations

import hashlib
import json
import os
import stat
import subprocess
import tarfile
import tempfile
import unittest
import zipfile
from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent
ADMIT = ROOT / "scripts/rc1-release-carrier-artifact.py"
EXTRACT = ROOT / "scripts/rc1-release-payload-extract.py"
HEAD = "a" * 40
RUN_ID = 12345
ARTIFACT_ID = 67890
BINARY = "marshal_1.0.0-rc1_darwin_arm64"
BASE_CONTENT = {
    "RELEASE-MANIFEST": b"manifest\n",
    "SHA256SUMS": b"sums\n",
    BINARY: b"Mach-O fixture bytes\n",
}


def zip_info(name: str, mode: int, file_type: int = stat.S_IFREG) -> zipfile.ZipInfo:
    info = zipfile.ZipInfo(name)
    info.create_system = 3
    info.external_attr = (file_type | mode) << 16
    info.compress_type = zipfile.ZIP_STORED
    return info


class RC1ReleaseCarrierArtifactTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory(prefix="rc1-release-carrier-test.")
        self.root = Path(self.temp.name).resolve()
        self.run_json = self.root / "run.json"
        self.artifact_json = self.root / "artifact.json"
        self.archive = self.root / "artifact.zip"
        self.run = {
            "id": RUN_ID,
            "path": ".github/workflows/rc1-canary.yml",
            "event": "workflow_dispatch",
            "status": "completed",
            "conclusion": "success",
            "head_sha": HEAD,
            "head_branch": "main",
            "repository": {"full_name": "chiga0/marshal-harness"},
        }

    def tearDown(self) -> None:
        self.temp.cleanup()

    def write_zip(
        self,
        *,
        extra: bool = False,
        drift: str | None = None,
        wrong_mode: str | None = None,
        symlink: str | None = None,
        duplicate: str | None = None,
    ) -> str:
        entries: list[tuple[str, bytes, int, int]] = []
        for prefix in ("dist", "rc1-carrier"):
            for name, content in BASE_CONTENT.items():
                path = f"{prefix}/{name}"
                actual = content + (b"drift" if drift == path else b"")
                mode = 0o644 if name != BINARY else 0o755
                if wrong_mode == path:
                    mode = 0o777
                entries.append((path, actual, mode, stat.S_IFLNK if symlink == path else stat.S_IFREG))
        entries.append(("rc1-carrier/RC1-CANARY-RECEIPT.json", b"{}\n", 0o644, stat.S_IFREG))
        if extra:
            entries.append(("unexpected", b"x", 0o644, stat.S_IFREG))
        if duplicate:
            entries.append((duplicate, b"duplicate", 0o644, stat.S_IFREG))
        with zipfile.ZipFile(self.archive, "w") as archive:
            for name, content, mode, file_type in entries:
                archive.writestr(zip_info(name, mode, file_type), content)
        return hashlib.sha256(self.archive.read_bytes()).hexdigest()

    def write_metadata(self, digest: str) -> None:
        self.run_json.write_text(json.dumps(self.run), encoding="utf-8")
        artifact = {
            "id": ARTIFACT_ID,
            "name": "rc1-carrier-99999",
            "expired": False,
            "digest": f"sha256:{digest}",
            "workflow_run": {"id": RUN_ID, "head_sha": HEAD},
        }
        self.artifact_json.write_text(json.dumps(artifact), encoding="utf-8")

    def invoke(self, digest: str, output_name: str = "out") -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [
                "python3", "-I", "-B", str(ADMIT), str(self.run_json),
                str(self.artifact_json), str(self.archive), str(self.root / output_name),
                str(RUN_ID), str(ARTIFACT_ID), digest, HEAD,
            ],
            text=True,
            capture_output=True,
            check=False,
        )

    def valid_admission(self) -> Path:
        digest = self.write_zip()
        self.write_metadata(digest)
        result = self.invoke(digest)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), "99999")
        output = self.root / "out"
        self.assertEqual({path.name for path in output.iterdir()}, set(BASE_CONTENT) | {"RC1-CANARY-RECEIPT.json"})
        return output

    def test_valid_carrier_and_payload(self) -> None:
        carrier = self.valid_admission()
        payload = self.root / "payload.tar"
        with tarfile.open(payload, "w:") as archive:
            for path in sorted(carrier.iterdir()):
                archive.add(path, arcname=path.name, recursive=False)
        destination = self.root / "payload-out"
        destination.mkdir()
        result = subprocess.run(
            ["python3", "-I", "-B", str(EXTRACT), str(payload), str(destination)],
            text=True, capture_output=True, check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual({path.name for path in destination.iterdir()}, {path.name for path in carrier.iterdir()})

    def test_wrong_finalize_branch_rejected(self) -> None:
        digest = self.write_zip()
        self.run["head_branch"] = "feat/forged"
        self.write_metadata(digest)
        self.assertNotEqual(self.invoke(digest).returncode, 0)

    def test_download_digest_drift_rejected(self) -> None:
        digest = self.write_zip()
        self.write_metadata("b" * 64)
        self.assertNotEqual(self.invoke("b" * 64).returncode, 0)
        self.assertNotEqual(digest, "b" * 64)

    def test_extra_member_rejected(self) -> None:
        digest = self.write_zip(extra=True)
        self.write_metadata(digest)
        self.assertNotEqual(self.invoke(digest).returncode, 0)

    def test_duplicate_member_rejected(self) -> None:
        digest = self.write_zip(duplicate="dist/RELEASE-MANIFEST")
        self.write_metadata(digest)
        self.assertNotEqual(self.invoke(digest).returncode, 0)

    def test_symlink_member_rejected(self) -> None:
        digest = self.write_zip(symlink=f"rc1-carrier/{BINARY}")
        self.write_metadata(digest)
        self.assertNotEqual(self.invoke(digest).returncode, 0)

    def test_wrong_mode_rejected(self) -> None:
        digest = self.write_zip(wrong_mode=f"rc1-carrier/{BINARY}")
        self.write_metadata(digest)
        self.assertNotEqual(self.invoke(digest).returncode, 0)

    def test_duplicate_copy_drift_rejected(self) -> None:
        digest = self.write_zip(drift="dist/RELEASE-MANIFEST")
        self.write_metadata(digest)
        self.assertNotEqual(self.invoke(digest).returncode, 0)

    def test_payload_extra_member_rejected(self) -> None:
        carrier = self.valid_admission()
        payload = self.root / "payload-extra.tar"
        extra = self.root / "extra"
        extra.write_text("x", encoding="utf-8")
        with tarfile.open(payload, "w:") as archive:
            for path in sorted(carrier.iterdir()):
                archive.add(path, arcname=path.name, recursive=False)
            archive.add(extra, arcname="extra", recursive=False)
        destination = self.root / "payload-extra-out"
        destination.mkdir()
        result = subprocess.run(
            ["python3", "-I", "-B", str(EXTRACT), str(payload), str(destination)],
            capture_output=True, check=False,
        )
        self.assertNotEqual(result.returncode, 0)


if __name__ == "__main__":
    unittest.main(verbosity=2)
