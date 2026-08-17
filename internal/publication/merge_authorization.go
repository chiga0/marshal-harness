package publication

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
)

type durableMergeAuthorization struct {
	LedgerKey       string                               `json:"ledgerKey"`
	Authorization   authority.PublicationAuthorization   `json:"authorization"`
	DecisionBinding authority.PublicationDecisionBinding `json:"decisionBinding"`
	RecordDigest    string                               `json:"recordDigest"`
}

type durableMergeAuthorizationHead struct {
	IntentDigest         string `json:"intentDigest"`
	RecordDigest         string `json:"recordDigest"`
	RevocationGeneration uint64 `json:"revocationGeneration"`
	HeadDigest           string `json:"headDigest"`
}

func (head durableMergeAuthorizationHead) digest() (string, error) {
	detached := head
	detached.HeadDigest = ""
	data, err := json.Marshal(detached)
	if err != nil {
		return "", err
	}
	return canonical.DigestJSON(data)
}

type mergeAuthorizationSink struct {
	runDir string
	intent domain.SCMMergeIntent
}

func (sink mergeAuthorizationSink) PersistenceID() string {
	return sink.runDir + ":" + sink.intent.IntentDigest
}

func (sink mergeAuthorizationSink) PersistPublicationAuthorization(ledgerKey string, authorization authority.PublicationAuthorization, decision authority.PublicationDecisionBinding) error {
	if decision.SideEffectIntentDigest != sink.intent.IntentDigest {
		return nil
	}
	record := durableMergeAuthorization{LedgerKey: ledgerKey, Authorization: authorization, DecisionBinding: decision}
	if err := record.validateBinding(sink.intent); err != nil {
		return err
	}
	return persistDurableMergeAuthorization(sink.runDir, record)
}

func (record durableMergeAuthorization) digest() (string, error) {
	detached := record
	detached.RecordDigest = ""
	data, err := json.Marshal(detached)
	if err != nil {
		return "", err
	}
	return canonical.DigestJSON(data)
}

func (record durableMergeAuthorization) validateBinding(intent domain.SCMMergeIntent) error {
	if record.LedgerKey == "" || record.Authorization.EdgeDigest == "" {
		return errors.New("durable merge authorization is incomplete")
	}
	if err := record.Authorization.Validate(); err != nil {
		return err
	}
	if err := record.DecisionBinding.Validate(); err != nil {
		return err
	}
	if record.Authorization.BoundPublicationDigest != intent.PublicationDigest ||
		record.Authorization.ExpectedPrincipal != intent.ExpectedMergedBy ||
		record.DecisionBinding.SideEffectIntentDigest != intent.IntentDigest ||
		record.DecisionBinding.ReviewDecisionDigest != intent.ReviewDecisionDigest ||
		record.DecisionBinding.EvidenceDigest != intent.EvidenceDigest {
		return errors.New("durable merge authorization does not bind the merge intent")
	}
	issuance := record.Authorization
	issuance.RevocationGeneration = 0
	issuance.EdgeDigest = ""
	issuanceDigest, err := issuance.Digest()
	if err != nil {
		return err
	}
	if record.LedgerKey != issuanceDigest {
		return errors.New("durable merge authorization ledger key is not its issuance digest")
	}
	return nil
}

func (record durableMergeAuthorization) validate(intent domain.SCMMergeIntent) error {
	if err := record.validateBinding(intent); err != nil {
		return err
	}
	recomputed, err := record.digest()
	if err != nil || recomputed != record.RecordDigest {
		return errors.New("durable merge authorization digest mismatch")
	}
	return nil
}

func persistDurableMergeAuthorization(runDir string, record durableMergeAuthorization) error {
	if record.LedgerKey == "" {
		return errors.New("durable merge authorization ledger key is required")
	}
	digest, err := record.digest()
	if err != nil {
		return err
	}
	record.RecordDigest = digest
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	intentDigest := record.DecisionBinding.SideEffectIntentDigest
	directory := filepath.Join(runDir, "merge-authorizations", strings.TrimPrefix(intentDigest, "sha256:"))
	path := filepath.Join(directory, strings.TrimPrefix(digest, "sha256:")+".json")
	if existing, readErr := os.ReadFile(path); readErr == nil {
		existingDigest, digestErr := canonical.DigestJSON(existing)
		incomingDigest, incomingErr := canonical.DigestJSON(data)
		if digestErr != nil || incomingErr != nil || existingDigest != incomingDigest {
			return errors.New("durable merge authorization conflicts with immutable bytes")
		}
		// Continue to the head write: a previous crash may have persisted the
		// record but not advanced its independently stored monotonic anchor.
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	} else {
		if err := putFileNoReplace(path, append(data, '\n')); err != nil {
			return err
		}
	}
	head := durableMergeAuthorizationHead{IntentDigest: intentDigest, RecordDigest: digest, RevocationGeneration: record.Authorization.RevocationGeneration}
	head.HeadDigest, err = head.digest()
	if err != nil {
		return err
	}
	headData, err := json.Marshal(head)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(runDir, "merge-authorization-heads", strings.TrimPrefix(intentDigest, "sha256:")+".json"), append(headData, '\n'))
}

func loadDurableMergeAuthorization(runDir string, intent domain.SCMMergeIntent) (durableMergeAuthorization, error) {
	directory := filepath.Join(runDir, "merge-authorizations", strings.TrimPrefix(intent.IntentDigest, "sha256:"))
	entries, err := os.ReadDir(directory)
	if err != nil {
		return durableMergeAuthorization{}, err
	}
	var selected durableMergeAuthorization
	found := false
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			return durableMergeAuthorization{}, errors.New("durable merge authorization directory contains an invalid entry")
		}
		data, readErr := os.ReadFile(filepath.Join(directory, entry.Name()))
		if readErr != nil {
			return durableMergeAuthorization{}, readErr
		}
		var record durableMergeAuthorization
		if err := json.Unmarshal(data, &record); err != nil {
			return durableMergeAuthorization{}, err
		}
		if err := record.validate(intent); err != nil {
			return durableMergeAuthorization{}, err
		}
		if entry.Name() != strings.TrimPrefix(record.RecordDigest, "sha256:")+".json" {
			return durableMergeAuthorization{}, errors.New("durable merge authorization filename does not bind its digest")
		}
		if found && record.Authorization.RevocationGeneration == selected.Authorization.RevocationGeneration && record != selected {
			return durableMergeAuthorization{}, errors.New("durable merge authorization has divergent records at one revocation generation")
		}
		if !found || record.Authorization.RevocationGeneration > selected.Authorization.RevocationGeneration {
			selected, found = record, true
		}
	}
	if !found {
		return durableMergeAuthorization{}, errors.New("merge intent lacks its durable PublicationAuthorization")
	}
	headData, err := os.ReadFile(filepath.Join(runDir, "merge-authorization-heads", strings.TrimPrefix(intent.IntentDigest, "sha256:")+".json"))
	if err != nil {
		return durableMergeAuthorization{}, errors.New("durable merge authorization lacks its monotonic head")
	}
	var head durableMergeAuthorizationHead
	if err := json.Unmarshal(headData, &head); err != nil {
		return durableMergeAuthorization{}, err
	}
	recomputedHead, err := head.digest()
	if err != nil || recomputedHead != head.HeadDigest || head.IntentDigest != intent.IntentDigest || head.RecordDigest != selected.RecordDigest || head.RevocationGeneration != selected.Authorization.RevocationGeneration {
		return durableMergeAuthorization{}, errors.New("durable merge authorization head is missing, rolled back or divergent")
	}
	return selected, nil
}

func issueMergeAuthorization(input MergeInput, intent domain.SCMMergeIntent, admission mergeAdmission, expiry, now time.Time) (authority.PublicationAuthorization, error) {
	issuerDigest, err := input.AuthorizationRuntime.Issuer().Digest()
	if err != nil || issuerDigest != intent.AuthorityNamespaceID {
		return authority.PublicationAuthorization{}, errors.New("PublicationAuthorization issuer does not bind the merge authority namespace")
	}
	if err := input.AuthorizationSource.Validate(); err != nil {
		return authority.PublicationAuthorization{}, err
	}
	if err := input.AuthorizationTarget.Validate(); err != nil {
		return authority.PublicationAuthorization{}, err
	}
	targetDigest, err := input.AuthorizationTarget.Digest()
	if err != nil || targetDigest != intent.MergerSecurityDomainID {
		return authority.PublicationAuthorization{}, errors.New("PublicationAuthorization target security domain does not bind the merge intent")
	}
	authorization, err := input.AuthorizationRuntime.IssuePublicationAuthorization(authority.PublicationIssuance{
		SourceActor:            input.AuthorizationSource,
		TargetActor:            input.AuthorizationTarget,
		Operation:              authority.PublicationOperationControlledMerge,
		BoundPublicationDigest: admission.publicationDigest,
		ExpectedPrincipal:      intent.ExpectedMergedBy,
		DecisionBinding: authority.PublicationDecisionBinding{
			SideEffectIntentDigest: intent.IntentDigest,
			ReviewDecisionDigest:   intent.ReviewDecisionDigest,
			EvidenceDigest:         intent.EvidenceDigest,
		},
		Expiry: expiry.UTC().Format(time.RFC3339),
	}, now)
	if err != nil {
		return authority.PublicationAuthorization{}, err
	}
	return authorization, nil
}

func loadOrIssueMergeAuthorization(input MergeInput, intent domain.SCMMergeIntent, admission mergeAdmission, expiry, now time.Time, runDir string, recovering bool) (authority.PublicationAuthorization, error) {
	if err := input.AuthorizationRuntime.BindPublicationAuthorizationPersistence(mergeAuthorizationSink{runDir: runDir, intent: intent}); err != nil {
		return authority.PublicationAuthorization{}, err
	}
	if !recovering {
		authorization, err := issueMergeAuthorization(input, intent, admission, expiry, now)
		if err != nil {
			return authority.PublicationAuthorization{}, err
		}
		return authorization, nil
	}
	record, err := loadDurableMergeAuthorization(runDir, intent)
	if err != nil {
		return authority.PublicationAuthorization{}, err
	}
	if current, binding, ok := input.AuthorizationRuntime.CurrentPublicationAuthorization(record.LedgerKey); ok {
		if current != record.Authorization || binding != record.DecisionBinding {
			// A revocation successor in the live current ledger is an authority
			// fact. Append it durably before use so a later process restart cannot
			// revive the issuance record.
			if current.RevocationGeneration <= record.Authorization.RevocationGeneration || binding != record.DecisionBinding {
				return authority.PublicationAuthorization{}, authority.ErrEdgeDiverged
			}
			record.Authorization = current
			record.RecordDigest = ""
			if err := persistDurableMergeAuthorization(runDir, record); err != nil {
				return authority.PublicationAuthorization{}, err
			}
		}
		return current, nil
	}
	if err := input.AuthorizationRuntime.RestorePublicationAuthorization(record.LedgerKey, record.Authorization, record.DecisionBinding); err != nil {
		return authority.PublicationAuthorization{}, err
	}
	return record.Authorization, nil
}

func recheckMergeAuthorization(input MergeInput, authorization authority.PublicationAuthorization, intent domain.SCMMergeIntent, operation string, now time.Time) error {
	document, err := json.Marshal(map[string]string{
		"intentDigest": intent.IntentDigest,
		"operation":    operation,
	})
	if err != nil {
		return err
	}
	requestDigest, err := canonical.DigestJSON(document)
	if err != nil {
		return err
	}
	request := authority.PublicationUseRequest{
		SourceActor:            input.AuthorizationSource,
		TargetActor:            input.AuthorizationTarget,
		Operation:              authority.PublicationOperationControlledMerge,
		PublicationDigest:      intent.PublicationDigest,
		ExpectedPrincipal:      intent.ExpectedMergedBy,
		SideEffectIntentDigest: intent.IntentDigest,
		ReviewDecisionDigest:   intent.ReviewDecisionDigest,
		EvidenceDigest:         intent.EvidenceDigest,
		RequestDigest:          requestDigest,
	}
	if err := input.AuthorizationRuntime.RecheckPublicationAuthorization(authorization, request, now); err != nil {
		return fmt.Errorf("PublicationAuthorization current-ledger recheck failed: %w", err)
	}
	return nil
}
