//go:build darwin

package processsupervisor

import (
	"errors"
	"syscall"
	"testing"
)

func TestDecodeChildInvocationRoutesExactGeneration(t *testing.T) {
	payload := validSpawnPayload()
	bootstrap := validBootstrap()
	marshal := HeldObjectSpec{
		Role: "marshal", CanonicalPath: bootstrap.Core.Binary.CanonicalPath,
		Device: bootstrap.Core.Binary.Device, Inode: bootstrap.Core.Binary.Inode,
		FileType: bootstrap.Core.Binary.FileType, UID: bootstrap.Core.Binary.UID,
		GID: bootstrap.Core.Binary.GID, Mode: bootstrap.Core.Binary.Mode,
		LinkCount: bootstrap.Core.Binary.LinkCount, Size: bootstrap.Core.Binary.Size,
		RawSHA256: bootstrap.Core.Binary.RawSHA256,
	}
	v1, err := buildChildSpec(payload, marshal)
	if err != nil {
		t.Fatal(err)
	}
	rawV1, err := v1.canonical()
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := decodeChildInvocation(rawV1)
	if err != nil || invocation.protocolRevision != ProtocolRevision {
		t.Fatalf("v1 route failed: revision=%q err=%v", invocation.protocolRevision, err)
	}
	v2, err := buildChildSpecV2(payload, marshal)
	if err != nil {
		t.Fatal(err)
	}
	rawV2, err := v2.canonical()
	if err != nil {
		t.Fatal(err)
	}
	invocation, err = decodeChildInvocation(rawV2)
	if err != nil || invocation.protocolRevision != protocolRevisionV2 || invocation.spec.ProtocolRevision != protocolRevisionV2 {
		t.Fatalf("v2 route failed: revision=%q err=%v", invocation.protocolRevision, err)
	}

	mixed := map[string]any{}
	if strictCanonicalDecode(rawV2, &mixed) != nil {
		t.Fatal("fixture decode failed")
	}
	mixed["protocolRevision"] = ProtocolRevision
	if _, err := decodeChildInvocation(mustCanonical(mixed)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mixed v2-as-v1 child accepted: %v", err)
	}
}

func TestDarwinExecBarrierIsGenerationExact(t *testing.T) {
	startSuspended := syscall.WaitStatus(0x7f)
	ptraceTrap := syscall.WaitStatus(uint32(syscall.SIGTRAP)<<8 | 0x7f)
	signalStop := syscall.WaitStatus(uint32(syscall.SIGSTOP)<<8 | 0x7f)
	if !validExecBarrier(startSuspended, protocolRevisionV2) || validExecBarrier(startSuspended, ProtocolRevision) {
		t.Fatal("START_SUSPENDED zero-signal barrier routed incorrectly")
	}
	if !validExecBarrier(ptraceTrap, ProtocolRevision) || validExecBarrier(ptraceTrap, protocolRevisionV2) {
		t.Fatal("ptrace SIGTRAP barrier routed incorrectly")
	}
	if validExecBarrier(signalStop, protocolRevisionV2) || validExecBarrier(signalStop, "process-supervisor/v3") {
		t.Fatal("unexpected signal or generation admitted")
	}
}

func TestChildStageReasonRemainsClosedAndTyped(t *testing.T) {
	err := childReject("marshal-parent-conflict", ErrConflict)
	if !errors.Is(err, ErrConflict) || ReasonCode(err) != "process-supervisor-child-marshal-parent-conflict" {
		t.Fatalf("closed child reason lost typing: %v / %s", err, ReasonCode(err))
	}
}
