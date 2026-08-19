package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/adapter"
	"github.com/chiga0/marshal-harness/internal/adapter/codex"
	"github.com/chiga0/marshal-harness/internal/adapter/opencode"
	"github.com/chiga0/marshal-harness/internal/adapter/pi"
	"github.com/chiga0/marshal-harness/internal/adapter/qoder"
	"github.com/chiga0/marshal-harness/internal/adapter/qwen"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/darwin"
	"github.com/chiga0/marshal-harness/internal/port"
)

// Worker configuration outcomes are fixed, structured codes. They never embed
// executable paths, environment values, or underlying provider errors, so
// doctor output and selection evidence cannot leak local environment details.
const (
	WorkerOutcomeNotConfigured        = "not-configured"
	WorkerOutcomeRegistered           = "registered"
	WorkerOutcomeInvalidConfiguration = "invalid-configuration"
)

// WorkerConfiguration is the structured, provider-neutral outcome of one
// frozen adapter binding. It intentionally carries no executable path, no
// error, and no secret.
type WorkerConfiguration struct {
	AdapterID           string `json:"adapterId"`
	EnvironmentVariable string `json:"environmentVariable"`
	Configured          bool   `json:"configured"`
	Registered          bool   `json:"registered"`
	Outcome             string `json:"outcome"`
	// AuthorityEndpointStatus is diagnostic only; it never changes registry
	// admission or the adapter's fail-closed probe result.
	AuthorityEndpointStatus string `json:"authorityEndpointStatus,omitempty"`
}

// workerBinding freezes the only adapter-to-environment mapping Marshal
// supports. Order here is the order Configurations reports; it never changes
// implicitly and registration never searches PATH or matches similar names.
// binaryNames and identify feed advisory discovery (DiscoverWorkers) only;
// discovery can suggest an export line but never registers an adapter.
type workerBinding struct {
	adapterID           string
	environmentVariable string
	binaryNames         []string
	identify            func(executable string) (version, digest string, err error)
	requiresAuthority   bool
	construct           func(executable string, validator *contract.Validator, getenv func(string) string) (port.WorkerAdapter, error)
}

var workerBindings = []workerBinding{
	{
		adapterID:           "opencode",
		environmentVariable: "MARSHAL_OPENCODE_PATH",
		binaryNames:         []string{"opencode"},
		identify:            opencode.Identify,
		construct: func(executable string, validator *contract.Validator, _ func(string) string) (port.WorkerAdapter, error) {
			return opencode.New(executable, validator)
		},
	},
	{
		adapterID:           "qwen",
		environmentVariable: "MARSHAL_QWEN_PATH",
		binaryNames:         []string{"qwen", "qwen-code"},
		identify:            qwen.Identify,
		construct: func(executable string, validator *contract.Validator, _ func(string) string) (port.WorkerAdapter, error) {
			return qwen.New(executable, validator)
		},
	},
	{
		adapterID:           "qoder",
		environmentVariable: "MARSHAL_QODER_PATH",
		binaryNames:         []string{"qodercli"},
		identify:            qoder.Identify,
		requiresAuthority:   true,
		construct: func(executable string, validator *contract.Validator, getenv func(string) string) (port.WorkerAdapter, error) {
			config := getenv("MARSHAL_QODER_CONFORMANCE_CONFIG")
			if config == "" {
				// Registration is not support admission. Without an authority
				// config Probe remains unsupported.
				return qoder.New(executable, validator)
			}
			return qoder.NewFromAuthorityConfig(context.Background(), executable, validator, config)
		},
	},
	{
		adapterID:           "codex",
		environmentVariable: "MARSHAL_CODEX_PATH",
		binaryNames:         []string{"codex"},
		identify:            codex.Identify,
		requiresAuthority:   true,
		construct: func(executable string, validator *contract.Validator, getenv func(string) string) (port.WorkerAdapter, error) {
			config := getenv("MARSHAL_CODEX_AUTHORITY_CONFIG")
			if config == "" {
				// Registration only makes the fail-closed Probe observable. It
				// does not admit Codex as supported without ADR 0037 authority.
				return codex.New(executable, validator)
			}
			return codex.NewFromAuthorityConfig(context.Background(), executable, validator, config)
		},
	},
	{
		adapterID:           "pi",
		environmentVariable: "MARSHAL_PI_PATH",
		binaryNames:         []string{"pi"},
		identify:            pi.Identify,
		construct: func(executable string, validator *contract.Validator, _ func(string) string) (port.WorkerAdapter, error) {
			return pi.New(executable, validator)
		},
	},
}

// WorkerRuntime assembles the provider-neutral local Worker runtime shared by
// `task plan`, `task run`, and `doctor`. It constructs concrete adapters from
// explicit environment values and never searches PATH, reads os.Environ, or
// writes files. Codex authority configuration is hard-disabled before any
// authority read until the credentialed provider is implemented; the caller
// supplies the environment lookup.
type WorkerRuntime struct {
	validator      *contract.Validator
	registry       *adapter.Registry
	selector       *adapter.Selector
	configurations []WorkerConfiguration
}

// NewWorkerRuntime builds the shared runtime from the caller-provided
// environment lookup. A nil lookup is fail-closed. An invalid or unconfigured
// adapter binding is recorded with a structured outcome and skipped so that
// explicit fallback can continue; only base initialization or invariant errors
// abort construction, always with a fixed, non-leaking message.
func NewWorkerRuntime(getenv func(string) string) (*WorkerRuntime, error) {
	if getenv == nil {
		return nil, port.Permanentf("worker runtime: nil environment lookup")
	}
	validator, err := contract.NewValidator()
	if err != nil {
		return nil, port.Permanentf("worker runtime: initialize contract validator")
	}
	registry := adapter.NewRegistry()
	configurations := make([]WorkerConfiguration, 0, len(workerBindings))
	for _, binding := range workerBindings {
		configuration := WorkerConfiguration{
			AdapterID:           binding.adapterID,
			EnvironmentVariable: binding.environmentVariable,
		}
		executable := getenv(binding.environmentVariable)
		if executable == "" {
			configuration.Outcome = WorkerOutcomeNotConfigured
			configurations = append(configurations, configuration)
			continue
		}
		configuration.Configured = true
		if binding.requiresAuthority {
			configuration.AuthorityEndpointStatus = darwin.InspectAuthorityEndpointStatus(getenv("MARSHAL_APAP_ENDPOINT"))
		}
		worker, constructErr := binding.construct(executable, validator, getenv)
		if constructErr != nil {
			// Deliberately discard the executable, the environment value, and
			// the underlying error: none may be stored or echoed.
			configuration.Outcome = WorkerOutcomeInvalidConfiguration
			configurations = append(configurations, configuration)
			continue
		}
		if registerErr := registry.Register(worker); registerErr != nil {
			return nil, port.Permanentf("worker runtime: invariant violation registering adapter")
		}
		configuration.Registered = true
		configuration.Outcome = WorkerOutcomeRegistered
		configurations = append(configurations, configuration)
	}
	selector, err := adapter.NewSelector(registry)
	if err != nil {
		return nil, port.Permanentf("worker runtime: initialize adapter selector")
	}
	return &WorkerRuntime{
		validator:      validator,
		registry:       registry,
		selector:       selector,
		configurations: configurations,
	}, nil
}

// Validator returns the shared contract validator.
func (r *WorkerRuntime) Validator() *contract.Validator { return r.validator }

// Registry returns the shared adapter registry.
func (r *WorkerRuntime) Registry() *adapter.Registry { return r.registry }

// Selector returns the adapter selector bound to the shared registry.
func (r *WorkerRuntime) Selector() *adapter.Selector { return r.selector }

// Configurations returns a clone of the structured binding outcomes in the
// frozen order. Mutating the returned slice cannot affect the runtime.
func (r *WorkerRuntime) Configurations() []WorkerConfiguration {
	result := make([]WorkerConfiguration, len(r.configurations))
	copy(result, r.configurations)
	return result
}

// Discovery source labels categorize where a candidate binary was found. They
// are advisory metadata only; similar-name candidates must never be suggested.
const (
	DiscoverySourcePath          = "path"
	DiscoverySourceKnownLocation = "known-location"
	DiscoverySourceSimilarName   = "similar-name"
)

// discoverCommandTimeout bounds helper commands executed during advisory
// discovery (such as `npm root -g`). Candidate binaries are bounded inside
// each adapter's Identify.
const discoverCommandTimeout = 10 * time.Second

// discoveryKnownLocationsVariable optionally overrides the built-in known
// location list. "-" disables known locations entirely so tests can pin a
// hermetic scan scope. The variable never changes registration semantics.
const discoveryKnownLocationsVariable = "MARSHAL_DISCOVERY_KNOWN_LOCATIONS"

// DiscoveryCandidate is one located candidate for a known Worker binary. The
// candidate is identified by realpath and SHA256 and its version is read via
// `<bin> --version`; discovery never executes anything else against it.
type DiscoveryCandidate struct {
	Path     string `json:"path"`
	Realpath string `json:"realpath"`
	SHA256   string `json:"sha256"`
	Version  string `json:"version"`
	Source   string `json:"source"`
}

// Discovery is the advisory discovery outcome of one adapter binding. Only
// bindings whose environment variable is unset participate. Discovery results
// never enter CapabilitySnapshot, never change registration, and never relax
// the fail-closed semantics of planning.
type Discovery struct {
	AdapterID           string               `json:"adapterId"`
	EnvironmentVariable string               `json:"environmentVariable"`
	Candidates          []DiscoveryCandidate `json:"candidates"`
	SuggestedEnv        string               `json:"suggestedEnv"`
}

// DiscoverWorkers scans PATH directories and a fixed set of common install
// locations for known Worker binaries and returns advisory discovery results
// for every adapter whose environment variable is unset. Discovery never
// writes environment variables, shell profiles, or files, and never
// registers an adapter: it only suggests an export line for the operator to
// paste. Candidates whose `--version` execution fails are silently skipped,
// similar-name guessing never happens, and discovery never recurses through
// the disk.
func DiscoverWorkers(ctx context.Context, getenv func(string) string) []Discovery {
	if ctx == nil || ctx.Err() != nil || getenv == nil {
		return nil
	}
	directories := discoveryScanDirectories(getenv)
	results := []Discovery{}
	for _, binding := range workerBindings {
		if strings.TrimSpace(getenv(binding.environmentVariable)) != "" {
			continue
		}
		discovery := Discovery{
			AdapterID:           binding.adapterID,
			EnvironmentVariable: binding.environmentVariable,
			Candidates:          []DiscoveryCandidate{},
		}
		seen := map[string]bool{}
		for _, directory := range directories {
			for _, name := range binding.binaryNames {
				path := filepath.Join(directory.path, name)
				realpath, err := filepath.EvalSymlinks(path)
				if err != nil || seen[realpath] {
					continue
				}
				seen[realpath] = true
				version, digest, err := binding.identify(realpath)
				if err != nil {
					continue
				}
				discovery.Candidates = append(discovery.Candidates, DiscoveryCandidate{
					Path:     path,
					Realpath: realpath,
					SHA256:   digest,
					Version:  version,
					Source:   directory.source,
				})
				if discovery.SuggestedEnv == "" && directory.source != DiscoverySourceSimilarName {
					discovery.SuggestedEnv = realpath
				}
			}
		}
		results = append(results, discovery)
	}
	return results
}

type discoveryDirectory struct {
	path   string
	source string
}

// discoveryScanDirectories builds the ordered, de-duplicated directory list
// advisory discovery scans: PATH entries first, then common install
// locations. The list is deterministic and never derived from guessing.
func discoveryScanDirectories(getenv func(string) string) []discoveryDirectory {
	var directories []discoveryDirectory
	seen := map[string]bool{}
	add := func(path, source string) {
		if path == "" {
			return
		}
		key := filepath.Clean(path)
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			key = resolved
		}
		if seen[key] {
			return
		}
		seen[key] = true
		directories = append(directories, discoveryDirectory{path: path, source: source})
	}
	for _, entry := range filepath.SplitList(getenv("PATH")) {
		if entry != "" {
			add(entry, DiscoverySourcePath)
		}
	}
	if override := getenv(discoveryKnownLocationsVariable); override != "" {
		if override != "-" {
			for _, entry := range filepath.SplitList(override) {
				if entry != "" {
					add(entry, DiscoverySourceKnownLocation)
				}
			}
		}
		return directories
	}
	home := getenv("HOME")
	if home != "" {
		add(filepath.Join(home, ".local", "bin"), DiscoverySourceKnownLocation)
	}
	add("/opt/homebrew/bin", DiscoverySourceKnownLocation)
	if home != "" {
		add(filepath.Join(home, ".opencode", "bin"), DiscoverySourceKnownLocation)
		add(filepath.Join(home, ".local", "share", "fnm", "aliases", "default", "bin"), DiscoverySourceKnownLocation)
		add(filepath.Join(home, "Library", "Application Support", "fnm", "aliases", "default", "bin"), DiscoverySourceKnownLocation)
	}
	if fnmDir := getenv("FNM_DIR"); fnmDir != "" {
		add(filepath.Join(fnmDir, "aliases", "default", "bin"), DiscoverySourceKnownLocation)
	}
	add(npmGlobalBinDirectory(getenv), DiscoverySourceKnownLocation)
	return directories
}

// npmGlobalBinDirectory resolves the node global bin directory via
// `npm root -g` on a best-effort basis. Any failure returns an empty string
// and discovery silently skips the location.
func npmGlobalBinDirectory(getenv func(string) string) string {
	npm := lookupInPath("npm", getenv("PATH"))
	if npm == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), discoverCommandTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, npm, "root", "-g")
	command.Env = discoveryEnvironment(getenv)
	output, err := command.Output()
	if err != nil {
		return ""
	}
	root := strings.TrimSpace(string(output))
	if !filepath.IsAbs(root) {
		return ""
	}
	return filepath.Join(filepath.Dir(filepath.Dir(root)), "bin")
}

func lookupInPath(name, pathValue string) string {
	for _, entry := range filepath.SplitList(pathValue) {
		if entry == "" {
			continue
		}
		path := filepath.Join(entry, name)
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			continue
		}
		return path
	}
	return ""
}

// discoveryEnvironment mirrors the sanitized probe environment so helper
// commands executed during discovery only see PATH, HOME, TMPDIR, and LANG.
func discoveryEnvironment(getenv func(string) string) []string {
	var environment []string
	for _, key := range []string{"PATH", "HOME", "TMPDIR", "LANG"} {
		if value := getenv(key); value != "" {
			environment = append(environment, key+"="+value)
		}
	}
	return environment
}
