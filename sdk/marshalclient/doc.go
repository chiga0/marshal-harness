// Package marshalclient is the typed Go client SDK of the Marshal Public
// API (protocol family marshal-public-api, version v1alpha1).
//
// The frozen contract is internal/server/openapi.json of the resident
// marshal-server; this SDK follows it and never diverges from it. It uses
// only the Go standard library (net/http, encoding/json, bufio and friends)
// and introduces no dependencies. CLI, Web, GitHub App and CI integrations
// are all Public API clients of the same surface.
//
// # Endpoints
//
// The Client exposes one typed method per frozen endpoint:
//
//	CreateTask    POST   /v1alpha1/tasks
//	GetTask       GET    /v1alpha1/tasks/{taskId}
//	CancelTask    POST   /v1alpha1/tasks/{taskId}/cancel
//	ApproveRun    POST   /v1alpha1/runs/{runId}/approval
//	GetRunStatus  GET    /v1alpha1/runs/{runId}/status
//	Events        GET    /v1alpha1/events       (SSE, see below)
//	PollEvents    GET    /v1alpha1/events/poll  (polling fallback)
//
// # Versioned identity envelope
//
// Every request carries the frozen protocol headers, constructed from the
// Config injected at construction time: Marshal-Protocol-Version,
// Marshal-Audience, Marshal-Scope, Marshal-Principal, a client-chosen
// Marshal-Request-Id and an RFC 3339 Marshal-Deadline derived from the
// request context deadline or Config.RequestTimeout. Credential material is
// injected once through Config and never appears in error messages.
//
// # Idempotent submissions
//
// Mutating endpoints submit the frozen idempotency envelope:
// idempotencyKey plus requestDigest plus payload. The SDK computes
// requestDigest as the sha256 digest of the RFC 8785 canonical form of the
// payload (canonicalizeJSON/RequestDigest), so the identical quadruple
// (authorityNamespaceId, scope, idempotencyKey, requestDigest) merges into
// the stored result server-side; the identical key with a different digest
// conflicts fail closed (typed ErrIdempotencyConflict). TaskSubmission and
// ApprovalRecord report replayed merges via their Replayed field.
//
// # Error model
//
// API Error documents map to the typed APIError with the frozen code and a
// preserved reason; errors.Is classifies every code through dedicated
// sentinels, and protocol-version and audience rejections are separately
// typed (ErrProtocolVersionRejected, ErrAudienceRejected). HTTP transport
// failures map to TransportError, deterministic event resync directives to
// ResyncRequiredError, and opaque non-2xx bodies to
// UnexpectedResponseError (body retained, never swallowed).
//
// # Event stream consumption
//
// Client.Events consumes the SSE read-only event projection: events are
// consumed by monotonically increasing ledgerSequence with eventId
// deduplication (at-least-once delivery); a lost connection resumes via
// Last-Event-ID or cursor; a deterministic EventResync directive is exposed
// as a typed stream item for the caller's explicit resume decision, never
// swallowed silently; and transport-level SSE unavailability degrades to
// the polling fallback over the identical projection data.
//
// # Example
//
//	client, err := marshalclient.New(marshalclient.Config{
//		BaseURL:   "http://127.0.0.1:7718",
//		Principal: "ci-bot",
//		Scope:     "repo:/srv/repo",
//	})
//	if err != nil { /* fail closed */ }
//	submission, err := client.CreateTask(ctx, marshalclient.TaskCreateRequest{
//		IdempotencyKey: "submission-1",
//		Payload: marshalclient.TaskCreatePayload{
//			RunID:          "run-1",
//			TaskSpec:       taskSpecJSON,
//			PolicySnapshot: policyJSON,
//		},
//	})
//	if err != nil { /* typed error: marshalclient.AsAPIError, errors.Is, ... */ }
//
//	stream, err := client.Events(ctx, marshalclient.EventsOptions{})
//	if err != nil { /* fail closed */ }
//	defer stream.Close()
//	for stream.Next() {
//		item := stream.Item()
//		if item.Resync != nil {
//			// Caller decision: rebuild the subscription from the directive.
//			_ = stream.Resume()
//			continue
//		}
//		consume(item.Event)
//	}
//	if err := stream.Err(); err != nil { /* terminal state */ }
package marshalclient
