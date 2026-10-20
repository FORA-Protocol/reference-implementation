package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
)

// TestBoot_RefusesASigningOriginThatIsNotAnHTTPSOrigin drives the Broker's own
// main() with BROKER_PUBLIC_ORIGIN set to values no verifier accepts as a
// Signature-Agent member — the http scheme, a path, a bare host — and with no
// BROKER_PUBLIC_ORIGIN over a BROKER_DOMAIN that cannot form one. Each boot
// must stop with the broker.exit record naming the setting, before it connects
// to anything: no BROKER_DSN is set, so a boot that got past the check would
// stop on the database instead and not name BROKER_PUBLIC_ORIGIN.
func TestBoot_RefusesASigningOriginThatIsNotAnHTTPSOrigin(t *testing.T) {
	t.Parallel()
	for name, env := range map[string][]string{
		"http scheme":    {"BROKER_PUBLIC_ORIGIN=http://broker"},
		"path":           {"BROKER_PUBLIC_ORIGIN=https://broker/relay"},
		"bare host":      {"BROKER_PUBLIC_ORIGIN=broker"},
		"derived, upper": {"BROKER_DOMAIN=Broker.Example"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			res := testutil.RunHelper(t, exec.CommandContext(context.Background(), os.Args[0]),
				"TestHelperMain", env...)
			if res.Code == 0 {
				t.Fatalf("the Broker booted with %v", env)
			}
			if !bytes.Contains(res.Stdout, []byte("broker.exit")) ||
				!bytes.Contains(res.Stdout, []byte("BROKER_PUBLIC_ORIGIN")) {
				t.Fatalf("the boot did not stop on BROKER_PUBLIC_ORIGIN:\n%s", res.Stdout)
			}
		})
	}
}
