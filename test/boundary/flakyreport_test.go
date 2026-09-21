package boundary

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The verdict scripts/flaky-report.mjs fails a gate with. CI's --retries=1
// turns a spec that fails once and passes into an exit-0 "flaky", so this
// sentence is the only thing that reaches the person who has to act on it.
const flakyVerdict = "a retry passed this spec; ledger it with evidence or fix it"

// runFlakyReport runs the audit the way check-e2e does and answers its exit
// code and its whole output.
func runFlakyReport(t *testing.T, root, report, ledger string) (int, string) {
	t.Helper()
	out, err := exec.Command("node", filepath.Join(root, "scripts", "flaky-report.mjs"), report, ledger).CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0, string(out)
	case errors.As(err, &exit):
		return exit.ExitCode(), string(out)
	default:
		t.Fatalf("flaky-report.mjs: %v", err)
		return 0, ""
	}
}

// TestFlakyReportFailsOnlyAnUnledgeredRetry drives the audit over two reports
// that differ in one thing: whether the spec a retry passed has a ledger row.
func TestFlakyReportFailsOnlyAnUnledgeredRetry(t *testing.T) {
	root := repoRoot(t)
	data := filepath.Join(root, "test", "boundary", "testdata")
	ledger := filepath.Join(data, "flaky-ledger.md")

	code, out := runFlakyReport(t, root, filepath.Join(data, "flaky-ledgered.json"), ledger)
	if code != 0 {
		t.Errorf("a ledgered flake failed the gate (exit %d):\n%s", code, out)
	}
	if !strings.Contains(out, "apps/desktop/e2e/lost-release.spec.ts:42") {
		t.Errorf("a ledgered flake was not reported at all — the retry is still invisible:\n%s", out)
	}

	code, out = runFlakyReport(t, root, filepath.Join(data, "flaky-unledgered.json"), ledger)
	if code == 0 {
		t.Errorf("an unledgered flake passed the gate:\n%s", out)
	}
	if !strings.Contains(out, "apps/desktop/e2e-web/web-core.spec.ts:9") || !strings.Contains(out, flakyVerdict) {
		t.Errorf("the failure names neither the spec nor what to do about it:\n%s", out)
	}
}

// TestFlakyReportReadsTheRealLedger pins that the script and
// TestFlakyLedgerIndexesEveryFlakeNote agree on what a row looks like: a
// backticked repo-relative spec path in the row's first cell. The fixture
// names a spec docs/flake-ledger.md carries, so a row format only one side
// understands fails here.
func TestFlakyReportReadsTheRealLedger(t *testing.T) {
	root := repoRoot(t)
	report := filepath.Join(root, "test", "boundary", "testdata", "flaky-ledgered.json")
	code, out := runFlakyReport(t, root, report, filepath.Join(root, "docs", "flake-ledger.md"))
	if code != 0 {
		t.Errorf("docs/flake-ledger.md carries lost-release.spec.ts, and the script did not find it (exit %d):\n%s", code, out)
	}
}

// TestGatesAuditTheReportThePlaywrightConfigsWrite pins the two halves of one
// path: a config names the file the run's JSON report lands in, and the
// Makefile hands that file to the audit. Drift makes the audit read a stale
// report, or none, and say nothing.
func TestGatesAuditTheReportThePlaywrightConfigsWrite(t *testing.T) {
	root := repoRoot(t)
	mk, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	configs, err := filepath.Glob(filepath.Join(root, "apps", "desktop", "playwright*.config.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) == 0 {
		t.Fatal("no playwright config under apps/desktop")
	}
	outputFile := regexp.MustCompile(`outputFile:\s*'([^']+)'`)
	for _, c := range configs {
		src, err := os.ReadFile(c)
		if err != nil {
			t.Fatal(err)
		}
		m := outputFile.FindSubmatch(src)
		if m == nil {
			t.Errorf("%s emits no JSON report, so a retry that passes leaves the run with no record", filepath.Base(c))
			continue
		}
		audited := "$(FLAKY_REPORT) $(DESKTOP)/" + string(m[1])
		if !strings.Contains(string(mk), audited) {
			t.Errorf("%s writes its report to %s and no Makefile gate audits it (expected a recipe line %q)",
				filepath.Base(c), m[1], audited)
		}
	}
}
