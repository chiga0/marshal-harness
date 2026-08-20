def sha256: type == "string" and test("^sha256:[0-9a-f]{64}$");
def sha40: type == "string" and test("^[0-9a-f]{40}$");
def positive_integer: type == "number" and floor == . and . >= 0;
def ztime: type == "string" and test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\\.[0-9]{1,9})?Z$");
def epoch: sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601;

.format == "marshal-skill/operator-admission-receipt-v2" and
.authority == "operator-local-non-core" and
(.taskId | type == "string" and length > 0) and
(.runId | type == "string" and length > 0) and
(.observationSequence | positive_integer) and
(.stateEventSequence | positive_integer) and
(.observedAt | ztime) and
(.validUntil | ztime) and
((.observedAt | epoch) <= now) and
(now <= (.validUntil | epoch)) and
(((.validUntil | epoch) - (.observedAt | epoch)) <= 60) and
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
(.files | type == "object" and keys == ["controlRecordsPath", "statePath"]) and
(.files.statePath | type == "string" and length > 0) and
(.files.controlRecordsPath | type == "string" and length > 0) and
(.planApproval | type == "object" and keys == ["controlSequence", "recordId"]) and
(.planApproval.recordId | type == "string" and length > 0) and
(.planApproval.controlSequence | positive_integer and . >= 1) and
(.launchEnvironment | type == "object" and keys == ["digest", "keys"]) and
(.launchEnvironment.keys | type == "array" and length > 0 and all(.[]; type == "string" and length > 0)) and
(.launchEnvironment.digest | sha256) and
(.tooling | type == "object" and keys == ["marshalExecutable", "watchScript"]) and
([.tooling.marshalExecutable, .tooling.watchScript] | all(.[];
  type == "object" and keys == ["device", "digest", "inode"] and
  (.digest | sha256) and (.device | positive_integer) and (.inode | positive_integer))) and
([.dynamicEvidence.doctorDigest, .dynamicEvidence.capacityDigest,
  .dynamicEvidence.providerBackpressureDigest] | all(.[]; sha256)) and
(.checks | type == "object" and
  keys == ["acceptancePure", "capacityAvailable", "currentPlanApproved",
           "doctorConfigured", "doctorSupported", "providerBackpressureAbsent",
           "scopeExclusive", "stateReady", "worktreeClean"] and
  all(.[]; . == true)) and
.decision == "admit" and
.reasonCode == "admitted"
