package launcher

import "time"

// ExecutePath consumes the one-use envelope and replaces (or, on platforms
// without exec, supervises) the current process with its exact Worker launch.
// Environment values are never accepted as function arguments.
func ExecutePath(path string, now time.Time) error {
	envelope, err := ConsumePath(path, now)
	if err != nil {
		return err
	}
	return executeEnvelope(envelope)
}
