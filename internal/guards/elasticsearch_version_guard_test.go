// Structural guard: one Elasticsearch version.
//
// The version is written in places that cannot reference each other: the image
// the integration tests start, the image the e2e stack runs, and the version the
// operator documentation calls tested. A move of any one of them alone would
// leave one tier proving a version the others do not run.
package guards

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
)

func TestElasticsearchVersionIsOneValue(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	_, version, ok := strings.Cut(testutil.ElasticsearchImage, ":")
	if !ok {
		t.Fatalf("testutil.ElasticsearchImage = %q has no tag", testutil.ElasticsearchImage)
	}

	for file, want := range map[string]string{
		"docker-compose.e2e.yml":      "image: " + testutil.ElasticsearchImage + "\n",
		"src/broker/CONFIGURATION.md": "Tested with Elasticsearch " + version + ".",
	} {
		raw, err := os.ReadFile(filepath.Join(root, file)) //nolint:gosec // fixed paths under the repository root
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), want) {
			t.Errorf("%s does not contain %q: the Elasticsearch version has moved in one place only", file, want)
		}
	}
}
