package darwin

import (
	"bytes"
	"encoding/xml"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	DefaultAuthorityLabel    = "com.marshal.apap"
	DefaultAuthorityEndpoint = "/private/var/run/marshal-apap.sock"
)

var launchdLabelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,127}$`)

// LaunchdAuthoritySpec is the non-secret deployment projection for the
// root-owned APAP daemon. It does not sign, install or enable a service.
type LaunchdAuthoritySpec struct {
	Label          string `json:"label"`
	ServiceBinary  string `json:"serviceBinary"`
	LauncherBinary string `json:"launcherBinary"`
	Endpoint       string `json:"endpoint"`
}

func (spec LaunchdAuthoritySpec) validate() error {
	if !launchdLabelPattern.MatchString(spec.Label) {
		return errors.New("darwin launchd label is invalid")
	}
	for name, path := range map[string]string{
		"service":  spec.ServiceBinary,
		"launcher": spec.LauncherBinary,
		"endpoint": spec.Endpoint,
	} {
		if path == "" || strings.IndexByte(path, 0) >= 0 || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
			return errors.New("darwin launchd " + name + " path is invalid")
		}
	}
	return nil
}

// RenderLaunchdPlist returns deterministic XML for a root-owned launchd
// daemon. External provisioning must still verify the signed service and
// launcher identities, install owner/mode, and bootstrap the plist as root.
func (spec LaunchdAuthoritySpec) RenderLaunchdPlist() ([]byte, error) {
	if err := spec.validate(); err != nil {
		return nil, err
	}
	var output bytes.Buffer
	output.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	output.WriteString("<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n")
	output.WriteString("<plist version=\"1.0\"><dict>")
	writePlistString(&output, "Label", spec.Label)
	output.WriteString("<key>ProgramArguments</key><array>")
	writePlistValue(&output, spec.ServiceBinary)
	writePlistValue(&output, "--launcher")
	writePlistValue(&output, spec.LauncherBinary)
	writePlistValue(&output, "--endpoint")
	writePlistValue(&output, spec.Endpoint)
	output.WriteString("</array>")
	output.WriteString("<key>RunAtLoad</key><true/>")
	output.WriteString("<key>KeepAlive</key><true/>")
	output.WriteString("<key>ProcessType</key><string>Background</string>")
	output.WriteString("<key>Umask</key><integer>63</integer>")
	output.WriteString("</dict></plist>\n")
	return output.Bytes(), nil
}

func writePlistString(output *bytes.Buffer, key, value string) {
	output.WriteString("<key>")
	_ = xml.EscapeText(output, []byte(key))
	output.WriteString("</key>")
	writePlistValue(output, value)
}

func writePlistValue(output *bytes.Buffer, value string) {
	output.WriteString("<string>")
	_ = xml.EscapeText(output, []byte(value))
	output.WriteString("</string>")
}
