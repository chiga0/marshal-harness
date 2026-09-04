//go:build darwin && arm64

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/fixedcontrolplane"
	"github.com/chiga0/marshal-harness/internal/productionruntime"
	"github.com/chiga0/marshal-harness/internal/repository"
	"github.com/chiga0/marshal-harness/internal/selfidentity"
)

const (
	controlPlaneDrainTimeout  = 30 * time.Second
	controlPlaneCancelTimeout = 30 * time.Second
)

func runControlPlane(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "用法：marshal control-plane <serve|status|inspect|start|collect|verify|review-packet|decision>")
		return ExitUsage
	}
	switch args[0] {
	case "serve":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "用法：marshal control-plane serve")
			return ExitUsage
		}
		return runControlPlaneServe(ctx, stdout, stderr)
	case "status":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "用法：marshal control-plane status")
			return ExitUsage
		}
		return runControlPlaneStatus(ctx, stdout, stderr)
	case "inspect":
		return runControlPlaneInspect(ctx, args[1:], stdout, stderr)
	case "start":
		return runControlPlaneStart(ctx, args[1:], stdout, stderr)
	case "collect":
		return runControlPlaneCollect(ctx, args[1:], stdout, stderr)
	case "verify":
		return runControlPlaneVerify(ctx, args[1:], stdout, stderr)
	case "review-packet":
		return runControlPlaneReviewPacket(ctx, args[1:], stdout, stderr)
	case "decision":
		return runControlPlaneDecision(ctx, args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, "用法：marshal control-plane <serve|status|inspect|start|collect|verify|review-packet|decision>")
		return ExitUsage
	}
}

func runControlPlaneServe(ctx context.Context, stdout, stderr io.Writer) int {
	location, err := repository.Discover(".")
	if err != nil {
		fmt.Fprintln(stderr, "control-plane serve 失败：无法验证仓库。")
		return ExitUnavailable
	}
	entryIdentity := localDogfoodObservation(ctx)
	piRuntime, piEntrypoint := os.Getenv("MARSHAL_PI_RUNTIME"), os.Getenv("MARSHAL_PI_ENTRYPOINT")
	if entryIdentity == nil || piRuntime == "" || piEntrypoint == "" {
		fmt.Fprintln(stderr, "control-plane serve 失败：缺少 fixed activation 或 Pi 0.84.4 配置。")
		return ExitUnavailable
	}
	applicationAdapter, err := openSealedRepositoryApplication(ctx, sealedRepositoryApplicationConfig{
		StateRoot: location.StateRoot, RepositoryRoot: location.RepositoryRoot,
		PiRuntime: piRuntime, PiEntrypoint: piEntrypoint, EntryIdentity: entryIdentity,
		RecoveryMode: sealedRepositoryRecoveryResident,
		ObserveIdentity: func() (selfidentity.LocalSelfIdentityObservationV2, error) {
			return freshLocalDogfoodObservation(selfidentity.CommandControlPlaneServe)
		},
	})
	if err != nil {
		fmt.Fprintf(stderr, "control-plane serve 失败：compositionStage=%s。\n", sealedRepositoryOpenStage(err))
		writeControlPlaneRequestFailure(stderr, err)
		return ExitFailure
	}
	endpointAuthority, err := applicationAdapter.session.OpenFixedEndpointAuthority(ctx)
	if err != nil {
		_ = applicationAdapter.Close()
		fmt.Fprintln(stderr, "control-plane serve 失败：endpoint authority 不可用。")
		return ExitFailure
	}
	delivery, err := applicationAdapter.session.OpenFixedDeliveryStore(ctx)
	if err != nil {
		_ = endpointAuthority.Close()
		_ = applicationAdapter.Close()
		fmt.Fprintln(stderr, "control-plane serve 失败：delivery store 不可用。")
		return ExitFailure
	}
	router, err := fixedcontrolplane.NewHTTPRouter(applicationAdapter, delivery)
	if err != nil {
		_ = delivery.Close()
		_ = endpointAuthority.Close()
		_ = applicationAdapter.Close()
		return ExitFailure
	}
	endpoint, err := fixedcontrolplane.OpenEndpoint(ctx, endpointAuthority)
	if err != nil {
		_ = delivery.Close()
		_ = endpointAuthority.Close()
		_ = applicationAdapter.Close()
		fmt.Fprintln(stderr, "control-plane serve 失败：authenticated endpoint 不可用。")
		return ExitFailure
	}
	ready, _ := json.Marshal(map[string]any{"availability": "ready", "protocolRevision": fixedcontrolplane.ProtocolRevision})
	fmt.Fprintln(stdout, string(ready))

	requestCtx, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()
	var requests sync.WaitGroup
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = endpoint.StopAccept()
		case <-stop:
		}
	}()
	serveFailed := false
	for ctx.Err() == nil {
		connection, acceptErr := endpoint.Accept(ctx)
		if acceptErr != nil {
			if ctx.Err() != nil {
				break
			}
			if endpointAuthority.Recheck(ctx) != nil {
				serveFailed = true
				break
			}
			continue
		}
		if ctx.Err() != nil {
			_ = connection.Close()
			break
		}
		requests.Add(1)
		go func() {
			defer requests.Done()
			defer connection.Close()
			if requestErr := router.ServeAuthenticated(requestCtx, connection); requestErr != nil {
				writeControlPlaneRequestFailure(stderr, requestErr)
			}
		}()
	}
	close(stop)
	stopErr := endpoint.StopAccept()
	if !drainControlPlaneRequests(&requests, cancelRequests, controlPlaneDrainTimeout, controlPlaneCancelTimeout) {
		// The process returns without releasing owner resources. main exits the
		// whole process, so no stuck application goroutine can outlive owner.
		fmt.Fprintln(stderr, "control-plane serve 失败：shutdown cancel 后仍未停止，进入 fail-stop。")
		return ExitFailure
	}
	closeErr := errors.Join(stopErr, endpoint.Close())
	closeErr = errors.Join(closeErr, delivery.Close(), endpointAuthority.Close(), applicationAdapter.Close())
	if serveFailed || closeErr != nil {
		fmt.Fprintln(stderr, "control-plane serve 失败：authority 或 shutdown 不完整。")
		return ExitFailure
	}
	return ExitOK
}

func sealedRepositoryOpenStage(err error) string {
	if err == nil {
		return "none"
	}
	message := err.Error()
	for _, stage := range []string{
		"open owner scope", "open result ingress", "acquire owner", "close owner phase", "claim owner",
		"open fixed root", "open runstore", "seal prepared execution", "validate fixed root",
	} {
		if strings.Contains(message, "repository session: "+stage+":") {
			return "repository-session-" + strings.ReplaceAll(stage, " ", "-")
		}
	}
	for _, stage := range []string{
		"resolve Pi runtime", "resolve Pi entrypoint", "locate fixed marshal", "resolve fixed marshal",
		"prepare authority directory", "compile contracts", "open owner directory", "open ingress directory",
		"open control root", "open provider directory", "open state root", "bind run store", "bind canonical repository root",
		"open dispatch ledger", "observe owner acquisition", "observe attestation", "open repository session",
		"open provider authority", "construct provider authority", "persist provider authority", "observe Pi identity",
		"derive Pi identity", "construct Pi status profile", "recover repository runs",
	} {
		if strings.Contains(message, "sealed repository application: "+stage+":") {
			return strings.ReplaceAll(stage, " ", "-")
		}
	}
	return "unknown"
}

// writeControlPlaneRequestFailure deliberately emits only the typed
// application operation/reason pair. Raw errors may contain host paths or
// provider details and therefore never cross this diagnostic boundary.
func writeControlPlaneRequestFailure(stderr io.Writer, err error) {
	var applicationError *application.Error
	if errors.As(err, &applicationError) {
		fmt.Fprintf(stderr, "control-plane request failed: operation=%s reasonCode=%s\n", applicationError.Operation, applicationError.Reason)
		return
	}
	fmt.Fprintln(stderr, "control-plane request failed: reasonCode=transport-failure")
}

func drainControlPlaneRequests(requests *sync.WaitGroup, cancel context.CancelFunc, drainTimeout, cancelTimeout time.Duration) bool {
	drained := make(chan struct{})
	go func() {
		requests.Wait()
		close(drained)
	}()
	if waitForControlPlaneDrain(drained, drainTimeout) {
		return true
	}
	cancel()
	return waitForControlPlaneDrain(drained, cancelTimeout)
}

func waitForControlPlaneDrain(drained <-chan struct{}, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-drained:
		return true
	case <-timer.C:
		return false
	}
}

func openControlPlaneClient(ctx context.Context) (*productionruntime.FixedEndpointAuthority, error) {
	location, err := repository.Discover(".")
	if err != nil {
		return nil, err
	}
	return productionruntime.OpenFixedEndpointClientAuthority(ctx, location.RepositoryRoot)
}

func controlPlaneReadKey(operation, runID string) string {
	return fmt.Sprintf("%s-%s-%d-%d", operation, runID, os.Getpid(), time.Now().UnixNano())
}

func runControlPlaneStatus(ctx context.Context, stdout, stderr io.Writer) int {
	authority, err := openControlPlaneClient(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "control-plane status 失败：resident server 不可用。")
		return ExitUnavailable
	}
	defer authority.Close()
	projection, err := fixedcontrolplane.CallStatus(ctx, authority, controlPlaneReadKey("status", "repository"), time.Now().UTC().Add(2*time.Minute))
	if err != nil {
		fmt.Fprintln(stderr, "control-plane status 失败：authenticated request 未完成。")
		return ExitFailure
	}
	return writeControlPlaneJSON(stdout, stderr, projection)
}

func runControlPlaneInspect(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("control-plane inspect", flag.ContinueOnError)
	flags.SetOutput(stderr)
	runID := flags.String("run", "", "Run ID")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *runID == "" {
		fmt.Fprintln(stderr, "用法：marshal control-plane inspect --run RUN_ID")
		return ExitUsage
	}
	authority, err := openControlPlaneClient(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "control-plane inspect 失败：resident server 不可用。")
		return ExitUnavailable
	}
	defer authority.Close()
	projection, err := fixedcontrolplane.CallInspectRun(ctx, authority, controlPlaneReadKey("inspect", *runID), application.InspectRunRequest{RunID: *runID}, time.Now().UTC().Add(2*time.Minute))
	if err != nil {
		fmt.Fprintln(stderr, "control-plane inspect 失败：authenticated request 未完成。")
		return ExitFailure
	}
	return writeControlPlaneJSON(stdout, stderr, projection)
}

func runControlPlaneStart(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("control-plane start", flag.ContinueOnError)
	flags.SetOutput(stderr)
	runID := flags.String("run", "", "Run ID")
	sequence := flags.Uint64("expected-sequence", 0, "expected durable sequence")
	head := flags.String("expected-authority-head", "", "expected authority head")
	requestKey := flags.String("request-key", "", "stable idempotency key")
	deadlineRaw := flags.String("deadline", "", "frozen UTC RFC3339Nano application deadline")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *runID == "" || *sequence == 0 || *head == "" || *requestKey == "" || *deadlineRaw == "" {
		fmt.Fprintln(stderr, "用法：marshal control-plane start --run RUN_ID --expected-sequence N --expected-authority-head DIGEST --request-key KEY --deadline UTC_RFC3339NANO")
		return ExitUsage
	}
	deadline, err := parseControlPlaneDeadline(*deadlineRaw, time.Now().UTC())
	if err != nil {
		fmt.Fprintln(stderr, "control-plane start 失败：deadline 必须是未来十分钟内的 canonical UTC RFC3339Nano；重试必须原样复用。")
		return ExitUsage
	}
	authority, err := openControlPlaneClient(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "control-plane start 失败：resident server 不可用。")
		return ExitUnavailable
	}
	defer authority.Close()
	result, err := fixedcontrolplane.CallStartRun(ctx, authority, *requestKey, application.StartRunRequest{RunID: *runID, ExpectedSequence: *sequence, ExpectedAuthorityHead: *head}, deadline)
	if err != nil {
		fmt.Fprintln(stderr, "control-plane start 失败：结果未证明成功；请使用同一 request key 与冻结请求重放。")
		return ExitFailure
	}
	return writeControlPlaneJSON(stdout, stderr, result)
}

type controlPlaneCurrentInput struct {
	current      application.CurrentRunRequest
	requestKey   string
	deadline     time.Time
	decisionPath string
}

func parseControlPlaneCurrentInput(command string, args []string, stderr io.Writer, decision bool) (controlPlaneCurrentInput, int) {
	flags := flag.NewFlagSet("control-plane "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	runID := flags.String("run", "", "Run ID")
	attemptID := flags.String("attempt", "", "Attempt ID")
	sequence := flags.Uint64("expected-sequence", 0, "expected durable sequence")
	head := flags.String("expected-authority-head", "", "expected authority head")
	requestKey := flags.String("request-key", "", "stable idempotency key")
	deadlineRaw := flags.String("deadline", "", "frozen UTC RFC3339Nano application deadline")
	decisionPath := new(string)
	if decision {
		decisionPath = flags.String("decision", "", "external ReviewDecision JSON path")
	}
	usage := fmt.Sprintf("用法：marshal control-plane %s --run RUN_ID --attempt ATTEMPT_ID --expected-sequence N --expected-authority-head DIGEST --request-key KEY --deadline UTC_RFC3339NANO", command)
	if decision {
		usage += " --decision PATH"
	}
	if flags.Parse(args) != nil || flags.NArg() != 0 || *runID == "" || *attemptID == "" || *sequence == 0 || *head == "" || *requestKey == "" || *deadlineRaw == "" || decision && *decisionPath == "" {
		fmt.Fprintln(stderr, usage)
		return controlPlaneCurrentInput{}, ExitUsage
	}
	deadline, err := parseControlPlaneDeadline(*deadlineRaw, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(stderr, "control-plane %s 失败：deadline 必须是未来十分钟内的 canonical UTC RFC3339Nano；重试必须原样复用。\n", command)
		return controlPlaneCurrentInput{}, ExitUsage
	}
	return controlPlaneCurrentInput{current: application.CurrentRunRequest{RunID: *runID, AttemptID: *attemptID, ExpectedSequence: *sequence, ExpectedAuthorityHead: *head}, requestKey: *requestKey, deadline: deadline, decisionPath: *decisionPath}, ExitOK
}

func runControlPlaneCollect(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	input, exit := parseControlPlaneCurrentInput("collect", args, stderr, false)
	if exit != ExitOK {
		return exit
	}
	authority, err := openControlPlaneClient(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "control-plane collect 失败：resident server 不可用。")
		return ExitUnavailable
	}
	defer authority.Close()
	result, err := fixedcontrolplane.CallCollectRunResult(ctx, authority, input.requestKey, application.CollectRunResultRequest(input.current), input.deadline)
	if err != nil {
		fmt.Fprintln(stderr, "control-plane collect 失败：结果未证明成功；请使用同一 request key 与冻结请求重放。")
		return ExitFailure
	}
	return writeControlPlaneJSON(stdout, stderr, result)
}

func runControlPlaneVerify(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	input, exit := parseControlPlaneCurrentInput("verify", args, stderr, false)
	if exit != ExitOK {
		return exit
	}
	authority, err := openControlPlaneClient(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "control-plane verify 失败：resident server 不可用。")
		return ExitUnavailable
	}
	defer authority.Close()
	result, err := fixedcontrolplane.CallVerifyRun(ctx, authority, input.requestKey, application.VerifyRunRequest(input.current), input.deadline)
	if err != nil {
		fmt.Fprintln(stderr, "control-plane verify 失败：结果未证明成功；请使用同一 request key 与冻结请求重放。")
		return ExitFailure
	}
	return writeControlPlaneJSON(stdout, stderr, result)
}

func runControlPlaneReviewPacket(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	input, exit := parseControlPlaneCurrentInput("review-packet", args, stderr, false)
	if exit != ExitOK {
		return exit
	}
	authority, err := openControlPlaneClient(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "control-plane review-packet 失败：resident server 不可用。")
		return ExitUnavailable
	}
	defer authority.Close()
	result, err := fixedcontrolplane.CallBuildReviewPacket(ctx, authority, input.requestKey, application.BuildReviewPacketRequest(input.current), input.deadline)
	if err != nil {
		fmt.Fprintln(stderr, "control-plane review-packet 失败：结果未证明成功；请使用同一 request key 与冻结请求重放。")
		return ExitFailure
	}
	return writeControlPlaneJSON(stdout, stderr, result)
}

func runControlPlaneDecision(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	input, exit := parseControlPlaneCurrentInput("decision", args, stderr, true)
	if exit != ExitOK {
		return exit
	}
	decisionFile, err := os.Open(input.decisionPath)
	if err != nil {
		fmt.Fprintln(stderr, "control-plane decision 失败：无法读取 Decision。")
		return ExitUsage
	}
	decisionRaw, readErr := readBounded(decisionFile, 1<<20)
	closeErr := decisionFile.Close()
	decisionRaw, canonicalErr := canonical.JSON(decisionRaw)
	if readErr != nil || closeErr != nil || canonicalErr != nil {
		fmt.Fprintln(stderr, "control-plane decision 失败：Decision 必须是有界 canonical JSON。")
		return ExitUsage
	}
	authority, err := openControlPlaneClient(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "control-plane decision 失败：resident server 不可用。")
		return ExitUnavailable
	}
	defer authority.Close()
	request := application.ApplyReviewDecisionRequest{RunID: input.current.RunID, AttemptID: input.current.AttemptID, ExpectedSequence: input.current.ExpectedSequence, ExpectedAuthorityHead: input.current.ExpectedAuthorityHead, Decision: decisionRaw, DecisionDigest: canonical.DigestBytes(decisionRaw)}
	result, err := fixedcontrolplane.CallApplyReviewDecision(ctx, authority, input.requestKey, request, input.deadline)
	if err != nil {
		fmt.Fprintln(stderr, "control-plane decision 失败：结果未证明成功；请使用同一 request key 与冻结请求重放。")
		return ExitFailure
	}
	return writeControlPlaneJSON(stdout, stderr, result)
}

func parseControlPlaneDeadline(raw string, now time.Time) (time.Time, error) {
	deadline, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil || deadline.Location() != time.UTC || deadline.Format(time.RFC3339Nano) != raw || !deadline.After(now) || deadline.After(now.Add(10*time.Minute)) {
		return time.Time{}, errors.New("invalid frozen deadline")
	}
	return deadline, nil
}

func writeControlPlaneJSON(stdout, stderr io.Writer, value any) int {
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		fmt.Fprintln(stderr, "control-plane 输出失败。")
		return ExitFailure
	}
	return ExitOK
}
