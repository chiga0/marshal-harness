//go:build darwin && arm64

package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/processsupervisor"
	"github.com/chiga0/marshal-harness/internal/productionruntime"
	"github.com/chiga0/marshal-harness/internal/selfidentity"
)

// runSealedReadyBranch drives one READY/RUNNING Run through the same
// repository application adapter used by fixed server mode. Direct CLI use
// keeps the application lifetime bounded to this operation.
func runSealedReadyBranch(ctx context.Context, stateRoot, repositoryRoot, _, runID string, stdout, stderr io.Writer) int {
	entryLocalSelfIdentity := localDogfoodObservation(ctx)
	if entryLocalSelfIdentity == nil {
		fmt.Fprintln(stderr, "运行失败：sealed local-dogfood 组合缺少命令入口身份观察。")
		return ExitUnavailable
	}
	piRuntime, piEntrypoint := os.Getenv("MARSHAL_PI_RUNTIME"), os.Getenv("MARSHAL_PI_ENTRYPOINT")
	if piRuntime == "" || piEntrypoint == "" {
		fmt.Fprintln(stderr, "运行失败：sealed 组合需要 MARSHAL_PI_RUNTIME 与 MARSHAL_PI_ENTRYPOINT 指向冻结的 Pi 0.84.4 镜像。")
		return ExitUnavailable
	}
	applicationAdapter, err := openSealedRepositoryApplication(ctx, sealedRepositoryApplicationConfig{
		StateRoot: stateRoot, RepositoryRoot: repositoryRoot, PiRuntime: piRuntime, PiEntrypoint: piEntrypoint,
		EntryIdentity: entryLocalSelfIdentity,
		ObserveIdentity: func() (selfidentity.LocalSelfIdentityObservationV2, error) {
			return freshLocalDogfoodObservation(selfidentity.CommandTaskRun)
		},
	})
	if err != nil {
		fmt.Fprintf(stderr, "运行失败：sealed repository application 组装失败：%v\n", err)
		return ExitFailure
	}
	defer applicationAdapter.Close()

	after, stage, err := applicationAdapter.advanceRun(ctx, runID)
	if err != nil {
		fmt.Fprintf(stderr, "运行失败：%s：%v\n", stage, err)
		return ExitFailure
	}
	fmt.Fprintf(stdout, "Run：%s\nAttempt：%s\n状态：%s\n", runID, after.AttemptID, after.State)
	return ExitOK
}

// observeCompositionAcquisition binds acquisition evidence to the exact fixed
// marshal core already resolved by the CLI composition root.
func observeCompositionAcquisition(fixedMarshal string, namespace authority.AuthorityNamespaceId) (productionruntime.ControlOwnerAcquisition, error) {
	core, err := processsupervisor.ObserveCurrentCore(fixedMarshal)
	if err != nil {
		return productionruntime.ControlOwnerAcquisition{}, err
	}
	repositoryIdentityDigest, err := namespace.Digest()
	if err != nil {
		return productionruntime.ControlOwnerAcquisition{}, err
	}
	return productionruntime.ControlOwnerAcquisition{
		Scope:      productionruntime.ControlOwnerScope{AuthorityNamespaceID: namespace, RepositoryIdentityDigest: repositoryIdentityDigest},
		OwnerEpoch: 0,
		OwnerUID:   core.UID, OwnerGID: core.GID, OwnerProcess: core.Process, OwnerBinary: core.Binary,
		ObserverIdentity: "darwin-owner-observer/v1",
		ObservedAt:       time.Unix(core.Process.BirthSeconds, core.Process.BirthMicroseconds*int64(time.Microsecond)).UTC().Add(time.Second).Format(time.RFC3339Nano),
	}, nil
}
