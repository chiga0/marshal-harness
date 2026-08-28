// Package allocationcontrol implements the Darwin allocation filesystem
// recovery projection frozen by ADR 0057. The package never creates business
// authority: every mutation is driven by an already committed authority fact.
package allocationcontrol

import "errors"

var (
	ErrInvalid             = errors.New("allocationcontrol: invalid closed record")
	ErrAuthorityConflict   = errors.New("allocationcontrol: authority conflict")
	ErrJournalCorrupt      = errors.New("allocationcontrol: recovery journal corrupt")
	ErrFilesystemConflict  = errors.New("allocationcontrol: filesystem identity conflict")
	ErrFilesystemUnknown   = errors.New("allocationcontrol: filesystem state unknown")
	ErrPlatformUnavailable = errors.New("allocationcontrol: platform profile unavailable")
)
