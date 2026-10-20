// This file proves that a production build cannot contain the e2e-only control
// service (ADR-028 D6).
//
// The control service sets state no admin RPC reaches, per-tenant offer and
// delivery-URL lifetimes among it. It must exist only in binaries built with the
// e2e build tag. The structural property is that every file of the control
// package carries `//go:build e2e`, and that nothing compiled without the tag
// imports it. The checks below read those facts with the go/build package, under
// the same build constraints the compiler applies, rather than trusting a comment:
//
//   - without the tag, the control package has no Go files at all, so a
//     production build that imported it could not compile;
//   - without the tag, no package under src/ or internal/ imports it, so no
//     production binary links it, directly or through another package;
//   - with the tag, the Exchange's composition root does import it. That is the
//     control case: it shows this guard can see the import it forbids, so the
//     second check would fail if an untagged file gained it.
package guards

import (
	"errors"
	"go/build"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// e2eControlPackages are the e2e-only control packages, by repo-relative directory.
var e2eControlPackages = []string{
	"src/exchange/internal/e2econtrol",
}

// e2eControlWiring is the composition root whose e2e-tagged file imports the
// Exchange's control package.
const e2eControlWiring = "src/exchange/cmd/server"

const e2eTag = "e2e"

// buildContext is the default build context with exactly tags set, so the
// guard sees the files the compiler would compile for that tag set.
func buildContext(tags ...string) build.Context {
	ctx := build.Default
	ctx.BuildTags = tags
	return ctx
}

func isE2EControlImport(path string) bool {
	return slices.ContainsFunc(e2eControlPackages, func(dir string) bool {
		return strings.HasSuffix(path, "/"+dir)
	})
}

func TestE2EControlPackagesHaveNoFilesWithoutTheTag(t *testing.T) {
	root := repoRoot(t)
	untagged := buildContext()
	tagged := buildContext(e2eTag)
	for _, dir := range e2eControlPackages {
		abs := filepath.Join(root, dir)
		_, err := untagged.ImportDir(abs, 0)
		var none *build.NoGoError
		if !errors.As(err, &none) {
			t.Errorf("%s compiles files without the %s tag (err=%v); every file must carry //go:build %s",
				dir, e2eTag, err, e2eTag)
		}
		if _, err := tagged.ImportDir(abs, 0); err != nil {
			t.Errorf("%s has no files under the %s tag: %v", dir, e2eTag, err)
		}
	}
}

func TestNoUntaggedPackageImportsAnE2EControlPackage(t *testing.T) {
	root := repoRoot(t)
	untagged := buildContext()
	for _, dir := range goPackageDirs(t, root) {
		pkg, err := untagged.ImportDir(filepath.Join(root, dir), 0)
		if err != nil {
			continue // a directory every one of whose files is tagged out, or none is Go
		}
		for _, imported := range pkg.Imports {
			if isE2EControlImport(imported) {
				t.Errorf("%s imports %s without the %s tag, so a production binary would carry it",
					dir, imported, e2eTag)
			}
		}
	}
}

func TestTheE2EBuildImportsTheControlPackage(t *testing.T) {
	tagged := buildContext(e2eTag)
	pkg, err := tagged.ImportDir(filepath.Join(repoRoot(t), e2eControlWiring), 0)
	if err != nil {
		t.Fatalf("import %s under the %s tag: %v", e2eControlWiring, e2eTag, err)
	}
	if !slices.ContainsFunc(pkg.Imports, isE2EControlImport) {
		t.Fatalf("%s does not import the control package under the %s tag, so the guard "+
			"above checks for an import that never exists", e2eControlWiring, e2eTag)
	}
}

// goPackageDirs is every directory under src/ and internal/ holding a .go file,
// repo-relative. node_modules and testdata trees hold no package of this module.
func goPackageDirs(t *testing.T, root string) []string {
	t.Helper()
	seen := map[string]bool{}
	for _, top := range []string{"src", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && (d.Name() == "node_modules" || d.Name() == "testdata") {
				return filepath.SkipDir
			}
			if !d.IsDir() && strings.HasSuffix(path, ".go") {
				rel, relErr := filepath.Rel(root, filepath.Dir(path))
				if relErr != nil {
					return relErr
				}
				seen[rel] = true
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", top, err)
		}
	}
	dirs := make([]string, 0, len(seen))
	for dir := range seen {
		dirs = append(dirs, dir)
	}
	slices.Sort(dirs)
	return dirs
}
