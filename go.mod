module github.com/chiga0/marshal-harness

go 1.26.0

toolchain go1.26.6

require (
	github.com/bmatcuk/doublestar/v4 v4.10.0
	github.com/dlclark/regexp2 v1.11.0
	github.com/gofrs/flock v0.13.0
	github.com/gowebpki/jcs v1.0.1
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2
)

require (
	github.com/BurntSushi/toml v1.4.1-0.20240526193622-a339e1f7089c // indirect
	golang.org/x/exp/typeparams v0.0.0-20231108232855-2478ac86f678 // indirect
	golang.org/x/mod v0.38.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/telemetry v0.0.0-20260708182218-49f421fb7959 // indirect
	golang.org/x/text v0.39.0 // indirect
	golang.org/x/tools v0.48.0 // indirect
	golang.org/x/vuln v1.6.0 // indirect
	honnef.co/go/tools v0.7.0 // indirect
)

tool honnef.co/go/tools/cmd/staticcheck

tool golang.org/x/vuln/cmd/govulncheck
