//go:build !darwin

package allocationcontrol

import (
	"context"
	"os"
)

func NewDescriptorBoundRunV1(string, *os.File, string) (DescriptorBoundRunV1, error) {
	return DescriptorBoundRunV1{}, ErrPlatformUnavailable
}
func NewExistingWorktreeDescriptorGraph(*os.File, *os.File, *os.File, *os.File, string) (ExistingWorktreeDescriptorGraphV1, error) {
	return ExistingWorktreeDescriptorGraphV1{}, ErrPlatformUnavailable
}
func NewLinkedExistingWorktreeDescriptorGraph(*os.File, *os.File, *os.File, *os.File, *os.File, *os.File, string, string) (ExistingWorktreeDescriptorGraphV1, error) {
	return ExistingWorktreeDescriptorGraphV1{}, ErrPlatformUnavailable
}
func ObserveHeldDirectoryIdentity(*os.File) (ObjectIdentityV1, error) {
	return ObjectIdentityV1{}, ErrPlatformUnavailable
}
func WithExistingWorktreeTargetFromGraph(context.Context, ExistingWorktreeDescriptorGraphV1, ExistingWorktreeBindRequestV1, *ExistingWorktreeObservationV1, func(ExistingWorktreeTargetSession) error) error {
	return ErrPlatformUnavailable
}

func validateDescriptorBoundRun(DescriptorBoundRunV1) error { return ErrPlatformUnavailable }
func ObserveExistingWorktreeFromGraph(context.Context, ExistingWorktreeDescriptorGraphV1, ExistingWorktreeBindRequestV1) (ExistingWorktreeObservationV1, error) {
	return ExistingWorktreeObservationV1{}, ErrPlatformUnavailable
}
func VerifyExistingWorktreeTargetFromGraph(ExistingWorktreeDescriptorGraphV1, ExistingWorktreeBindRequestV1, ExistingWorktreeObservationV1) error {
	return ErrPlatformUnavailable
}
func SyncExistingWorktreeProjectionFromGraph(ExistingWorktreeDescriptorGraphV1, ExistingWorktreeAuthoritySnapshotV1) error {
	return ErrPlatformUnavailable
}
func VerifyExistingWorktreeProjectionFromGraph(ExistingWorktreeDescriptorGraphV1, ExistingWorktreeAuthoritySnapshotV1) error {
	return ErrPlatformUnavailable
}
