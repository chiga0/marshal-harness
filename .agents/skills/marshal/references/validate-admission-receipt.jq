def sha256: type == "string" and test("^sha256:[0-9a-f]{64}$");
def sha40: type == "string" and test("^[0-9a-f]{40}$");
def positive_integer: type == "number" and floor == . and . >= 0;
def ztime: type == "string" and test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$");

.format == "marshal-skill/operator-admission-receipt-v1" and
.authority == "operator-local-non-core" and
(.taskId | type == "string" and length > 0) and
(.runId | type == "string" and length > 0) and
(.observationSequence | positive_integer) and
(.stateEventSequence | positive_integer) and
(.observedAt | ztime) and
(.validUntil | ztime) and
((.observedAt | fromdateiso8601) <= now) and
(now <= (.validUntil | fromdateiso8601)) and
(((.validUntil | fromdateiso8601) - (.observedAt | fromdateiso8601)) <= 60) and
(.bindings.sourceHead | sha40) and
(.bindings.baseSha | sha40) and
([.bindings.specDigest, .bindings.policyDigest, .bindings.capabilityDigest,
  .bindings.runStateDigest, .bindings.planApprovalDigest,
  .bindings.adapterConfigDigest, .bindings.eventContractDigest,
  .bindings.resultTransportDigest, .bindings.permissionProfileDigest] | all(.[]; sha256)) and
(.host.os == "darwin" or .host.os == "linux") and
(.host.arch == "arm64" or .host.arch == "amd64") and
(.host.fingerprintDigest | sha256) and
(.adapter.id == "qoder" or .adapter.id == "codex" or .adapter.id == "qwen" or .adapter.id == "pi") and
(.adapter.mode == "ordinary-user" or .adapter.mode == "strict") and
(.adapter.binaryVersion | type == "string" and length > 0) and
(.adapter.permissionMode | type == "string" and length > 0) and
(.adapter.resultPathIdentityDigest | sha256) and
(.adapter.executable.canonicalPath | type == "string" and startswith("/")) and
(.adapter.executable.digest | sha256) and
(.adapter.executable.device | positive_integer) and
(.adapter.executable.inode | positive_integer) and
(.worktree.canonicalPath | type == "string" and startswith("/")) and
(.worktree.headSha | sha40) and
(.worktree.statusDigest | sha256) and
(.worktree.scopeLeaseDigest | sha256) and
([.dynamicEvidence.doctorDigest, .dynamicEvidence.capacityDigest,
  .dynamicEvidence.providerBackpressureDigest] | all(.[]; sha256)) and
(.checks | type == "object" and
  keys == ["acceptancePure", "capacityAvailable", "currentPlanApproved",
           "doctorConfigured", "doctorSupported", "providerBackpressureAbsent",
           "scopeExclusive", "stateReady", "worktreeClean"] and
  all(.[]; . == true)) and
.decision == "admit" and
.reasonCode == "admitted"
