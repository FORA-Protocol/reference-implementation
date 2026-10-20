package transport_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExchangeFault_SingleGenericEnvelopeSource guards that the generic
// (no-reason) ExchangeService ErrorDetail envelope has exactly ONE source:
// connectserver.AttachErrorDetail, the shared SDK call that builds it, appears
// once in the exchange transport package's non-test source, in
// genericFaultError. executeTxError uses the distinct typed-reason
// connectserver.AttachDetail path, so it is not counted.
//
// It is a structural guard because the property is not observable through the
// RPC surface: a second wrapper building a byte-identical envelope answers
// exactly like the first, until the two drift when the envelope changes (a new
// metadata key, a per-RPC domain). The behaviour itself is pinned elsewhere,
// through the real Connect RPC surface: the assertReportRejectionField
// assertions in report_usage_integration_test.go pin the ReportUsage envelope,
// and assertErrorDomain in exchange_negatives_integration_test.go pins the
// generic DiscoverResources envelope.
func TestExchangeFault_SingleGenericEnvelopeSource(t *testing.T) {
	t.Parallel()

	const marker = "connectserver.AttachErrorDetail("

	// go test runs with cwd = package directory, so the package's own source
	// files are the non-test *.go files here. Scanning source text (not runtime
	// behavior) is the sanctioned shape ONLY because this is a behavior-preserving
	// dedup with no observable-to-change envelope.
	goFiles, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob package sources: %v", err)
	}

	var sites []string
	for _, f := range goFiles {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, readErr := os.ReadFile(f)
		if readErr != nil {
			t.Fatalf("read %s: %v", f, readErr)
		}
		lines := strings.Split(string(src), "\n")
		for i, line := range lines {
			if strings.Contains(line, marker) {
				sites = append(sites, filepath.Base(f)+":"+itoa(i+1))
			}
		}
	}

	if len(sites) != 1 {
		t.Fatalf("generic ExchangeService fault envelope has %d source(s) via %q, want exactly 1 "+
			"(genericFaultError is the single source of truth; reportUsageError must delegate, "+
			"not duplicate the AttachErrorDetail body). Call sites: %v",
			len(sites), marker, sites)
	}
}

// itoa avoids a strconv import for the single line-number rendering above.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
