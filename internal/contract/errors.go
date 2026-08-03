package contract

import (
	"fmt"
	"strings"
)

// Violation is a deterministic semantic contract failure.
type Violation struct {
	Path    string
	Code    string
	Message string
}

// SemanticError contains all semantic violations found in one record.
type SemanticError struct {
	Violations []Violation
}

func (e *SemanticError) Error() string {
	parts := make([]string, 0, len(e.Violations))
	for _, violation := range e.Violations {
		parts = append(parts, fmt.Sprintf("%s [%s]: %s", violation.Path, violation.Code, violation.Message))
	}
	return "semantic validation failed: " + strings.Join(parts, "; ")
}
