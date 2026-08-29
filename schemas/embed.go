// Package schemas exposes Marshal's versioned JSON Schemas and contract
// fixtures as an embedded filesystem. The JSON files remain the source of
// truth; embedding keeps the marshal binary self-contained.
package schemas

import "embed"

// FS contains all v1alpha1 schemas and their contract fixtures.
//
//go:embed *.schema.json examples/happy-path/*.json examples/invalid/*.json examples/local-dogfood/*.json selfidentity/*.schema.json release/*.schema.json release/examples/valid/*.json release/examples/invalid/*.json
var FS embed.FS
