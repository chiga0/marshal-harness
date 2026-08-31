//go:build !darwin

package launchidentity

type HeldClosure struct{ Closure ClosureV1 }

func (held *HeldClosure) Close() {}
func OpenPi0844(string, string, []string, []string, string) (*HeldClosure, error) {
	return nil, ErrUnavailable
}
func Reopen(ClosureV1) (*HeldClosure, error) { return nil, ErrUnavailable }
