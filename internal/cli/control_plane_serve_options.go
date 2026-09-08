package cli

import (
	"errors"
	"net"
	"path/filepath"
	"strconv"
	"strings"
)

// One closed, side-effect-free parser is shared by the real self-identity
// entry gate and the resident startup adapter. No file/config is read before
// the existing activation check, and aliases/duplicates cannot widen admission.
func controlPlaneServeOptions(args []string) (auto bool, address, template string, err error) {
	invalid := errors.New("invalid control-plane serve options")
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		name := args[i]
		if seen[name] {
			return false, "", "", invalid
		}
		seen[name] = true
		switch name {
		case "--auto-team-progress":
			auto = true
		case "--task-http-address", "--task-template":
			i++
			if i >= len(args) || args[i] == "" || len(args[i]) > 4096 || strings.ContainsRune(args[i], 0) {
				return false, "", "", invalid
			}
			if name == "--task-http-address" {
				address = args[i]
			} else {
				template = args[i]
			}
		default:
			return false, "", "", invalid
		}
	}
	if (address == "") != (template == "") {
		return false, "", "", invalid
	}
	if address != "" {
		host, port, e := net.SplitHostPort(address)
		n, numberErr := strconv.Atoi(port)
		if e != nil || host != "127.0.0.1" || numberErr != nil || n < 0 || n > 65535 || strconv.Itoa(n) != port || !filepath.IsAbs(template) || filepath.Clean(template) != template {
			return false, "", "", invalid
		}
		auto = true
	}
	return auto, address, template, nil
}
