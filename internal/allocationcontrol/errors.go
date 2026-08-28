// Package allocationcontrol implements the Darwin allocation filesystem
// recovery projection frozen by ADR 0057. The package never creates business
// authority: every mutation is driven by an already committed authority fact.
package allocationcontrol

import "errors"

var (
	ErrInvalid           = errors.New("allocationcontrol: invalid closed record")
	ErrAuthorityConflict = errors.New("allocationcontrol: authority conflict")
	// ErrAllocationUnavailable is the typed fail-closed result when the
	// production staging facade cannot obtain one current, complete Stage2
	// provision authority. It never authorizes a filesystem fallback.
	ErrAllocationUnavailable = errors.New("allocationcontrol: current production allocation unavailable")
	// ErrAllocationIntervention is the typed fail-closed result for a current
	// authority or held-filesystem identity that is internally inconsistent.
	// The caller must terminalize/repair the Attempt; retrying through a path
	// or directory-based adoption is forbidden.
	ErrAllocationIntervention = errors.New("allocationcontrol: production allocation requires intervention")
	ErrJournalCorrupt         = errors.New("allocationcontrol: recovery journal corrupt")
	ErrFilesystemConflict     = errors.New("allocationcontrol: filesystem identity conflict")
	ErrFilesystemUnknown      = errors.New("allocationcontrol: filesystem state unknown")
	ErrPlatformUnavailable    = errors.New("allocationcontrol: platform profile unavailable")
)
