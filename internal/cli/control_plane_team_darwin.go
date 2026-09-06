//go:build darwin && arm64

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/fixedcontrolplane"
	"golang.org/x/sys/unix"
)

func readInitialTeamRequest(path string) (application.ApproveInitialTeamRequest, error) {
	fail := errors.New("invalid initial team request")
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return application.ApproveInitialTeamRequest{}, fail
	}
	file := os.NewFile(uintptr(fd), "team-approval-input")
	if file == nil {
		_ = unix.Close(fd)
		return application.ApproveInitialTeamRequest{}, fail
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return application.ApproveInitialTeamRequest{}, fail
	}
	raw, err := readBounded(file, 1<<20)
	if err != nil {
		return application.ApproveInitialTeamRequest{}, fail
	}
	raw, err = canonical.JSON(raw)
	if err != nil {
		return application.ApproveInitialTeamRequest{}, fail
	}
	var request application.ApproveInitialTeamRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil {
		return application.ApproveInitialTeamRequest{}, fail
	}
	frozen, _, err := request.Frozen()
	return frozen, err
}

func runControlPlaneTeam(ctx context.Context, operation string, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("control-plane "+operation, flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("request-file", "", "frozen complete operator approval request")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *path == "" || (operation != "team-approve" && operation != "team-reconcile") {
		fmt.Fprintln(stderr, "用法：marshal control-plane <team-approve|team-reconcile> --request-file REQUEST.json")
		return ExitUsage
	}
	request, err := readInitialTeamRequest(*path)
	if err != nil {
		fmt.Fprintln(stderr, "团队请求无效：必须是有界常规 JSON 文件，包含原批准摘要、requestId 与冻结 deadline。")
		return ExitUsage
	}
	if operation == "team-approve" {
		if _, err := parseControlPlaneDeadline(request.Deadline, time.Now().UTC()); err != nil {
			fmt.Fprintln(stderr, "批准 deadline 必须在未来十分钟内；过期原请求请使用 team-reconcile 查询，不延长原批准。")
			return ExitUsage
		}
	}
	authority, err := openControlPlaneClient(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "团队请求失败：resident server 不可用。")
		return ExitUnavailable
	}
	defer authority.Close()
	var projection application.InitialTeamApprovalProjection
	found := true
	if operation == "team-approve" {
		projection, err = fixedcontrolplane.CallApproveInitialTeam(ctx, authority, request)
	} else {
		projection, found, err = fixedcontrolplane.CallReconcileInitialTeamApproval(ctx, authority, controlPlaneReadKey("team-reconcile", request.RequestID), request, time.Now().UTC().Add(2*time.Minute))
	}
	if err != nil {
		writeControlPlaneRequestFailure(stderr, err)
		fmt.Fprintln(stderr, "团队批准结果尚未证明；保留原请求，使用 team-reconcile 查询，不自动重新批准。")
		return ExitFailure
	}
	var result *application.InitialTeamApprovalProjection
	if found {
		result = &projection
	}
	return writeControlPlaneJSON(stdout, stderr, struct {
		Found    bool                                       `json:"found"`
		Approval *application.InitialTeamApprovalProjection `json:"approval,omitempty"`
	}{found, result})
}
