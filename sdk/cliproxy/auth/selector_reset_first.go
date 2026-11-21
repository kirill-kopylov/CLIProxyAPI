package auth

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// claudeWeeklyResetSignal is the canonical quota-signal key carrying the unix
// timestamp at which the account's 7-day window resets.
var claudeWeeklyResetSignal = http.CanonicalHeaderKey("Anthropic-Ratelimit-Unified-7d-Reset")

// ResetFirstSelector prefers the credential whose weekly quota resets soonest:
// whatever is left on it at reset time is lost, so it is drained first.
// Credentials without a known (future) reset are tried first to learn it.
// Ties and non-Claude credentials fall back to deterministic fill-first order.
type ResetFirstSelector struct{}

// Pick selects the available auth with the earliest weekly reset.
func (s *ResetFirstSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = opts
	now := time.Now()
	available, err := getSelectorAvailableAuths(ctx, auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)
	return earliestWeeklyReset(available, now), nil
}

func earliestWeeklyReset(available []*Auth, now time.Time) *Auth {
	ordered := append([]*Auth(nil), available...)
	sort.SliceStable(ordered, func(i, j int) bool {
		left, right := weeklyResetAt(ordered[i], now), weeklyResetAt(ordered[j], now)
		if !left.Equal(right) {
			return left.Before(right)
		}
		return ordered[i].ID < ordered[j].ID
	})
	return ordered[0]
}

// weeklyResetAt returns the observed weekly reset, or the zero time when it is
// unknown or already passed (such credentials sort first).
func weeklyResetAt(auth *Auth, now time.Time) time.Time {
	if auth == nil {
		return time.Time{}
	}
	raw := strings.TrimSpace(auth.Quota.Signals[claudeWeeklyResetSignal])
	seconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}
	}
	resetAt := time.Unix(seconds, 0)
	if !resetAt.After(now) {
		return time.Time{}
	}
	return resetAt
}
