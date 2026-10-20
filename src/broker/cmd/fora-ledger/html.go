package main

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"time"
)

// The two output formats. Text is the default and is what the terminal has
// always shown; HTML writes a file an operator can read in a browser.
const (
	formatText = "text"
	formatHTML = "html"
)

// Output says where the rendered ledger goes and what happens once it is there.
type Output struct {
	Format string
	// Path is the HTML report's location; empty means a generated name in the
	// current directory. Text output ignores it.
	Path string
	// Open launches a browser on the saved HTML report. Text output ignores it.
	Open bool

	// openBrowser and now are the two things a test must not do for real: one
	// would open a window on the developer's desktop, the other would make the
	// generated filename unrepeatable.
	openBrowser func(ctx context.Context, path string) error
	now         func() time.Time
}

// emit renders the ledger in the requested format. Text goes to stdout as it
// always has. HTML is written to a file first, its absolute path is printed,
// and only then is a browser tried: a browser that fails to open costs the
// operator a warning, never the report.
func emit(ctx context.Context, stdout io.Writer, out Output, l Ledger) error {
	if out.Format == formatText {
		return Render(stdout, l)
	}
	var buf bytes.Buffer
	if err := RenderHTML(&buf, l, out.now()); err != nil {
		return err
	}
	path := out.Path
	if path == "" {
		path = defaultReportName(l.TransactionID, out.now())
	}
	abs, err := writeReport(path, buf.Bytes())
	if err != nil {
		return err
	}
	line(stdout, "report saved to %s\n", abs)
	if !out.Open {
		return nil
	}
	if err := out.openBrowser(ctx, abs); err != nil {
		line(stdout, "warning: could not open a browser (%v); open the report above by hand\n", err)
	}
	return nil
}

//go:embed report.html.tmpl
var reportTemplate string

// report is the template's view of a Ledger: the ledger itself plus the
// counts the summary callout keys its colour and wording on.
type report struct {
	Ledger
	Tally     Tally
	Summary   string
	Generated string
}

// RenderHTML writes the same chain and assertions Render prints, as one
// self-contained page: the styling is inline, nothing is fetched, and every
// dynamic value passes through html/template's contextual escaping.
func RenderHTML(w io.Writer, l Ledger, now time.Time) error {
	tmpl, err := template.New("report").Parse(reportTemplate)
	if err != nil {
		return fmt.Errorf("parse report template: %w", err)
	}
	return tmpl.Execute(w, report{
		Ledger:    l,
		Tally:     TallyOf(l.Assertions),
		Summary:   Summarize(l.Assertions),
		Generated: now.UTC().Format("2006-01-02 15:04:05 UTC"),
	})
}

// unsafeInName matches everything a filename should not carry over from a
// transaction id: path separators, shell metacharacters, whitespace, and
// anything outside ASCII. The id is an operator-typed value and lands in a
// path on their machine, so it is reduced to a conservative alphabet rather
// than trusted.
var unsafeInName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// defaultReportName builds ledger-<transaction>-<UTC stamp>.html. The
// transaction id is sanitized and capped so a hostile or merely long id cannot
// escape the directory or exceed a filesystem's name limit.
func defaultReportName(txID string, now time.Time) string {
	safe := unsafeInName.ReplaceAllString(txID, "_")
	if safe == "" || safe == "." || safe == ".." {
		safe = "tx"
	}
	if len(safe) > 64 {
		safe = safe[:64]
	}
	return fmt.Sprintf("ledger-%s-%s-UTC.html", safe, now.UTC().Format("20060102-150405"))
}

// writeReport creates the file, refusing to replace one that already exists,
// and returns its absolute path. 0600 because the report names a tenant and
// its transaction; nothing in it is secret, but nothing in it is for anyone
// other than the operator who ran the tool either.
func writeReport(path string, content []byte) (string, error) {
	f, err := os.OpenFile(filepath.Clean(path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("%s already exists; pass -output to choose another name", path)
		}
		return "", fmt.Errorf("write report: %w", err)
	}
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("write report: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("write report: %w", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve report path: %w", err)
	}
	return abs, nil
}

// openBrowser hands the report to the desktop's default handler. Only macOS
// and Linux are covered; anywhere else the caller gets an error naming the
// platform and keeps the file.
func openBrowser(ctx context.Context, path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "open", path)
	case "linux":
		cmd = exec.CommandContext(ctx, "xdg-open", path)
	default:
		return fmt.Errorf("no browser launcher for %s", runtime.GOOS)
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", cmd.Path, err)
	}
	return nil
}
