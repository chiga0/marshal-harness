package selfidentity

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const testSourceHead = "0123456789abcdef0123456789abcdef01234567"

var testNow = time.Date(2026, 8, 27, 8, 0, 0, 0, time.UTC)

func TestNonDarwinLocalProfileFailsClosed(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("non-Darwin behavior")
	}
	_, err := Admit("/missing", CommandDoctor, ".", BuildIdentity{SourceHead: testSourceHead, SelfProfile: LocalProfile}, testNow)
	if ReasonCode(err) != ReasonProfileMismatch {
		t.Fatalf("reason = %q, want %q", ReasonCode(err), ReasonProfileMismatch)
	}
}

func TestDarwinActivationAndObservationPositivePath(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin current-path observation")
	}
	root := canonicalTempDir(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	raw := renderTestActivation(t, root, executable)
	if canonicalRaw, err := canonical.JSON(raw); err != nil || !bytes.Equal(raw, canonicalRaw) {
		t.Fatalf("bootstrap output is not exact JCS: err=%v", err)
	}
	activation, err := DecodeActivation(raw, testNow)
	if err != nil {
		t.Fatalf("DecodeActivation: %v", err)
	}
	activationPath := writeActivation(t, root, raw)
	observation, err := admit(activationPath, CommandTaskScaffold, root, executable,
		BuildIdentity{SourceHead: testSourceHead, SelfProfile: LocalProfile}, testNow, nil)
	if err != nil {
		t.Fatalf("admit production path: %v", err)
	}
	if observation.Status != "pass" || observation.ReasonCode != ReasonObserved ||
		observation.ActivationDigest != activation.ActivationDigest || !validDigest(observation.IdentitySubjectDigest) ||
		!validDigest(observation.ObservationDigest) || observation.CurrentPathObject.ObservationKind != "darwin-current-path-fd-object" {
		t.Fatalf("unexpected observation: %+v", observation)
	}
	validateSchemaDocument(t, raw)
	observationRaw, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	validateSchemaDocument(t, observationRaw)
}

func TestDarwinActivationStrictAndIdentityNegativeMatrix(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin current-path observation")
	}
	root := canonicalTempDir(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	raw := renderTestActivation(t, root, executable)
	activation, err := DecodeActivation(raw, testNow)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("trailing newline", func(t *testing.T) {
		_, err := DecodeActivation(append(append([]byte(nil), raw...), '\n'), testNow)
		assertReason(t, err, ReasonOptInMissing)
	})
	t.Run("duplicate member", func(t *testing.T) {
		duplicate := append([]byte(`{"schemaVersion":"marshal.local-dogfood-activation.v1",`), raw[1:]...)
		_, err := DecodeActivation(duplicate, testNow)
		assertReason(t, err, ReasonOptInMissing)
	})
	t.Run("unknown member", func(t *testing.T) {
		unknown := append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"unknown":true}`)...)
		unknown, _ = canonical.JSON(unknown)
		_, err := DecodeActivation(unknown, testNow)
		assertReason(t, err, ReasonOptInMissing)
	})
	t.Run("expired", func(t *testing.T) {
		_, err := DecodeActivation(raw, testNow.Add(9*time.Hour))
		assertReason(t, err, ReasonOptInMissing)
	})
	t.Run("source drift", func(t *testing.T) {
		path := writeActivation(t, canonicalTempDir(t), raw)
		_, err := admit(path, CommandDoctor, root, executable,
			BuildIdentity{SourceHead: strings.Repeat("a", 40), SelfProfile: LocalProfile}, testNow, nil)
		assertReason(t, err, ReasonSourceMismatch)
	})
	t.Run("profile drift", func(t *testing.T) {
		path := writeActivation(t, canonicalTempDir(t), raw)
		_, err := admit(path, CommandDoctor, root, executable,
			BuildIdentity{SourceHead: testSourceHead, SelfProfile: "unprofiled"}, testNow, nil)
		assertReason(t, err, ReasonProfileMismatch)
	})
	t.Run("size drift", func(t *testing.T) {
		candidate := activation
		candidate.ExpectedSize++
		path := writeActivation(t, canonicalTempDir(t), marshalActivation(t, candidate))
		_, err := admit(path, CommandDoctor, root, executable,
			BuildIdentity{SourceHead: testSourceHead, SelfProfile: LocalProfile}, testNow, nil)
		assertReason(t, err, ReasonObjectMismatch)
	})
	t.Run("hash drift", func(t *testing.T) {
		candidate := activation
		candidate.ExpectedRawSHA256 = "sha256:" + strings.Repeat("0", 64)
		path := writeActivation(t, canonicalTempDir(t), marshalActivation(t, candidate))
		_, err := admit(path, CommandDoctor, root, executable,
			BuildIdentity{SourceHead: testSourceHead, SelfProfile: LocalProfile}, testNow, nil)
		assertReason(t, err, ReasonObjectMismatch)
	})
	t.Run("symlink executable", func(t *testing.T) {
		directory := canonicalTempDir(t)
		link := filepath.Join(directory, "marshal-link")
		if err := os.Symlink(executable, link); err != nil {
			t.Fatal(err)
		}
		candidate := activation
		candidate.CanonicalExecutablePath = link
		_, err := DecodeActivation(marshalActivation(t, candidate), testNow)
		assertReason(t, err, ReasonObjectMismatch)
	})
	t.Run("nonregular executable", func(t *testing.T) {
		candidate := activation
		candidate.CanonicalExecutablePath = root
		_, err := DecodeActivation(marshalActivation(t, candidate), testNow)
		assertReason(t, err, ReasonObjectMismatch)
	})
	t.Run("activation symlink", func(t *testing.T) {
		directory := canonicalTempDir(t)
		realPath := writeActivation(t, directory, raw)
		link := filepath.Join(directory, "activation-link.json")
		if err := os.Symlink(realPath, link); err != nil {
			t.Fatal(err)
		}
		_, err := readActivation(link)
		assertReason(t, err, ReasonOptInMissing)
	})
	t.Run("activation pathname swap", func(t *testing.T) {
		directory := canonicalTempDir(t)
		path := writeActivation(t, directory, raw)
		_, err := readActivationWithHook(path, func() {
			if renameErr := os.Rename(path, path+".old"); renameErr != nil {
				t.Fatal(renameErr)
			}
			if writeErr := os.WriteFile(path, raw, 0o600); writeErr != nil {
				t.Fatal(writeErr)
			}
		})
		assertReason(t, err, ReasonOptInMissing)
	})
	t.Run("executable pathname recheck", func(t *testing.T) {
		directory := canonicalTempDir(t)
		copyPath := filepath.Join(directory, "marshal-copy")
		if writeErr := os.WriteFile(copyPath, []byte("#!/bin/sh\nexit 0\n"), 0o700); writeErr != nil {
			t.Fatal(writeErr)
		}
		copyRaw := renderTestActivation(t, root, copyPath)
		path := writeActivation(t, canonicalTempDir(t), copyRaw)
		_, err := admit(path, CommandDoctor, root, copyPath,
			BuildIdentity{SourceHead: testSourceHead, SelfProfile: LocalProfile}, testNow, func() {
				if renameErr := os.Rename(copyPath, copyPath+".old"); renameErr != nil {
					t.Fatal(renameErr)
				}
				if writeErr := os.WriteFile(copyPath, []byte("replacement"), 0o700); writeErr != nil {
					t.Fatal(writeErr)
				}
			})
		assertReason(t, err, ReasonObjectMismatch)
	})
}

func TestDarwinRejectionsReturnOnlyTypedError(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin current-path observation")
	}
	root := canonicalTempDir(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	raw := renderTestActivation(t, root, executable)
	activation, err := DecodeActivation(raw, testNow)
	if err != nil {
		t.Fatal(err)
	}
	activationPath := writeActivation(t, canonicalTempDir(t), raw)

	tests := []struct {
		name       string
		path       string
		build      BuildIdentity
		activation *LocalDogfoodActivationV1
		wantReason string
	}{
		{name: "opt-in missing", path: filepath.Join(root, "missing.json"), build: BuildIdentity{SourceHead: testSourceHead, SelfProfile: LocalProfile}, wantReason: ReasonOptInMissing},
		{name: "profile mismatch", path: activationPath, build: BuildIdentity{SourceHead: testSourceHead, SelfProfile: "unprofiled"}, wantReason: ReasonProfileMismatch},
		{name: "object mismatch", build: BuildIdentity{SourceHead: testSourceHead, SelfProfile: LocalProfile}, activation: &activation, wantReason: ReasonObjectMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := test.path
			if test.activation != nil {
				candidate := *test.activation
				candidate.ExpectedSize++
				path = writeActivation(t, canonicalTempDir(t), marshalActivation(t, candidate))
			}
			observation, err := admit(path, CommandDoctor, root, executable, test.build, testNow, nil)
			assertReason(t, err, test.wantReason)
			if observation != (LocalSelfIdentityObservationV1{}) {
				t.Fatalf("rejection returned a versioned observation: %+v", observation)
			}
		})
	}
}

func renderTestActivation(t *testing.T, root, executable string) []byte {
	t.Helper()
	raw, err := RenderActivation(BootstrapOptions{
		RepositoryRoot: root, ActivationID: "local-test", IssuedAt: testNow,
		ValidUntil: testNow.Add(8 * time.Hour), ExecutablePath: executable,
		Build: BuildIdentity{SourceHead: testSourceHead, SelfProfile: LocalProfile},
	})
	if err != nil {
		t.Fatalf("RenderActivation: %v", err)
	}
	return raw
}

func marshalActivation(t *testing.T, activation LocalDogfoodActivationV1) []byte {
	t.Helper()
	var err error
	activation.ActivationDigest, err = digestActivation(activation)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(activation)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = canonical.JSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func writeActivation(t *testing.T, directory string, raw []byte) string {
	t.Helper()
	path := filepath.Join(directory, "activation.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func assertReason(t *testing.T, err error, want string) {
	t.Helper()
	if ReasonCode(err) != want {
		t.Fatalf("reason = %q, want %q (err=%v)", ReasonCode(err), want, err)
	}
}

func validateSchemaDocument(t *testing.T, raw []byte) {
	t.Helper()
	schemaRaw, err := os.ReadFile(filepath.Join("..", "..", "schemas", "selfidentity", "local-dogfood.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaRaw))
	if err != nil {
		t.Fatalf("decode Draft 2020-12 schema: %v", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	if err := compiler.AddResource("https://marshal.dev/schemas/selfidentity/local-dogfood.schema.json", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("https://marshal.dev/schemas/selfidentity/local-dogfood.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(value); err != nil {
		t.Fatalf("schema validation: %v", err)
	}
}
