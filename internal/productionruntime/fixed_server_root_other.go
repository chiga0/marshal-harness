//go:build !darwin || !arm64

package productionruntime

import "os"

type fixedServerRoot struct{}
type CanonicalRepositoryRoot struct{}

func OpenCanonicalRepositoryRoot(string) (*CanonicalRepositoryRoot, error) {
	return nil, ErrFixedDeliveryConflict
}

func (*CanonicalRepositoryRoot) Close() error { return nil }

func openFixedServerRoot(*CanonicalRepositoryRoot) (fixedServerRoot, error) {
	return fixedServerRoot{}, ErrFixedDeliveryConflict
}

func validateFixedServerRoot(fixedServerRoot, int) error     { return ErrFixedDeliveryConflict }
func adoptFixedServerRuntimeMutation(*fixedServerRoot) error { return ErrFixedDeliveryConflict }
func (fixedServerRoot) stateRoot() *os.File                  { return nil }
func (fixedServerRoot) deliveryRoot() *os.File               { return nil }
func (fixedServerRoot) digest() (string, error)              { return "", ErrFixedDeliveryConflict }
func (*fixedServerRoot) close() error                        { return nil }
