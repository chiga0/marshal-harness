package attemptgate

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/chiga0/marshal-harness/internal/runtimeprofile"
)

var (
	// ErrInvalidAttempt 拒绝空 attemptID。
	ErrInvalidAttempt = errors.New("invalid attempt id")
	// ErrInvalidProfile 拒绝未通过 runtimeprofile 校验的 profile。
	ErrInvalidProfile = errors.New("invalid worker runtime profile")
	// ErrUnknownAttempt 拒绝未绑定 profile 的 attempt（fail closed）。
	ErrUnknownAttempt = errors.New("unknown attempt")
	// ErrProfileConflict 拒绝同 attemptID 绑定不同 ProfileDigest。
	ErrProfileConflict = errors.New("attempt profile conflict")
)

// AttemptProfileStore 是 Attempt → WorkerRuntimeProfile 的 immutable 绑定
// 存储（ADR 0018 §13 put-if-absent 风格）：绑定只增不改，同一 attemptID
// 重复绑定同一 ProfileDigest 幂等，绑定不同 ProfileDigest fail closed。
// 并发安全；纯内存、确定性，不携带任何时钟。
type AttemptProfileStore struct {
	mu       sync.Mutex
	profiles map[string]runtimeprofile.WorkerRuntimeProfile
}

// NewAttemptProfileStore 返回一个空的可用存储。
func NewAttemptProfileStore() *AttemptProfileStore {
	return &AttemptProfileStore{profiles: make(map[string]runtimeprofile.WorkerRuntimeProfile)}
}

// Bind 把 attemptID 绑定到 profile。profile 必须通过 AgentBinding 与
// SandboxBinding 双侧 Validate，且携带合法 ProfileDigest。重复绑定同一
// profile 幂等成功；绑定不同 profile fail closed（ErrProfileConflict）。
func (s *AttemptProfileStore) Bind(attemptID string, profile runtimeprofile.WorkerRuntimeProfile) error {
	if strings.TrimSpace(attemptID) == "" {
		return fmt.Errorf("attemptgate: %w: must not be empty", ErrInvalidAttempt)
	}
	if err := profile.Agent.Validate(); err != nil {
		return fmt.Errorf("attemptgate: %w: agent binding: %v", ErrInvalidProfile, err)
	}
	if err := profile.Sandbox.Validate(); err != nil {
		return fmt.Errorf("attemptgate: %w: sandbox binding: %v", ErrInvalidProfile, err)
	}
	if err := requireDigest("ProfileDigest", profile.ProfileDigest); err != nil {
		return fmt.Errorf("attemptgate: %w: %v", ErrInvalidProfile, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.profiles[attemptID]; ok {
		if existing.ProfileDigest == profile.ProfileDigest {
			return nil
		}
		return fmt.Errorf("attemptgate: %w: attempt %q already bound to %q, got %q",
			ErrProfileConflict, attemptID, existing.ProfileDigest, profile.ProfileDigest)
	}
	s.profiles[attemptID] = profile
	return nil
}

// Resolve 返回 attemptID 绑定的 immutable profile；未绑定 fail closed
// （ErrUnknownAttempt）。
func (s *AttemptProfileStore) Resolve(attemptID string) (runtimeprofile.WorkerRuntimeProfile, error) {
	if strings.TrimSpace(attemptID) == "" {
		return runtimeprofile.WorkerRuntimeProfile{}, fmt.Errorf("attemptgate: %w: must not be empty", ErrInvalidAttempt)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	profile, ok := s.profiles[attemptID]
	if !ok {
		return runtimeprofile.WorkerRuntimeProfile{}, fmt.Errorf("attemptgate: %w: %q", ErrUnknownAttempt, attemptID)
	}
	return profile, nil
}
