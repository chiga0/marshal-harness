//go:build darwin

package allocationcontrol

func decodeExistingWorktreePayload[T any](fact ExistingWorktreeAttemptFactV1) (T, error) {
	var value T
	if fact.Validate() != nil || strictCanonicalDecode(fact.Payload, &value) != nil {
		return value, ErrAuthorityConflict
	}
	return value, nil
}
