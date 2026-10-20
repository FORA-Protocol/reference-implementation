// The licensed-scope rule is pure logic whose branches are the point, and its
// cases are shared with the edge worker's copy of it, so a change to either
// that the other does not make fails here or there.
package licensedscope_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/licensedscope"
)

// sharedVectors is the repo-root case file the edge worker's inLicensedScope
// and this package are both pinned to. The path is relative to this test's
// package directory.
const sharedVectors = "../../testdata/licensed-scope-vectors.json"

type vector struct {
	Name    string   `json:"name"`
	Scope   []string `json:"scope"`
	Path    string   `json:"path"`
	Verdict string   `json:"verdict"`
}

type vectorFile struct {
	Comment []string `json:"comment"`
	Vectors []vector `json:"vectors"`
}

// loadVectors reads the case file strictly, as the edge's reader does: an
// unknown key is a typo in a case, and a typo that silently drops a field
// would leave the case testing nothing.
func loadVectors(t *testing.T) []vector {
	t.Helper()
	f, err := os.Open(filepath.Clean(sharedVectors))
	if err != nil {
		t.Fatalf("open shared vectors: %v", err)
	}
	defer func() { _ = f.Close() }()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	var vf vectorFile
	if err := dec.Decode(&vf); err != nil {
		t.Fatalf("decode shared vectors: %v", err)
	}
	if len(vf.Vectors) == 0 {
		t.Fatal("the shared vectors file has no vectors")
	}
	return vf.Vectors
}

// TestClassify_SharedVectors runs every shared case, after checking that the
// file names each case once and holds every verdict.
func TestClassify_SharedVectors(t *testing.T) {
	t.Parallel()
	vectors := loadVectors(t)
	names := map[string]bool{}
	verdicts := map[licensedscope.Verdict]bool{}
	for _, v := range vectors {
		if names[v.Name] {
			t.Fatalf("the vector name %q is used twice", v.Name)
		}
		names[v.Name] = true
		verdicts[licensedscope.Verdict(v.Verdict)] = true
	}
	for _, want := range []licensedscope.Verdict{licensedscope.Inside, licensedscope.Outside, licensedscope.Undecidable} {
		if !verdicts[want] {
			t.Errorf("no vector has the verdict %q", want)
		}
		delete(verdicts, want)
	}
	for unknown := range verdicts {
		t.Errorf("a vector has the unknown verdict %q", unknown)
	}

	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			t.Parallel()
			if got := licensedscope.Classify(v.Path, v.Scope); string(got) != v.Verdict {
				t.Errorf("Classify(%q, %q) = %q, want %q", v.Path, v.Scope, got, v.Verdict)
			}
		})
	}
}
