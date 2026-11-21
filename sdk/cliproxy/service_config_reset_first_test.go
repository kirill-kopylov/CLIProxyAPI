package cliproxy

import (
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestResetFirstRoutingSelector(t *testing.T) {
	state := normalizedRoutingRuntimeState(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{Strategy: "reset-first"},
	})
	if state.strategy != "reset-first" {
		t.Fatalf("strategy = %q, want reset-first", state.strategy)
	}
	if _, ok := newRoutingSelector(state).(*coreauth.ResetFirstSelector); !ok {
		t.Fatalf("selector type = %T, want *auth.ResetFirstSelector", newRoutingSelector(state))
	}
}
