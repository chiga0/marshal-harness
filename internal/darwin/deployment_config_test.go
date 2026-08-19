package darwin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadLaunchdDeploymentConfigRejectsUntrustedShapes(t *testing.T) {
	for name, raw := range map[string]string{
		"unknown field": `{"schemaVersion":"marshal.darwin.launchd-deployment.v1","spec":{},"policy":{},"extra":true}`,
		"noncanonical":  `{"policy":{},"spec":{},"schemaVersion":"marshal.darwin.launchd-deployment.v1"}`,
		"wrong owner":   `{"schemaVersion":"wrong","spec":{},"policy":{"ownerUid":501}}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(resolvedTempDir(t), "deployment.json")
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadLaunchdDeploymentConfig(path); err == nil {
				t.Fatal("untrusted launchd config was accepted")
			}
		})
	}
}

func TestLoadLaunchdDeploymentConfigRejectsSymlinkedParent(t *testing.T) {
	root := resolvedTempDir(t)
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(link, "deployment.json")
	if err := os.WriteFile(filepath.Join(target, "deployment.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLaunchdDeploymentConfig(path); err == nil {
		t.Fatal("launchd config followed a symlinked parent")
	}
}

func TestLoadLaunchdDeploymentConfigAcceptsClosedPrivateRecord(t *testing.T) {
	path := filepath.Join(resolvedTempDir(t), "deployment.json")
	raw := `{"policy":{"launcher":{"cdHash":"launcher-cdhash","identifier":"com.marshal.launcher","sha256":"launcher-sha","teamId":"TEAM"},"ownerUid":0,"service":{"cdHash":"service-cdhash","identifier":"com.marshal.apap","sha256":"service-sha","teamId":"TEAM"}},"schemaVersion":"marshal.darwin.launchd-deployment.v1","spec":{"endpoint":"/private/var/run/marshal-apap.sock","label":"com.marshal.apap","launcherBinary":"/Library/PrivilegedHelperTools/marshal-darwin-launcher","serviceBinary":"/Library/PrivilegedHelperTools/marshal-apap"}}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadLaunchdDeploymentConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.SchemaVersion != launchdDeploymentConfigSchema || config.Policy.OwnerUID != 0 || config.Spec.Label != DefaultAuthorityLabel {
		t.Fatalf("config = %+v", config)
	}
}

func resolvedTempDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}
