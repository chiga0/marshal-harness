//go:build !darwin || !arm64

package productionruntime

type fixedDeliveryPublishPhase string

func readFixedDeliveryRecord(fixedServerRoot, string, int64) ([]byte, bool, error) {
	return nil, false, ErrFixedDeliveryConflict
}

func publishFixedDeliveryRecord(fixedServerRoot, string, []byte, func(fixedDeliveryPublishPhase) error) error {
	return ErrFixedDeliveryConflict
}

func adoptFixedDeliveryRecord(fixedServerRoot, string, []byte, func(fixedDeliveryPublishPhase) error) error {
	return ErrFixedDeliveryConflict
}
