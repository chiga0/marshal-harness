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
func WithExistingWorktreeTargetFromGraph(context.Context, ExistingWorktreeDescriptorGraphV1, ExistingWorktreeBindRequestV1, *ExistingWorktreeObservationV1, func(ExistingWorktreeTargetSession) error) error {
	return ErrPlatformUnavailable
}

func validateDescriptorBoundRun(DescriptorBoundRunV1) error { return ErrPlatformUnavailable }
func validateExistingWorktreeDescriptorGraph(ExistingWorktreeDescriptorGraphV1) error {
	return ErrPlatformUnavailable
}
func ObserveExistingWorktreeFromGraph(context.Context, ExistingWorktreeDescriptorGraphV1, ExistingWorktreeBindRequestV1) (ExistingWorktreeObservationV1, error) {
	return ExistingWorktreeObservationV1{}, ErrPlatformUnavailable
}
func VerifyExistingWorktreeTargetFromGraph(ExistingWorktreeDescriptorGraphV1, ExistingWorktreeBindRequestV1, ExistingWorktreeObservationV1) error {
	return ErrPlatformUnavailable
}
func SyncExistingWorktreeProjectionFromGraph(ExistingWorktreeDescriptorGraphV1, ExistingWorktreeAuthoritySnapshotV1) error {
	return ErrPlatformUnavailable
}

type existingWorktreeProjection struct{}

func openExistingWorktreeProjection(ExistingWorktreeDescriptorGraphV1) (*existingWorktreeProjection, error) {
	return nil, ErrPlatformUnavailable
}
func (*existingWorktreeProjection) Close() error { return nil }
func (*existingWorktreeProjection) Sync(ExistingWorktreeAuthoritySnapshotV1) error {
	return ErrPlatformUnavailable
}
