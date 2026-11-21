package auth

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func authWithWeeklyReset(id string, resetAt time.Time) *Auth {
	return &Auth{
		ID: id,
		Quota: QuotaState{Signals: map[string]string{
			claudeWeeklyResetSignal: strconv.FormatInt(resetAt.Unix(), 10),
		}},
	}
}

func pickResetFirst(t *testing.T, auths []*Auth) string {
	t.Helper()
	got, err := (&ResetFirstSelector{}).Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	return got.ID
}

func TestResetFirstSelectorPick_PrefersSoonestWeeklyReset(t *testing.T) {
	t.Parallel()
	now := time.Now()
	auths := []*Auth{
		authWithWeeklyReset("later", now.Add(96*time.Hour)),
		authWithWeeklyReset("soonest", now.Add(20*time.Hour)),
		authWithWeeklyReset("middle", now.Add(48*time.Hour)),
	}
	if got := pickResetFirst(t, auths); got != "soonest" {
		t.Fatalf("Pick() = %q, want soonest", got)
	}
}

func TestResetFirstSelectorPick_UnknownOrStaleResetIsExploredFirst(t *testing.T) {
	t.Parallel()
	now := time.Now()
	known := authWithWeeklyReset("known", now.Add(time.Hour))

	if got := pickResetFirst(t, []*Auth{known, {ID: "unknown"}}); got != "unknown" {
		t.Fatalf("Pick() = %q, want unknown", got)
	}
	stale := authWithWeeklyReset("stale", now.Add(-time.Hour))
	if got := pickResetFirst(t, []*Auth{known, stale}); got != "stale" {
		t.Fatalf("Pick() = %q, want stale", got)
	}
}

func TestResetFirstSelectorPick_PriorityTierWinsOverReset(t *testing.T) {
	t.Parallel()
	now := time.Now()
	soon := authWithWeeklyReset("soon-low-priority", now.Add(time.Hour))
	late := authWithWeeklyReset("late-high-priority", now.Add(100*time.Hour))
	late.Attributes = map[string]string{"priority": "10"}

	if got := pickResetFirst(t, []*Auth{soon, late}); got != "late-high-priority" {
		t.Fatalf("Pick() = %q, want late-high-priority", got)
	}
}

func TestResetFirstSelectorPick_SkipsCooledDownAuth(t *testing.T) {
	t.Parallel()
	now := time.Now()
	exhausted := authWithWeeklyReset("exhausted", now.Add(time.Hour))
	exhausted.Quota.Exceeded = true
	exhausted.Quota.NextRecoverAt = now.Add(time.Hour)
	exhausted.Unavailable = true
	exhausted.NextRetryAfter = now.Add(time.Hour)
	fresh := authWithWeeklyReset("fresh", now.Add(100*time.Hour))

	if got := pickResetFirst(t, []*Auth{exhausted, fresh}); got != "fresh" {
		t.Fatalf("Pick() = %q, want fresh", got)
	}
}

func TestManagerPick_ResetFirstBehindSessionAffinity(t *testing.T) {
	now := time.Now()
	soon := authWithWeeklyReset("reset-first-soon", now.Add(20*time.Hour))
	late := authWithWeeklyReset("reset-first-late", now.Add(90*time.Hour))
	for _, auth := range []*Auth{soon, late} {
		auth.Provider = "claude"
		auth.Status = StatusActive
		auth.Metadata = map[string]any{"access_token": "token"}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "claude-test"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	}

	selector := NewSessionAffinitySelector(&ResetFirstSelector{})
	manager := NewManager(nil, selector, nil)
	manager.RegisterExecutor(&refreshMockExecutor{id: "claude"})
	for _, auth := range []*Auth{late, soon} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, err)
		}
	}

	picked, _, err := manager.pickNext(context.Background(), "claude", "claude-test", cliproxyexecutor.Options{}, nil)
	if err != nil {
		t.Fatalf("pickNext() error = %v", err)
	}
	if picked.ID != soon.ID {
		t.Fatalf("pickNext() = %q, want %q", picked.ID, soon.ID)
	}
}
