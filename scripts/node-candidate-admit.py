#!/usr/bin/env python3
"""Read-only GitHub candidate admission; never a release authorization.

Python only handles operator-side HTTP/archive transport. The unchanged Node
restore/verify and installed CLI consumer remain the actual product path.
No credentials, workflow mutation, source rebuild, tag or publication API.
"""
from __future__ import annotations

import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import re
import selectors
import shutil
import signal
import stat
import struct
import subprocess
import sys
import time
import zipfile

REPOSITORY = "chiga0/marshal-harness"
API_ROOT = f"repos/{REPOSITORY}"
WORKFLOW = ".github/workflows/node-team.yml"
NODE_VERSION = "24.15.0"
MAX_JSON = 1 << 20
MAX_ARCHIVE = 20 << 20
MAX_FILE = 2 << 20
MAX_TOTAL = 16 << 20
MAX_MANIFEST = 65536
MAX_OUTPUT = 256 << 10
# 与受信 packages/task-distribution/index.mjs 的 inspect 静态资产规则一致。
# Python仅约束ZIP传输；落盘后仍必须通过原Node restore/verify，不执行下载代码。
UI_ROOT = "apps/task-web/dist/"
UI_MAX_FILES = 512
UI_NAME = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}\Z")
UI_EXTENSIONS = frozenset(("html", "js", "css", "json", "map", "svg", "png", "jpg", "jpeg", "ico", "webmanifest", "txt", "woff", "woff2"))
SHA = re.compile(r"[a-f0-9]{40}\Z")
DIGEST = re.compile(r"sha256:[a-f0-9]{64}\Z")
DECIMAL = re.compile(r"[1-9][0-9]{0,15}\Z")
JOBS = frozenset({"Freeze one Node candidate"} | {
    f"{name} ({platform}, Node {version})"
    for name in ("Node team", "Consume the same Node candidate")
    for platform in ("ubuntu-latest", "macos-latest")
    for version in ("22.22.1", "24.15.0")
} | {
    f"task-web (typecheck, build, test + e2e, {platform}, Node {version})"
    for platform in ("ubuntu-latest", "macos-latest")
    for version in ("22.22.1", "24.15.0")
})
VALIDATORS = (
    "packages/task-store/runtime.mjs",
    "packages/task-distribution/index.mjs", "packages/task-distribution/main.mjs",
    "packages/task-distribution/candidate-consumer.mjs", "packages/task-distribution/installed-team.fixture.mjs",
    "packages/task-distribution/team-service.fixture.mjs", "packages/task-team-integration/agent.fixture.mjs",
    "packages/task-team-integration/scenario.fixture.mjs",
)
SAFE_ENV = ("PATH", "HOME", "LANG", "LC_ALL", "LC_CTYPE", "TZ", "TMPDIR")


class CandidateError(Exception):
    pass


def need(condition, code):
    if not condition:
        raise CandidateError(code)


def sha(raw):
    return "sha256:" + hashlib.sha256(raw).hexdigest()


def integer(value):
    return type(value) is int and 0 < value <= 9007199254740991


def no_duplicates(pairs):
    result = {}
    for key, value in pairs:
        need(key not in result, "duplicate_json_key")
        result[key] = value
    return result


def parse(raw):
    need(type(raw) is bytes and 0 < len(raw) <= MAX_JSON and not raw.startswith(b"\xef\xbb\xbf"), "invalid_json")
    try:
        value = json.loads(raw.decode("utf-8", "strict"), object_pairs_hook=no_duplicates,
                           parse_constant=lambda _: (_ for _ in ()).throw(CandidateError("invalid_json")))
    except (ValueError, UnicodeError, RecursionError):
        raise CandidateError("invalid_json") from None
    need(type(value) is dict, "invalid_json")
    return value


def clean_env(credentials=False):
    result = {key: os.environ[key] for key in SAFE_ENV if key in os.environ}
    if credentials:
        result.update({key: os.environ[key] for key in ("GH_TOKEN", "GITHUB_TOKEN") if key in os.environ})
        result.update(GH_PROMPT_DISABLED="1", GH_PAGER="cat")
    return result


def command(argv, *, timeout=30, maximum=MAX_JSON, env=None, cwd=None, owned_group=False):
    """Bound output/time; stderr is never echoed. Only stop the child we created.

    The consumer runs in its own group. On a hard failure, stop that *live*
    original group, not a replayed PID; a timeout never becomes cleanup proof.
    """
    process = subprocess.Popen(argv, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                               stderr=subprocess.DEVNULL, env=env or clean_env(), cwd=cwd,
                               start_new_session=owned_group)
    output = bytearray()
    end = time.monotonic() + timeout
    reader = selectors.DefaultSelector()
    reader.register(process.stdout, selectors.EVENT_READ)
    try:
        while reader.get_map():
            remaining = end - time.monotonic()
            need(remaining > 0, "command_timeout")
            for key, _ in reader.select(min(remaining, 0.25)):
                chunk = os.read(key.fd, min(65536, maximum + 1 - len(output)))
                if not chunk:
                    reader.unregister(key.fd)
                else:
                    output.extend(chunk)
                    need(len(output) <= maximum, "command_output_limit")
        need(process.wait(timeout=max(0.01, end - time.monotonic())) == 0, "command_failed")
        return bytes(output)
    except subprocess.TimeoutExpired:
        error = CandidateError("command_timeout")
        error.output = bytes(output[:maximum])
        raise error from None
    except CandidateError as error:
        error.output = bytes(output[:maximum])
        raise
    finally:
        reader.close()
        if process.poll() is None:
            if owned_group:
                os.killpg(process.pid, signal.SIGKILL)
            else:
                process.kill()
        process.wait(timeout=10)
        process.stdout.close()


def options(source_head, run_id, attempt, artifact_id, archive_digest, manifest_digest):
    need(type(source_head) is str and SHA.fullmatch(source_head), "invalid_source_head")
    need(all(type(value) is str and DECIMAL.fullmatch(value) for value in (run_id, attempt, artifact_id)), "invalid_id")
    need(all(type(value) is str and DIGEST.fullmatch(value) for value in (archive_digest, manifest_digest)), "invalid_digest")
    need(all(integer(int(value)) for value in (run_id, attempt, artifact_id)), "invalid_id")
    return dict(sourceHead=source_head, runId=int(run_id), attempt=int(attempt), artifactId=int(artifact_id),
                archiveDigest=archive_digest, manifestDigest=manifest_digest)


class GitHub:
    def __init__(self, executable):
        need(os.path.isabs(executable) and os.access(executable, os.X_OK), "invalid_github_cli")
        self.executable = executable
        self.deadline = time.monotonic() + 300

    def raw(self, endpoint, maximum=MAX_JSON):
        need(endpoint.startswith(API_ROOT + "/"), "invalid_api_endpoint")
        remaining = self.deadline - time.monotonic()
        need(remaining > 0, "admission_deadline")
        return command([self.executable, "api", "--hostname", "github.com", endpoint],
                       maximum=maximum, timeout=min(30, remaining), env=clean_env(credentials=True))

    def get(self, endpoint):
        return parse(self.raw(endpoint))


def validate_run(run, expected, workflow_id):
    need(all(run.get(key) == value for key, value in {
        "id": expected["runId"], "run_attempt": expected["attempt"], "workflow_id": workflow_id,
        "head_sha": expected["sourceHead"], "head_branch": "main", "event": "push", "path": WORKFLOW,
        "status": "completed", "conclusion": "success"}.items()), "run_binding_mismatch")
    need(all(integer(run.get(key)) for key in ("id", "run_attempt", "workflow_id")), "invalid_run_id")
    for key in ("repository", "head_repository"):
        need(type(run.get(key)) is dict and run[key].get("full_name") == REPOSITORY and integer(run[key].get("id")), "foreign_repository")
    need(run["repository"]["id"] == run["head_repository"]["id"], "foreign_repository")


def github_snapshot(api, expected):
    """Exact attempt, closed job set, complete page, then reread current run.

    No caller-provided metadata file is an authority input to the CLI. A rerun
    detected before/after download or consumption rejects the observation.
    """
    workflow = api.get(API_ROOT + "/actions/workflows/node-team.yml")
    need(integer(workflow.get("id")) and workflow.get("path") == WORKFLOW, "workflow_mismatch")
    run_path = API_ROOT + f'/actions/runs/{expected["runId"]}'
    run = api.get(run_path)
    validate_run(run, expected, workflow["id"])
    attempt = api.get(run_path + f'/attempts/{expected["attempt"]}')
    validate_run(attempt, expected, workflow["id"])
    page_path = run_path + f'/attempts/{expected["attempt"]}/jobs?per_page=100&page='
    first, tail = api.get(page_path + "1"), api.get(page_path + "2")
    jobs = first.get("jobs")
    need(type(first.get("total_count")) is int and first["total_count"] == len(JOBS) and type(jobs) is list and len(jobs) == len(JOBS) and
         type(tail.get("total_count")) is int and tail["total_count"] == len(JOBS) and tail.get("jobs") == [], "incomplete_jobs")
    need(all(type(job) is dict for job in jobs), "invalid_jobs")
    need({job.get("name") for job in jobs} == JOBS and len({job.get("id") for job in jobs}) == len(JOBS), "invalid_jobs")
    for job in jobs:
        need(integer(job.get("id")) and integer(job.get("run_id")) and integer(job.get("run_attempt")) and
             all(job.get(key) == value for key, value in {"run_id": expected["runId"], "run_attempt": expected["attempt"],
                 "head_sha": expected["sourceHead"], "status": "completed", "conclusion": "success"}.items()), "job_binding_mismatch")
    artifact = api.get(API_ROOT + f'/actions/artifacts/{expected["artifactId"]}')
    need(integer(artifact.get("id")) and all(artifact.get(key) == value for key, value in {
        "id": expected["artifactId"], "name": f'node-candidate-{expected["sourceHead"]}-{expected["attempt"]}',
        "digest": expected["archiveDigest"]}.items()) and artifact.get("expired") is False, "artifact_binding_mismatch")
    need(integer(artifact.get("size_in_bytes")) and artifact["size_in_bytes"] <= MAX_ARCHIVE, "archive_size_limit")
    origin = artifact.get("workflow_run")
    need(type(origin) is dict and origin.get("id") == expected["runId"] and integer(origin.get("id")) and
         origin.get("head_sha") == expected["sourceHead"] and origin.get("head_branch") == "main" and
         origin.get("repository_id") == run["repository"]["id"] and
         origin.get("head_repository_id") == run["repository"]["id"], "artifact_origin_mismatch")
    latest = api.get(run_path)
    validate_run(latest, expected, workflow["id"])
    return {"workflowId": workflow["id"], "jobs": sorted(job["id"] for job in jobs), "archiveBytes": artifact["size_in_bytes"]}


def canonical_directory(value, private=False):
    need(type(value) is str and os.path.isabs(value) and os.path.normpath(value) == value and os.path.realpath(value) == value, "unsafe_directory")
    current = Path(value)
    for parent in (*reversed(current.parents), current):
        need(stat.S_ISDIR(os.lstat(parent).st_mode), "unsafe_directory")
    info = os.lstat(value)
    if private:
        need(info.st_uid == os.getuid() and stat.S_IMODE(info.st_mode) == 0o700, "unsafe_private_directory")


def read_regular(filename, maximum):
    canonical_directory(str(Path(filename).parent))
    before = os.lstat(filename)
    need(stat.S_ISREG(before.st_mode) and before.st_nlink == 1 and before.st_size <= maximum, "unsafe_file")
    fd = os.open(filename, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    try:
        held = os.fstat(fd)
        need((held.st_dev, held.st_ino) == (before.st_dev, before.st_ino), "file_drift")
        content = bytearray()
        while len(content) <= maximum:
            chunk = os.read(fd, min(65536, maximum + 1 - len(content)))
            if not chunk:
                break
            content.extend(chunk)
        after = os.fstat(fd)
        need(len(content) == held.st_size and len(content) <= maximum and
             (held.st_size, held.st_mtime_ns, held.st_ctime_ns) == (after.st_size, after.st_mtime_ns, after.st_ctime_ns), "file_drift")
        return bytes(content)
    finally:
        os.close(fd)


def trusted_source(root, source_head, node):
    canonical_directory(root)
    need(os.path.isabs(node) and os.access(node, os.X_OK), "invalid_node")
    version = command([node, "--version"]).strip()
    need(re.fullmatch(rb"v\d+\.\d+\.\d+", version) is not None and int(version.split(b".")[0][1:]) >= 22, "unsupported_node")
    need(command(["git", "-C", root, "rev-parse", "--show-toplevel"]).decode().strip() == root and
         command(["git", "-C", root, "rev-parse", "HEAD"]).decode().strip() == source_head, "validator_source_mismatch")
    for name in VALIDATORS:
        need(read_regular(os.path.join(root, name), MAX_FILE) == command(["git", "-C", root, "show", source_head + ":" + name], maximum=MAX_FILE),
             "validator_source_drift")
    bridge = str(Path(__file__).with_name("node-candidate-consume.mjs"))
    inventory = parse(command([node, bridge, "inventory", root]))
    files = inventory.get("files")
    need(inventory.get("node") == NODE_VERSION and type(files) is list and 1 <= len(files) <= 128 and
         files == sorted(set(files)) and all(type(name) is str and re.fullmatch(r"packages/(?:[A-Za-z0-9_-]+/)*[A-Za-z0-9_.-]+", name) for name in files),
         "invalid_inventory")
    return files


def ui_path(name):
    if type(name) is not str or not name.startswith(UI_ROOT):
        return False
    relative = name[len(UI_ROOT):]
    return all(UI_NAME.fullmatch(part) for part in relative.split("/")) and \
        "." in relative and relative.rsplit(".", 1)[1].lower() in UI_EXTENSIONS


def validate_archive(raw, expected, files, *, require_ui=False):
    need(type(raw) is bytes and 0 < len(raw) <= MAX_ARCHIVE and sha(raw) == expected["archiveDigest"], "archive_digest_mismatch")
    # Bound member count BEFORE ZipFile allocates a central-directory object per
    # entry. Reject ZIP64, multi-disk, trailers and unexplained archive comments.
    need(len(raw) >= 22 and raw[-22:-18] == b"PK\x05\x06", "unsupported_zip")
    disk, start_disk, disk_count, count, central_bytes, offset, comment = struct.unpack_from("<4H2IH", raw, len(raw) - 18)
    need(disk == start_disk == 0 and disk_count == count and len(files) + 1 <= count <= len(files) + UI_MAX_FILES + 1 and comment == 0 and
         central_bytes < MAX_MANIFEST and offset + central_bytes == len(raw) - 22, "unsupported_zip")
    wanted = set(files) | {"manifest.json"}
    contents = {}
    try:
        with zipfile.ZipFile(io.BytesIO(raw)) as archive:
            members = archive.infolist()
            need(len(members) == count, "zip_inventory_mismatch")
            total = 0
            for member in members:
                name = member.filename
                need(name == member.orig_filename and (name in wanted or ui_path(name)) and name not in contents and not member.is_dir(), "zip_inventory_mismatch")
                kind = stat.S_IFMT(member.external_attr >> 16)
                maximum = MAX_MANIFEST if name == "manifest.json" else MAX_FILE
                need(kind in (0, stat.S_IFREG) and not member.flag_bits & ~(0x8 | 0x800) and not member.extra and not member.comment and
                     member.compress_type in (zipfile.ZIP_STORED, zipfile.ZIP_DEFLATED) and
                     0 <= member.file_size <= maximum and 0 <= member.compress_size <= MAX_ARCHIVE, "unsafe_zip_member")
                total += member.file_size
                need(total <= MAX_TOTAL + MAX_MANIFEST, "zip_expansion_limit")
                with archive.open(member) as stream:
                    value = stream.read(maximum + 1)
                    need(len(value) == member.file_size and stream.read(1) == b"", "zip_size_mismatch")
                contents[name] = value
    except (zipfile.BadZipFile, NotImplementedError, RuntimeError, OSError, ValueError):
        raise CandidateError("invalid_zip") from None
    need("manifest.json" in contents and sha(contents["manifest.json"]) == expected["manifestDigest"], "manifest_digest_mismatch")
    manifest = parse(contents["manifest.json"])
    entries = manifest.get("files")
    need(type(entries) is list and len(files) <= len(entries) <= len(files) + UI_MAX_FILES, "invalid_manifest")
    ui_entries = entries[len(files):]
    need(all(type(entry) is dict and ui_path(entry.get("path")) for entry in ui_entries), "invalid_manifest")
    ui_files = [entry["path"] for entry in ui_entries]
    need(ui_files == sorted(set(ui_files)), "invalid_manifest")
    need((not ui_files and not require_ui) or UI_ROOT + "index.html" in ui_files, "missing_ui_entry")
    all_files = files + ui_files
    need(set(contents) == set(all_files) | {"manifest.json"}, "zip_inventory_mismatch")
    for name, entry in zip(all_files, entries):
        need(type(entry) is dict and list(entry) == ["path", "digest", "bytes"] and entry["path"] == name and
             type(entry["bytes"]) is int and entry["bytes"] == len(contents[name]) and entry["digest"] == sha(contents[name]), "manifest_file_mismatch")
    canonical = {"format": "marshal-node-script-package/v1", "sourceHead": expected["sourceHead"], "node": NODE_VERSION,
                 "platforms": ["darwin-arm64", "linux-x64"], "entrypoint": "packages/task-service/main.mjs", "files": entries}
    need((json.dumps(canonical, ensure_ascii=False, indent=2) + "\n").encode() == contents["manifest.json"], "invalid_manifest")
    need(sum(len(contents[name]) for name in all_files) <= MAX_TOTAL, "package_size_limit")
    return contents


class PrivateOutput:
    """Fresh exclusive output; preserve partial failures. No implicit reset/GC."""
    def __init__(self, target):
        need(os.path.isabs(target) and os.path.normpath(target) == target, "invalid_target")
        self.target = target
        parent = str(Path(target).parent)
        canonical_directory(parent, private=True)
        self.held = {}
        self.hold(parent)
        try:
            self.mkdir(target)
        except BaseException:
            self.close()
            raise

    def hold(self, name):
        self.held[name] = os.open(name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        self.check()

    def check(self):
        for name, fd in self.held.items():
            current, held = os.lstat(name), os.fstat(fd)
            need(stat.S_ISDIR(held.st_mode) and held.st_uid == os.getuid() and stat.S_IMODE(held.st_mode) == 0o700 and
                 (held.st_dev, held.st_ino) == (current.st_dev, current.st_ino) and os.path.realpath(name) == name, "output_directory_drift")

    def mkdir(self, name):
        self.check()
        os.mkdir(name, 0o700)
        self.hold(name)

    def write(self, name, content):
        self.check()
        fd = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        try:
            with os.fdopen(os.dup(fd), "wb") as output:
                output.write(content)
                output.flush()
            os.fsync(fd)
            info = os.fstat(fd)
            need(stat.S_ISREG(info.st_mode) and info.st_nlink == 1 and stat.S_IMODE(info.st_mode) == 0o600 and info.st_size == len(content), "output_file_drift")
        finally:
            os.close(fd)
        self.check()

    def sync(self):
        for fd in reversed(list(self.held.values())):
            self.check()
            os.fsync(fd)
            self.check()

    def close(self):
        for fd in self.held.values():
            os.close(fd)
        self.held.clear()


def consumer_result(raw, expected, runtime_version=NODE_VERSION):
    lines = raw.decode("utf-8", "strict").splitlines()
    for item in ("# tests 2", "# pass 2", "# fail 0", "# cancelled 0", "# skipped 0"):
        need(lines.count(item) == 1, "consumer_incomplete")
    summaries, origins = [], []
    for line in lines:
        if line.startswith("# {"):
            item = parse(line[2:].encode())
            if "layout" in item:
                need(set(item) == {"sourceHead", "manifestDigest", "node", "platform", "arch", "uid", "layout", "originalExecutions", "attempts", "deliveryDigest", "coldReplayDuplicateStarts"} and
                     item["sourceHead"] == expected["sourceHead"] and item["manifestDigest"] == expected["manifestDigest"] and
                     item["node"] == runtime_version and item["platform"] + "-" + item["arch"] in ("darwin-arm64", "linux-x64") and
                     integer(item["uid"]) and item["layout"] in (1, 2) and item["originalExecutions"] == item["attempts"] == 4 and
                     item["coldReplayDuplicateStarts"] == 0 and DIGEST.fullmatch(item["deliveryDigest"]), "consumer_binding_mismatch")
                summaries.append(item)
            elif "modelCalls" in item:
                need(item == {"artifactId": str(expected["artifactId"]), "source": "current-workflow-artifact", "modelCalls": 0}, "consumer_binding_mismatch")
                origins.append(item)
    need(len(summaries) == len(origins) == 2 and {item["layout"] for item in summaries} == {1, 2} and
         summaries[0]["deliveryDigest"] == summaries[1]["deliveryDigest"], "consumer_incomplete")
    return summaries


def admit(expected, *, api, source, node, target, archive=None):
    need(os.getuid() != 0, "ordinary_user_required")
    before = github_snapshot(api, expected)
    # A manual workflow input is not a trusted revision on its own. Bind it to
    # the original successful canonical run before importing its inventory.
    files = trusted_source(source, expected["sourceHead"], node)
    raw = read_regular(archive, MAX_ARCHIVE) if archive else api.raw(API_ROOT + f'/actions/artifacts/{expected["artifactId"]}/zip', MAX_ARCHIVE)
    need(len(raw) == before["archiveBytes"], "archive_size_mismatch")
    # 当前workflow强制4项UI测试并打包UI；不能用合法API-only旧包冒充本UI候选。
    contents = validate_archive(raw, expected, files, require_ui=True)
    need(github_snapshot(api, expected) == before, "github_observation_drift")
    output = PrivateOutput(target)
    try:
        carrier, installed = os.path.join(target, "carrier"), os.path.join(target, "installed")
        output.write(os.path.join(target, "candidate.zip"), raw)
        output.mkdir(carrier)
        for name, value in contents.items():
            destination = os.path.join(carrier, name)
            parts = Path(destination).parent.relative_to(carrier).parts
            parent = carrier
            for part in parts:
                parent = os.path.join(parent, part)
                if parent not in output.held:
                    output.mkdir(parent)
            output.write(destination, value)
        output.sync()
        trusted_source(source, expected["sourceHead"], node)
        restored = parse(command([node, os.path.join(source, "packages/task-distribution/main.mjs"), "restore-carrier",
            "--carrier", carrier, "--target", installed, "--manifest-digest", expected["manifestDigest"], "--source-head", expected["sourceHead"]]))
        need(restored.get("sourceHead") == expected["sourceHead"] and restored.get("manifestDigest") == expected["manifestDigest"] and
             restored.get("files") == len(contents) - 1, "restore_binding_mismatch")
        temporary = os.path.join(target, "consumer-tmp")
        output.mkdir(temporary)
        environment = {"PATH": str(Path(node).parent), "TMPDIR": temporary,
            "MARSHAL_CANDIDATE_ROOT": installed, "MARSHAL_CANDIDATE_MANIFEST": expected["manifestDigest"],
            "MARSHAL_CANDIDATE_SOURCE": expected["sourceHead"], "MARSHAL_CANDIDATE_ARTIFACT_ID": str(expected["artifactId"])}
        try:
            tap = command([node, "--test", "--test-concurrency=1", "--test-reporter=tap", os.path.join(source, "packages/task-distribution/candidate-consumer.mjs")],
                          env=environment, cwd=target, timeout=120, maximum=MAX_OUTPUT, owned_group=True)
        except CandidateError as error:
            output.write(os.path.join(target, "consumer.tap"), getattr(error, "output", b""))
            output.write(os.path.join(target, "failure.json"), (json.dumps({"code": str(error)}) + "\n").encode())
            output.sync()
            raise
        output.write(os.path.join(target, "consumer.tap"), tap)
        runtime_version = command([node, "--version"]).decode().strip().removeprefix("v")
        summaries = consumer_result(tap, expected, runtime_version)
        need(github_snapshot(api, expected) == before, "github_observation_drift")
        trusted_source(source, expected["sourceHead"], node)
        final = parse(command([node, os.path.join(source, "packages/task-distribution/main.mjs"), "verify", "--root", installed,
                              "--manifest-digest", expected["manifestDigest"]]))
        need(final == restored, "installed_bytes_drift")
        result = {"proofScope": "same-artifact-candidate-consumption-not-release-approval", "candidateVerified": True,
                  "repository": REPOSITORY, **expected, **before, "files": len(files), "bytes": restored["bytes"],
                  "consumer": {"passed": 2, "modelCalls": 0, "observations": summaries}}
        output.write(os.path.join(target, "result.json"), (json.dumps(result, separators=(",", ":")) + "\n").encode())
        output.sync()
        return result
    finally:
        output.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("source", "source-head", "run-id", "attempt", "artifact-id", "archive-digest", "manifest-digest", "node", "target"):
        parser.add_argument("--" + name, required=True)
    parser.add_argument("--archive", help="Optional original local ZIP; GitHub identity is still reread, never local metadata")
    parser.add_argument("--gh", default=shutil.which("gh"))
    args = parser.parse_args()
    try:
        expected = options(args.source_head, args.run_id, args.attempt, args.artifact_id, args.archive_digest, args.manifest_digest)
        result = admit(expected, api=GitHub(args.gh or ""), source=args.source, node=args.node, target=args.target, archive=args.archive)
        print(json.dumps(result, separators=(",", ":")))
    except (CandidateError, OSError, ValueError, KeyError, TypeError, UnicodeError):
        # Bounded code only: no stderr, credentials, signed URLs or fixture data.
        error = sys.exc_info()[1]
        print(json.dumps({"code": str(error) if isinstance(error, CandidateError) else "candidate_unavailable"}), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
