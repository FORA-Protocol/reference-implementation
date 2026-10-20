package setup_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/setup"
)

// mockPublisherManifest is the discovery document the e2e stack's mock
// publisher origin serves. The console onboarding suite checks it through the
// console, so it has to be a document the console could have generated -- only
// for a different payee and a different contributor, which is what that suite
// expects the check to report. A hand-edited file that drifted from the
// generated shape would test a document no publisher is ever handed.
const mockPublisherManifest = "../../../../tests/e2e/mock-publisher/content/.well-known/fora.json"

// TestMockPublisherManifestIsGenerated holds the static e2e file to the
// console's own generator, run with the deployment values the file names.
// Any field the generator adds, drops or spells differently fails here.
func TestMockPublisherManifestIsGenerated(t *testing.T) {
	t.Parallel()
	generated, err := setup.Manifest(setup.Deployment{
		ExchangeDomain:   "exchange:8081",
		ExchangeEndpoint: "http://exchange:8081",
		ContributorHost:  "publisher",
		// The e2e stack leaves CONSOLE_SUPPORTED_PROFILES unset, so its console
		// writes the default: the profiles the Exchange supports.
		SupportedProfiles: forawellknown.ExchangeProfiles(),
	}, setup.Site{Domain: "publisher", PublisherID: "demo-resource-owner"})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	onDisk, err := os.ReadFile(filepath.FromSlash(mockPublisherManifest))
	if err != nil {
		t.Fatalf("read the e2e fixture: %v", err)
	}
	var want, got any
	if err := json.Unmarshal(generated, &want); err != nil {
		t.Fatalf("decode the generated document: %v", err)
	}
	if err := json.Unmarshal(onDisk, &got); err != nil {
		t.Fatalf("decode the e2e fixture: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s drifted from the generated document\n got: %s\nwant: %s",
			mockPublisherManifest, onDisk, generated)
	}
}
