package publication

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
)

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
