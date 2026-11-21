package auth

import (
	"context"
	"net/http"
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

func TestUrgentDrainTarget(t *testing.T) {
	t.Parallel()
	now := time.Now()
	urgent := authWithWeeklyReset("urgent", now.Add(8*time.Hour))
	farBound := authWithWeeklyReset("far", now.Add(72*time.Hour))
	notUrgent := authWithWeeklyReset("not-urgent", now.Add(30*time.Hour))

	if got := urgentDrainTarget([]*Auth{farBound, urgent}, farBound, now); got != urgent {
		t.Fatalf("urgentDrainTarget() = %v, want urgent", got)
	}
	if got := urgentDrainTarget([]*Auth{farBound, urgent}, urgent, now); got != nil {
		t.Fatalf("session already on the urgent auth must stay, got %v", got)
	}
	if got := urgentDrainTarget([]*Auth{farBound, notUrgent}, farBound, now); got != nil {
		t.Fatalf("reset outside the urgent window must not rebind, got %v", got)
	}
	soonerBound := authWithWeeklyReset("sooner-bound", now.Add(2*time.Hour))
	if got := urgentDrainTarget([]*Auth{soonerBound, urgent}, soonerBound, now); got != nil {
		t.Fatalf("bound auth expiring even sooner must stay, got %v", got)
	}
}

func TestSessionAffinityResetFirst_RebindsBoundSessionToUrgentAuth(t *testing.T) {
	t.Parallel()
	now := time.Now()
	far := authWithWeeklyReset("far", now.Add(72*time.Hour))
	urgent := authWithWeeklyReset("urgent", now.Add(8*time.Hour))
	urgent.Unavailable = true
	urgent.NextRetryAfter = now.Add(time.Hour)

	selector := NewSessionAffinitySelector(&ResetFirstSelector{})
	opts := cliproxyexecutor.Options{Headers: http.Header{"X-Session-Id": []string{"urgent-drain-session"}}}
	pick := func() string {
		t.Helper()
		got, err := selector.Pick(context.Background(), "claude", "claude-test", opts, []*Auth{far, urgent})
		if err != nil {
			t.Fatalf("Pick() error = %v", err)
		}
		return got.ID
	}

	if got := pick(); got != "far" {
		t.Fatalf("first Pick() = %q, want far while urgent is cooling down", got)
	}
	urgent.Unavailable = false
	urgent.NextRetryAfter = time.Time{}
	if got := pick(); got != "urgent" {
		t.Fatalf("Pick() after recovery = %q, want urgent", got)
	}
	if got := pick(); got != "urgent" {
		t.Fatalf("Pick() must stay bound to urgent, got %q", got)
	}
}
