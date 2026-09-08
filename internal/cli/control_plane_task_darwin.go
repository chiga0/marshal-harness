//go:build darwin && arm64

package cli

import (
	"fmt"
	"io"
)

type taskHTTPOptions struct {
	address string
	inputs  []byte
}

// The file is operator-installed configuration, never supplied by a Task
// client. Reuse the bounded, no-follow team envelope reader without granting
// its old request/deadline any approval authority.
func parseControlPlaneServe(args []string, stderr io.Writer) (bool, taskHTTPOptions, int) {
	auto, address, path, err := controlPlaneServeOptions(args)
	if err != nil {
		fmt.Fprintln(stderr, "用法：marshal control-plane serve [--auto-team-progress] [--task-http-address 127.0.0.1:0 --task-template TEAM.json]")
		return false, taskHTTPOptions{}, ExitUsage
	}
	if address == "" {
		return auto, taskHTTPOptions{}, ExitOK
	}
	request, err := readInitialTeamRequest(path)
	if err != nil {
		fmt.Fprintln(stderr, "Task 模板无效：需要有界常规 operator team JSON 文件。")
		return false, taskHTTPOptions{}, ExitUsage
	}
	return true, taskHTTPOptions{address: address, inputs: request.Inputs}, ExitOK
}
