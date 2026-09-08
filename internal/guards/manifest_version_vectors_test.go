// Cross-implementation gate for the manifest document-version corpus: the
// repo-root copy of the SDK's manifest-version-vectors.json must replay through
// this repo's own consumer gate (internal/forawellknown.ValidateManifest) and
// through the pinned module's face (sdk/go/helpers.CheckWellKnownManifestVersion
// in github.com/FORA-Protocol/protocol), and the two must agree row for row.
//
// Provenance and refresh rule. testdata/manifest-version-vectors.json is a
// verbatim byte copy of sdk/go/helpers/testdata/manifest-version-vectors.json in
// that module. The copy is refreshed from the module at every re-pin and never
// edited by hand, and scripts/check-sdk-pin-consistency.sh enforces that: its
// corpus section byte-compares every copy against the module's and fails when
// one differs, or when it could not reach the module to compare at all. It is
// copied rather than read from the module cache because testdata cannot cross a
// module boundary and a cache read breaks in vendored or air-gapped CI. The
// license-term, thumbprint and proof-of-possession corpora under testdata/ are
// the same arrangement.
//
// Why this repo replays it rather than trusting the SDK's own test. The rule has
// two halves and the SDK exports only one. CheckWellKnownManifestVersion takes a
// string, so it answers the "which versions are accepted" half. The other half —
// that a `ver` member which is absent, or present but not a string, is treated
// as absent — lives in the SDK's unexported manifestVer and is re-implemented
// here in forawellknown.checkManifestVersion. The corpus carries `present`
// alongside `ver` precisely so that half can be replayed, and this file is where
// this repo's copy of that policy is held to it. Without this, the repo's copy of
// a rule the SDK calls three-language-pinned was guarded only by a hand-written
// table.
//
// Both polarities have to be present, not just some vectors. The assertion has
// an accepting branch and a refusing branch, and which one a vector takes is
// decided by the vector itself, so a corpus that drifted to all-one-kind would
// run only one branch and still report green. The counts below are what make
// that a failure.
package guards_test

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
)

// manifestVersionVectors is the repo-root copy, relative to this package
// directory.
const manifestVersionVectors = "../../testdata/manifest-version-vectors.json"

// manifestVersionVector is one row: the `ver` value, whether the member is
// present at all, and whether a consumer accepts the document.
type manifestVersionVector struct {
	Name     string `json:"name"`
	Ver      string `json:"ver"`
	Present  bool   `json:"present"`
	Accepted bool   `json:"accepted"`
}

func loadManifestVersionCorpus(t *testing.T) []manifestVersionVector {
	t.Helper()
	raw, err := os.ReadFile(manifestVersionVectors)
	if err != nil {
		t.Fatalf("read the manifest-version corpus: %v — it is a verbatim copy of the "+
			"pinned module's, refreshed at every re-pin", err)
	}
	var corpus struct {
		ManifestVersion []manifestVersionVector `json:"manifest_version"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatalf("decode %s: %v", manifestVersionVectors, err)
	}
	if len(corpus.ManifestVersion) == 0 {
		t.Fatalf("%s carries no manifest_version list — the corpus moved upstream "+
			"and this guard needs its new shape", manifestVersionVectors)
	}
	return corpus.ManifestVersion
}

// manifestAt renders the smallest document that reaches the version gate,
// carrying ver exactly as the vector states it — present with that value, or
// absent altogether.
func manifestAt(v manifestVersionVector) []byte {
	doc := map[string]any{"role": "ROLE_AGENT", "domain": "a.example"}
	if v.Present {
		doc["ver"] = v.Ver
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		panic("marshal a fixed map: " + err.Error())
	}
	return raw
}

// TestManifestVersionCorpusReplaysThroughValidateManifest is the gate. Every row
// is driven through this repo's consumer entry point, and an accepted row must
// pass the version gate while a refused row must come back as ErrVersionRefused
// and NOT as a shape failure — the two are different answers to the caller, and
// a refusal reported as a shape failure is what sends a settled verdict down a
// retryable path.
func TestManifestVersionCorpusReplaysThroughValidateManifest(t *testing.T) {
	t.Parallel()
	corpus := loadManifestVersionCorpus(t)
	var accepted, refused int
	for _, v := range corpus {
		t.Run(v.Name, func(t *testing.T) {
			t.Parallel()
			err := forawellknown.ValidateManifest(manifestAt(v))
			switch {
			case v.Accepted:
				if err != nil {
					t.Fatalf("corpus row %q (ver=%q present=%v) is accepted upstream, "+
						"and this repo refused it: %v", v.Name, v.Ver, v.Present, err)
				}
			default:
				if !errors.Is(err, forawellknown.ErrVersionRefused) {
					t.Fatalf("corpus row %q (ver=%q present=%v) is refused upstream, and "+
						"this repo answered %v — a version refusal must carry "+
						"ErrVersionRefused, never a shape failure", v.Name, v.Ver, v.Present, err)
				}
			}
		})
		if v.Accepted {
			accepted++
		} else {
			refused++
		}
	}
	if accepted == 0 || refused == 0 {
		t.Fatalf("the corpus ran only one branch (%d accepted, %d refused) — a corpus "+
			"that drifted to all-one-kind proves nothing", accepted, refused)
	}
}

// TestManifestVersionCorpusAgreesWithThePinnedSDK holds this repo's gate and the
// SDK's exported face to the same corpus, for the rows where the SDK's face can
// be asked at all.
//
// The SDK takes a string, so it can only be asked about a `ver` that is present.
// An absent member is the half of the policy this repo re-implements, and the
// row above is what covers it. Where both can answer, a disagreement means the
// repo's copy has drifted from the rule it mirrors.
func TestManifestVersionCorpusAgreesWithThePinnedSDK(t *testing.T) {
	t.Parallel()
	corpus := loadManifestVersionCorpus(t)
	var asked int
	for _, v := range corpus {
		if !v.Present {
			continue
		}
		asked++
		sdkAccepted := helpers.CheckWellKnownManifestVersion(v.Ver) == nil
		if sdkAccepted != v.Accepted {
			t.Errorf("corpus row %q says accepted=%v, and the pinned SDK's "+
				"CheckWellKnownManifestVersion(%q) says %v — the copy and the module "+
				"disagree, so refresh the corpus from the module",
				v.Name, v.Accepted, v.Ver, sdkAccepted)
		}
		repoAccepted := forawellknown.ValidateManifest(manifestAt(v)) == nil
		if repoAccepted != sdkAccepted {
			t.Errorf("ver=%q: this repo accepts=%v and the pinned SDK accepts=%v",
				v.Ver, repoAccepted, sdkAccepted)
		}
	}
	if asked == 0 {
		t.Fatal("no corpus row carries a present ver, so the SDK's face was never asked")
	}
}
