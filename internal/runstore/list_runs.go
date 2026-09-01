package runstore

import "sort"

// ListExistingRunIDs returns the closed set of descriptor-bound Run
// directories without creating state. It is used by the fixed repository
// process to reconcile every in-flight Run before reporting ready.
func (s *Store) ListExistingRunIDs() ([]string, error) {
	runIDs, err := listExistingRunIDs(s)
	if err != nil {
		return nil, err
	}
	sort.Strings(runIDs)
	return runIDs, nil
}
