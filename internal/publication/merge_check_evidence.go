package publication

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
)

func persistRemoteCheckRecord(runDir, digest string, data []byte) error {
	recomputed, err := canonical.DigestJSON(data)
	if err != nil || recomputed != digest {
		return port.Permanent(errors.New("RemoteCheckRecord digest recomputation mismatch"))
	}
	path := filepath.Join(runDir, "remote-check-records", strings.TrimPrefix(digest, "sha256:")+".json")
	existing, err := os.ReadFile(path)
	if err == nil {
		existingDigest, digestErr := canonical.DigestJSON(existing)
		if digestErr != nil || existingDigest != digest {
			return port.Permanent(errors.New("content-addressed RemoteCheckRecord conflicts with immutable bytes"))
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return atomicWrite(path, append(data, '\n'))
}

func bindPersistedRemoteChecks(runDir string, validator *contract.Validator, intent domain.SCMMergeIntent, publication domain.PublicationRecord, requiredChecks []string, state domain.RunState) error {
	path := filepath.Join(runDir, "remote-check-records", strings.TrimPrefix(intent.RemoteCheckRecordDigest, "sha256:")+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return port.Permanent(errors.New("merge intent lacks its content-addressed RemoteCheckRecord bytes"))
	}
	if err := validator.Validate(domain.KindRemoteCheckRecord, data); err != nil {
		return port.Permanent(err)
	}
	digest, err := canonical.DigestJSON(data)
	if err != nil || digest != intent.RemoteCheckRecordDigest {
		return port.Permanent(errors.New("persisted RemoteCheckRecord bytes do not bind the merge intent digest"))
	}
	var checks domain.RemoteCheckRecord
	if err := json.Unmarshal(data, &checks); err != nil {
		return port.Permanent(err)
	}
	if checks.TaskID != state.TaskID || checks.RunID != state.RunID || checks.RepositoryID != publication.Repository.ID ||
		checks.RequestID != publication.Request.ID || checks.HeadSHA != publication.HeadSHA || checks.Status != domain.CheckStatusPass {
		return port.Permanent(errors.New("persisted RemoteCheckRecord identity or status is not current"))
	}
	if len(requiredChecks) > 0 && !requiredChecksMatch(checks.Checks, requiredChecks) {
		return port.Permanent(errors.New("persisted RemoteCheckRecord requiredChecks do not bind the frozen task"))
	}
	return nil
}
