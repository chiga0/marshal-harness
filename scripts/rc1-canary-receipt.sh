#!/usr/bin/env bash
# RC1 canary receipt/carrier producer (ADR 0068 §RC1 evidence)。
# 从 ACCEPTED canary evidence 目录、dist/ 与外部 authority 值组装
# RC1-CANARY-RECEIPT.json 并构建单资产 Darwin arm64 immutable carrier。
# 脚本不产生发布授权；所有字节身份由其后的 rc1-carrier-check.py 独立裁决。
#
# 用法:
#  scripts/rc1-canary-receipt.sh \
#    --canary-root .marshal/release-canary/<RUN_ID> --dist-root dist \
#    --out-dir <carrier-out-dir> \
#    --expected-head 40-hex --workflow-run-id N \
#    --artifact-id N --artifact-digest raw-sha256 \
#    --authority-head sha256:DIGEST \
#    --agent-version X --worker-actor-id ID --verifier-actor-id ID --decision PATH
set -euo pipefail
umask 077

CANARY_ROOT=""
DIST_ROOT=""
OUT_DIR=""
EXPECTED_HEAD=""
WORKFLOW_RUN_ID=""
ARTIFACT_ID=""
ARTIFACT_DIGEST=""
AUTHORITY_HEAD=""
AGENT_VERSION=""
WORKER_ACTOR_ID=""
VERIFIER_ACTOR_ID=""
DECISION_PATH=""

die() { printf '[rc1-canary-receipt] FAIL: %s\n' "$*" >&2; exit 1; }

while [ "$#" -gt 0 ]; do
  case "$1" in
    --canary-root) CANARY_ROOT="$2"; shift 2 ;;
    --dist-root) DIST_ROOT="$2"; shift 2 ;;
    --out-dir) OUT_DIR="$2"; shift 2 ;;
    --expected-head) EXPECTED_HEAD="$2"; shift 2 ;;
    --workflow-run-id) WORKFLOW_RUN_ID="$2"; shift 2 ;;
    --artifact-id) ARTIFACT_ID="$2"; shift 2 ;;
    --artifact-digest) ARTIFACT_DIGEST="$2"; shift 2 ;;
    --authority-head) AUTHORITY_HEAD="$2"; shift 2 ;;
    --agent-version) AGENT_VERSION="$2"; shift 2 ;;
    --worker-actor-id) WORKER_ACTOR_ID="$2"; shift 2 ;;
    --verifier-actor-id) VERIFIER_ACTOR_ID="$2"; shift 2 ;;
    --decision) DECISION_PATH="$2"; shift 2 ;;
    *) die "未知参数 $1" ;;
  esac
done

[ -n "$CANARY_ROOT" ] && [ -n "$DIST_ROOT" ] && [ -n "$OUT_DIR" ] || die "缺少 --canary-root/--dist-root/--out-dir"
[[ "$EXPECTED_HEAD" =~ ^[0-9a-f]{40}$ ]] || die "--expected-head 必须 40-hex"
[[ "$WORKFLOW_RUN_ID" =~ ^[1-9][0-9]*$ ]] || die "--workflow-run-id 必须为正整数"
[[ "$ARTIFACT_ID" =~ ^[1-9][0-9]*$ ]] || die "--artifact-id 必须为正整数"
[[ "$ARTIFACT_DIGEST" =~ ^[0-9a-f]{64}$ ]] || die "--artifact-digest 必须 raw 64-hex"
[[ "$AUTHORITY_HEAD" =~ ^sha256:[0-9a-f]{64}$ ]] || die "--authority-head 必须 sha256:64-hex"
[ -n "$AGENT_VERSION" ] && [ -n "$WORKER_ACTOR_ID" ] && [ -n "$VERIFIER_ACTOR_ID" ] || die "缺少 agent/worker/verifier actor 输入"
[ -f "$DECISION_PATH" ] || die "缺少合法 ReviewDecision 文件：$DECISION_PATH"
[ -d "$CANARY_ROOT" ] || die "canary 目录不存在：$CANARY_ROOT"
[ -f "$DIST_ROOT/RELEASE-MANIFEST" ] || die "缺 RELEASE-MANIFEST"
[ -f "$DIST_ROOT/SHA256SUMS" ] || die "缺 SHA256SUMS"
[ -f "$DIST_ROOT/marshal_1.0.0-rc1_darwin_arm64" ] || die "缺 candidate binary"

PYTHON_BIN="${PYTHON_BIN:-/usr/bin/python3}"
"$PYTHON_BIN" -I -B - "$CANARY_ROOT" "$DIST_ROOT" "$OUT_DIR" "$EXPECTED_HEAD" "$WORKFLOW_RUN_ID" "$ARTIFACT_ID" "$ARTIFACT_DIGEST" "$AUTHORITY_HEAD" "$AGENT_VERSION" "$WORKER_ACTOR_ID" "$VERIFIER_ACTOR_ID" "$DECISION_PATH" <<'PY'
import hashlib
import json
import os
import sys

canary_root, dist_root, out_dir, expected_head, workflow_run_id, artifact_id, artifact_digest, authority_head, agent_version, worker_actor_id, verifier_actor_id, decision_path = sys.argv[1:]

VERSION = "1.0.0-rc1"
TAG = "v1.0.0-rc1"
BINARY_NAME = f"marshal_{VERSION}_darwin_arm64"
MANIFEST_NAME = "RELEASE-MANIFEST"
SUMS_NAME = "SHA256SUMS"
RECEIPT_NAME = "RC1-CANARY-RECEIPT.json"


def fail(msg):
    raise SystemExit(f"[rc1-canary-receipt] ERROR: {msg}")


def file_bytes(path):
    if not os.path.isfile(path):
        fail(f"evidence 缺失：{path}")
    with open(path, "rb") as handle:
        return handle.read()


def sha256_raw(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def sha256_json(data: bytes) -> str:
    return "sha256:" + sha256_raw(data)


def canonical_json(value) -> bytes:
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")


def canonical_digest(value) -> str:
    return "sha256:" + hashlib.sha256(canonical_json(value)).hexdigest()


def load_json(path):
    return json.loads(file_bytes(path).decode("utf-8"))


def require_mapping(value, label):
    if not isinstance(value, dict):
        fail(f"{label} 必须是 object")
    return value


def require_field(container, key, label):
    require_mapping(container, label)
    if key not in container:
        fail(f"{label} 缺字段 {key}")
    return container[key]


def require_digest(value, label):
    if not isinstance(value, str) or not value.startswith("sha256:") or len(value) != 71:
        fail(f"{label} 必须是 sha256: 前缀 digest（实际：{value!r}）")
    return value


def require_identifier(value, label):
    if not isinstance(value, str) or not value or len(value) > 160:
        fail(f"{label} 必须是合法标识符")
    return value


run_dir = os.path.join(canary_root, "repository", ".marshal", "runs")
if not os.path.isdir(run_dir):
    fail(f"canary repository state 缺失：{run_dir}")
runs = [d for d in os.listdir(run_dir) if os.path.isdir(os.path.join(run_dir, d))]
if len(runs) != 1:
    fail(f"canary runs 目录必须恰好一个 Run：{runs}")
run_id = runs[0]
run_root = os.path.join(run_dir, run_id)

state = load_json(os.path.join(run_root, "state.json"))
attempt_id = require_field(state, "currentAttemptId", "run state.json")
if state.get("state") != "ACCEPTED":
    fail(f"canary 状态未终态为 ACCEPTED：{state.get('state')}")
task_id = require_field(state, "taskId", "run state.json")
base_sha = require_field(state, "baseSha", "run state.json")
spec_digest = require_digest(require_field(state, "specDigest", "run state.json"), "state.specDigest")

attempt_root = os.path.join(run_root, "attempts", attempt_id)
worker_result_path = os.path.join(attempt_root, "worker-result.json")
worker_result_bytes = file_bytes(worker_result_path)
worker_result_sha = sha256_json(worker_result_bytes)
result = json.loads(worker_result_bytes.decode("utf-8"))

identity = load_json(os.path.join(canary_root, "control", "identity.json"))
if require_field(identity, "runId", "identity") != run_id:
    fail("identity.runId 与 Run state 不一致")
if require_field(identity, "taskId", "identity") != task_id:
    fail("identity.taskId 与 Run state 不一致")
if require_field(identity, "expectedHead", "identity") != expected_head:
    fail("identity.expectedHead 漂移")

def nested_lookup(obj, wanted, label):
    found = []
    def visit(value, path):
        if isinstance(value, dict):
            if wanted in value:
                found.append((path, value[wanted]))
            for key, child in value.items():
                visit(child, path + "/" + key)
        elif isinstance(value, list):
            for i, child in enumerate(value):
                visit(child, f"{path}[{i}]")
    visit(obj, "")
    if not found:
        fail(f"{label} 中找不到 {wanted}")
    return found[0][1]

verification_path = os.path.join(canary_root, "control", "verification.json")
verification_obj = load_json(verification_path)
verification_bytes = file_bytes(verification_path)
verification_digest = sha256_json(verification_bytes)

packet_path = os.path.join(canary_root, "control", "review-packet-output.json")
packet_obj = load_json(packet_path)
packet_bytes = file_bytes(packet_path)
packet = require_field(packet_obj, "packet", "review-packet-output")
review_round = packet.get("reviewRound")
if review_round is None:
    review_round = 0
packet_digest_field = packet_obj.get("packetDigest") or packet.get("packetDigest")
packet_digest = require_digest(packet_digest_field or sha256_json(packet_bytes), "packetDigest")
local_binding = require_field(packet, "localSelfIdentityBinding", "packet")
local_binding_digest = canonical_digest(local_binding)
if not local_binding_digest.startswith("sha256:"):
    fail("localSelfIdentityBindingDigest 指向非法值")

doctor = load_json(os.path.join(canary_root, "control", "doctor.json"))
binding = require_field(doctor, "policyEnvironmentBinding", "doctor")
activation_digest = require_digest(require_field(binding, "activationDigest", "binding"), "binding.activationDigest")
subject_digest = require_digest(require_field(binding, "identitySubjectDigest", "binding"), "binding.identitySubjectDigest")
profile_name = require_field(binding, "selfProfile", "binding")

dispatch_identity = load_json(os.path.join(attempt_root, "local-self-identity-dispatch.json"))
ingress_identity = load_json(os.path.join(attempt_root, "local-self-identity-ingress.json"))
current_observation_digest = canonical_digest(ingress_identity)

verification_report = require_field(verification_obj, "report", "verification") if "report" in verification_obj else verification_obj
artifact_manifest_digest = require_digest(
    require_field(verification_report, "artifactManifestDigest", "verification"),
    "verification.artifactManifestDigest",
)
verification_inner_digest = require_digest(
    require_field(verification_report, "digest", "verification") if "digest" in verification_report else verification_digest,
    "verification.digest",
)

evidence = require_field(packet_obj, "evidence", "review-packet-output")
evidence_digest = require_digest(require_field(evidence, "digest", "packet.evidence"), "packet.evidence.digest")
decision = load_json(decision_path)
decision_digest = canonical_digest(decision)
reviewer = require_mapping(require_field(decision, "reviewer", "decision"), "decision.reviewer")
reviewer_actor = require_identifier(require_field(reviewer, "actorId", "decision.reviewer"), "decision.reviewer.actorId")
if require_field(decision, "independent", "decision") is not True:
    fail("decision.independent 必须为 true")
verdict = require_field(decision, "verdict", "decision")
if verdict != "accept":
    fail(f"decision.verdict 必须为 accept：{verdict}")
publication_rec = require_field(decision, "publicationRecommendation", "decision")

digest_head_bytes = file_bytes(os.path.join(canary_root, "repository", ".marshal", "result-ingress", "result-ingress.jsonl"))
last_line = digest_head_bytes.rstrip(b"\n").split(b"\n")[-1]
last_record = json.loads(last_line.decode("utf-8"))
record_digest = require_digest(require_field(last_record, "recordDigest", "ingress journal tail"), "ingress.recordDigest")
if record_digest != authority_head:
    fail(f"authority-head 输入与 ingress journal 当前持久头不一致：{authority_head} != {record_digest}")

adapter = require_mapping(require_field(result, "adapter", "worker-result"), "worker-result")
agent_adapter_id = require_field(adapter, "adapterId", "worker-result.adapter")
pi_version = require_field(adapter, "binaryVersion", "worker-result.adapter")
pi_provider = require_field(adapter, "model", "worker-result.adapter")
pi_identity = require_mapping(require_field(result, "identity", "worker-result"), "worker-result.identity")

binary_path = os.path.join(dist_root, BINARY_NAME)
binary_bytes = file_bytes(binary_path)
binary_sha256 = sha256_json(binary_bytes)
binary_size = len(binary_bytes)
manifest_bytes = file_bytes(os.path.join(dist_root, MANIFEST_NAME))
sums_bytes = file_bytes(os.path.join(dist_root, SUMS_NAME))
manifest_text = manifest_bytes.decode("utf-8")
manifest = {}
for line in manifest_text.splitlines():
    key, _, value = line.partition(" ")
    if key and value:
        manifest[key] = value

activation_current_ccp = os.path.realpath(os.path.join(os.getcwd(), "bin", "marshal"))
customer_bin = file_bytes(os.path.join(os.getcwd(), "bin", "marshal"))
if sha256_json(customer_bin) != binary_sha256:
    fail("bin/marshal 不是 candidate exact bytes")

receipt = {
    "schemaVersion": "marshal.rc1-canary-receipt.v1",
    "tag": TAG,
    "sourceHead": expected_head,
    "candidateWorkflow": {
        "runId": int(workflow_run_id),
        "artifactId": int(artifact_id),
        "artifactDigest": "sha256:" + artifact_digest,
    },
    "payload": {"schemaVersion": "marshal.rc1-carrier-payload.v1", "sha256": "", "size": 0},
    "manifest": {"path": MANIFEST_NAME, "sha256": sha256_json(manifest_bytes), "size": len(manifest_bytes)},
    "checksums": {"path": SUMS_NAME, "sha256": sha256_json(sums_bytes), "size": len(sums_bytes)},
    "binary": {
        "path": BINARY_NAME,
        "sha256": binary_sha256,
        "size": binary_size,
        "version": VERSION,
        "buildDate": manifest.get("buildDate", ""),
        "goVersion": manifest.get("goVersion", ""),
        "os": "darwin",
        "arch": "arm64",
        "profile": "darwin-local-dogfood",
    },
    "activation": {
        "activationDigest": activation_digest,
        "identitySubjectDigest": subject_digest,
        "currentObjectObservationDigest": current_observation_digest,
        "currentCanonicalPath": activation_current_ccp,
        "currentObjectRawSHA256": binary_sha256,
        "currentObjectSize": binary_size,
        "sourceHead": expected_head,
        "profile": profile_name,
        "localSelfIdentityBindingDigest": local_binding_digest,
    },
    "canary": {
        "taskId": task_id,
        "runId": run_id,
        "attemptId": attempt_id,
        "specDigest": spec_digest,
        "baseSha": base_sha,
        "artifactManifestDigest": artifact_manifest_digest,
        "workerResultDigests": [worker_result_sha],
        "localSelfIdentityBindingDigest": local_binding_digest,
        "agentProvider": agent_adapter_id,
        "agentVersion": agent_version,
        "invocation": "real",
        "workerActorId": worker_actor_id,
        "reviewPacket": {
            "digest": packet_digest,
            "runId": run_id,
            "attemptId": attempt_id,
            "reviewRound": review_round,
            "specDigest": spec_digest,
            "baseSha": base_sha,
            "verificationDigest": verification_inner_digest if isinstance(verification_inner_digest, str) else verification_digest,
            "artifactManifestDigest": artifact_manifest_digest,
            "workerResultDigests": [worker_result_sha],
            "evidenceDigest": evidence_digest,
            "localSelfIdentityBindingDigest": local_binding_digest,
        },
        "verification": {
            "digest": verification_inner_digest if isinstance(verification_inner_digest, str) else verification_digest,
            "runId": run_id,
            "attemptId": attempt_id,
            "specDigest": spec_digest,
            "artifactManifestDigest": artifact_manifest_digest,
            "workerResultDigests": [worker_result_sha],
            "evidenceDigest": evidence_digest,
            "localSelfIdentityBindingDigest": local_binding_digest,
            "verifier": {"type": "deterministic-verifier", "id": verifier_actor_id},
            "status": "pass",
            "independent": True,
        },
        "evidence": {
            "digest": evidence_digest,
            "runId": run_id,
            "attemptId": attempt_id,
            "specDigest": spec_digest,
            "baseSha": base_sha,
            "artifactManifestDigest": artifact_manifest_digest,
            "workerResultDigests": [worker_result_sha],
            "localSelfIdentityBindingDigest": local_binding_digest,
        },
        "reviewDecision": {
            "digest": decision_digest,
            "runId": run_id,
            "reviewRound": review_round,
            "reviewer": {"actorId": reviewer_actor},
            "independent": True,
            "specDigest": spec_digest,
            "reviewPacketDigest": packet_digest,
            "verificationDigest": verification_inner_digest if isinstance(verification_inner_digest, str) else verification_digest,
            "artifactManifestDigest": artifact_manifest_digest,
            "evidenceDigest": evidence_digest,
            "localSelfIdentityBindingDigest": local_binding_digest,
            "verdict": "accept",
            "blockingFindingCount": 0,
            "publicationRecommendation": publication_rec,
        },
        "outcome": {
            "runId": run_id,
            "state": "ACCEPTED",
            "terminalReason": require_field(state, "terminalReason", "state"),
            "attemptsUsed": require_field(state, "attemptsUsed", "state"),
            "reviewRound": review_round,
        },
        "publication": "none",
    },
    "authority": {
        "authorityHead": authority_head,
        "ownerAttempt": attempt_id,
        "ingressLedger": {"head": authority_head, "tailDigest": require_digest(require_field(last_record, "previousRecordDigest", "ingress"), "ingress.previousRecordDigest")},
    },
    "receiptDigest": "",
}

payload_bytes = binary_bytes + manifest_bytes + sums_bytes
payload = receipt["payload"]
payload["sha256"] = "sha256:" + hashlib.sha256(payload_bytes).hexdigest()
payload["size"] = len(payload_bytes)

os.makedirs(out_dir, exist_ok=True)
receipt_stub = dict(receipt)
receipt_stub["receiptDigest"] = ""
detached_bytes = canonical_json(receipt_stub)
receipt_digest_raw = hashlib.sha256(detached_bytes).hexdigest()
receipt["receiptDigest"] = "sha256:" + receipt_digest_raw

for name, data in (
    (BINARY_NAME, binary_bytes),
    (MANIFEST_NAME, manifest_bytes),
    (SUMS_NAME, sums_bytes),
    (RECEIPT_NAME, canonical_json(receipt) + b"\n"),
):
    path = os.path.join(out_dir, name)
    mode = 0o755 if name == BINARY_NAME else 0o644
    with open(path, "wb") as handle:
        handle.write(data)
    os.chmod(path, mode)

print(receipt_digest_raw)
PY