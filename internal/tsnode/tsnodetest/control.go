// Package tsnodetest runs a local Tailscale coordination server with DERP
// for tests. Only tests import it.
package tsnodetest

import (
	"net/http/httptest"
	"testing"

	"tailscale.com/net/netns"
	"tailscale.com/tailcfg"
	"tailscale.com/tstest/integration"
	"tailscale.com/tstest/integration/testcontrol"
	"tailscale.com/types/logger"
)

// Control starts the server; with requireAuth nodes get a login link that
// the test completes with CompleteAuth.
func Control(t testing.TB, requireAuth bool) *testcontrol.Server {
	t.Helper()
	netns.SetEnabled(false)
	t.Cleanup(func() { netns.SetEnabled(true) })
	derpMap := integration.RunDERPAndSTUN(t, logger.Discard, "127.0.0.1")
	control := &testcontrol.Server{
		DERPMap:        derpMap,
		DNSConfig:      &tailcfg.DNSConfig{Proxied: true},
		MagicDNSDomain: "tail-test.ts.net",
		Logf:           logger.Discard,
		RequireAuth:    requireAuth,
	}
	control.HTTPTestServer = httptest.NewUnstartedServer(control)
	control.HTTPTestServer.Start()
	t.Cleanup(control.HTTPTestServer.Close)
	return control
}
