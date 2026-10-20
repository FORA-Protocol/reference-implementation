// Structural guard: a test fixture never holds a fixed host port.
//
// The integration fixtures start their containers through testcontainers, and
// Docker publishes each exposed port on a host port Docker picks. The test reads
// that port back with MappedPort, so nothing else on the machine has to know it.
// A fixture that asks for a port of its own, 8080 say, works when it runs alone
// and fails as soon as a second run, or anything else on the machine, holds that
// port first. The Zitadel tier did exactly that: it bound host port 8080, so two
// runs of it could not overlap.
//
// In the testcontainers-go version go.mod pins, a fixture can hold a host port in
// two ways only:
//   - port bindings on the container's host config, with a HostPort other than ""
//     or "0" (Docker reads both of those as "pick one");
//   - host networking, where every port the container opens is a port on the host.
//
// A "host:container" entry in ExposedPorts is refused when the container is
// created, so that spelling fails on its own. An empty ExposedPorts publishes the
// image's own ports, on host ports Docker picks.
//
// So the check reads every file that imports a Docker client (testcontainers or
// the moby API) and reports any write of PortBindings, any HostPort that is not ""
// or "0", and any write of NetworkMode. testcontainers already binds every exposed
// port, so a fixture needs no bindings of its own, and fixtures join a network
// through Networks, never through NetworkMode.
package guards

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// dockerClientImportPrefixes are the import paths that make a file a container
// fixture: a file can only start or configure a container through one of them.
var dockerClientImportPrefixes = []string{
	"github.com/testcontainers/testcontainers-go",
	"github.com/moby/moby/",
	"github.com/docker/docker/",
}

// hostPortSentinelFile is the fixture that used to bind a fixed host port. The
// guard fails when it did not read this file, so a walk that found no fixtures
// at all cannot pass.
const hostPortSentinelFile = "internal/testutil/zitadel.go"

// hostPortSite is one place in a fixture that holds, or may hold, a fixed host
// port.
type hostPortSite struct {
	Pos     token.Pos
	Message string
}

// TestDockerFixturesHoldNoFixedHostPort fails when a file that starts or
// configures containers can hold a fixed host port, and when the walk did not
// read the fixture that once held one.
func TestDockerFixturesHoldNoFixedHostPort(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	fset := token.NewFileSet()
	readSentinel := false
	for _, rel := range appSourceAndTestFiles(t, root) {
		file, err := parser.ParseFile(fset, filepath.Join(root, rel), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", rel, err)
		}
		if !importsDockerClient(file) {
			continue
		}
		readSentinel = readSentinel || rel == hostPortSentinelFile
		for _, site := range fixedHostPortSites(file) {
			t.Errorf("%s:%d: %s", rel, fset.Position(site.Pos).Line, site.Message)
		}
	}
	if !readSentinel {
		t.Errorf("%s was not read as a container fixture: it moved, stopped importing a Docker "+
			"client, or the walk no longer reaches it. Point hostPortSentinelFile at the fixture "+
			"that starts Zitadel, so this guard cannot pass without reading a fixture.",
			hostPortSentinelFile)
	}
}

// importsDockerClient reports whether f imports testcontainers or the moby API.
func importsDockerClient(f *ast.File) bool {
	for _, imp := range f.Imports {
		if imp.Path == nil {
			continue
		}
		importPath, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		for _, prefix := range dockerClientImportPrefixes {
			if strings.HasPrefix(importPath, prefix) {
				return true
			}
		}
	}
	return false
}

// fixedHostPortSites is the guard's verdict on one fixture file. It reads keyed
// composite literals (HostConfig{PortBindings: ...}, PortBinding{HostPort: ...})
// and assignments (hc.PortBindings = ..., hc.PortBindings[p][0].HostPort = ...).
func fixedHostPortSites(f *ast.File) []hostPortSite {
	consts := stringConstsIn(f)
	var sites []hostPortSite
	note := func(pos token.Pos, field string, value ast.Expr) {
		if message := hostPortVerdict(field, value, consts); message != "" {
			sites = append(sites, hostPortSite{Pos: pos, Message: message})
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.KeyValueExpr:
			if key, isIdent := node.Key.(*ast.Ident); isIdent {
				note(node.Pos(), key.Name, node.Value)
			}
		case *ast.AssignStmt:
			for i, lhs := range node.Lhs {
				note(lhs.Pos(), assignedField(lhs), assignedValue(node, i))
			}
		}
		return true
	})
	return sites
}

// assignedField names the field an assignment writes: the last selector once
// indexing, dereferencing and parentheses are removed. So
// hc.PortBindings[p] = b writes PortBindings, and
// hc.PortBindings[p][0].HostPort = "8080" writes HostPort. A plain variable
// names no field.
func assignedField(lhs ast.Expr) string {
	for {
		switch e := lhs.(type) {
		case *ast.SelectorExpr:
			return e.Sel.Name
		case *ast.IndexExpr:
			lhs = e.X
		case *ast.StarExpr:
			lhs = e.X
		case *ast.ParenExpr:
			lhs = e.X
		default:
			return ""
		}
	}
}

// assignedValue is the value assigned to the i-th left-hand side. It is nil when
// the statement does not pair the two sides one to one (a call returning several
// values, or an operator assignment such as +=); the verdict treats nil as a
// value it cannot settle.
func assignedValue(assign *ast.AssignStmt, i int) ast.Expr {
	if assign.Tok != token.ASSIGN || len(assign.Lhs) != len(assign.Rhs) {
		return nil
	}
	return assign.Rhs[i]
}

// hostPortVerdict is the message for one write of field, or "" when that write
// cannot hold a fixed host port.
func hostPortVerdict(field string, value ast.Expr, consts map[string]string) string {
	switch field {
	case "PortBindings":
		return "writes PortBindings. testcontainers already publishes every exposed port on a " +
			"host port Docker picks, so a fixture needs no bindings of its own, and a binding " +
			"is how a fixed host port gets in. Expose the port and read it back with MappedPort."
	case "NetworkMode":
		return "writes NetworkMode. With host networking every port the container opens is a " +
			"port on the host; join a network through Networks instead."
	case "HostPort":
		hostPort, settled := resolvedString(value, consts)
		if !settled {
			return `sets HostPort from a value this file does not settle. Only "" or "0" lets ` +
				"Docker pick the host port; any other value may be a fixed one."
		}
		if hostPort != "" && hostPort != "0" {
			return fmt.Sprintf(`sets HostPort to %q, a fixed host port. Leave it empty or "0" `+
				"so Docker picks one, and read it back with MappedPort.", hostPort)
		}
	}
	return ""
}

// --- meta-tests: the guard fires on each broken shape, and not on the good ones --

// dockerFixtureSource wraps body in a file that imports what a fixture imports,
// so each meta-test shows only the shape it is about.
func dockerFixtureSource(body string) string {
	return "package fixture\n\n" +
		"import (\n" +
		"\t\"github.com/moby/moby/api/types/container\"\n" +
		"\tmobynet \"github.com/moby/moby/api/types/network\"\n" +
		"\t\"github.com/testcontainers/testcontainers-go\"\n" +
		")\n\n" +
		"func fixture(hc *container.HostConfig, port mobynet.Port, bindings mobynet.PortMap) any {\n" +
		body + "\n\treturn nil\n}\n"
}

// hostPortSitesIn parses body as a fixture and returns the guard's verdict on it.
func hostPortSitesIn(t *testing.T, body string) []hostPortSite {
	t.Helper()
	return fixedHostPortSites(parseSnippet(t, dockerFixtureSource(body)))
}

// TestMeta_HostPortDetectorCatchesTheRetiredZitadelShape feeds the guard the
// modifier the Zitadel fixture used to carry: one PortBindings write and one
// HostPort read from a constant, so exactly two sites.
func TestMeta_HostPortDetectorCatchesTheRetiredZitadelShape(t *testing.T) {
	t.Parallel()
	got := hostPortSitesIn(t, `	const zitadelHostPort = "8080"
	hc.PortBindings = mobynet.PortMap{
		mobynet.MustParsePort(zitadelHostPort + "/tcp"): []mobynet.PortBinding{
			{HostIP: netip.MustParseAddr("0.0.0.0"), HostPort: zitadelHostPort},
		},
	}`)
	if len(got) != 2 ||
		!strings.HasPrefix(got[0].Message, "writes PortBindings") ||
		!strings.HasPrefix(got[1].Message, `sets HostPort to "8080"`) {
		t.Fatalf("want the PortBindings write and HostPort \"8080\", got %+v", got)
	}
}

func TestMeta_HostPortDetectorCatchesEveryPortBindingsWrite(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		"\thc.PortBindings = bindings",
		"\thc.PortBindings[port] = bindings[port]",
		"\t(*hc).PortBindings = bindings",
		"\t_ = container.HostConfig{PortBindings: bindings}",
	} {
		got := hostPortSitesIn(t, body)
		if len(got) != 1 || !strings.HasPrefix(got[0].Message, "writes PortBindings") {
			t.Errorf("%q: want one PortBindings site, got %+v", body, got)
		}
	}
}

func TestMeta_HostPortDetectorCatchesAFixedHostPort(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		"\t_ = []mobynet.PortBinding{{HostPort: \"8080\"}}",
		"\tconst fixed = \"5432\"\n\t_ = mobynet.PortBinding{HostPort: fixed}",
		"\t_ = mobynet.PortBinding{HostPort: strconv.Itoa(int(port.Num()))}",
		"\thc.PortBindings[port][0].HostPort = \"8080\"",
	} {
		got := hostPortSitesIn(t, body)
		if len(got) != 1 || !strings.HasPrefix(got[0].Message, "sets HostPort") {
			t.Errorf("%q: want one HostPort site, got %+v", body, got)
		}
	}
}

func TestMeta_HostPortDetectorCatchesHostNetworking(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		"\t_ = testcontainers.ContainerRequest{NetworkMode: \"host\"}",
		"\thc.NetworkMode = container.NetworkMode(\"host\")",
		"\thc.NetworkMode = mobynet.NetworkHost",
	} {
		got := hostPortSitesIn(t, body)
		if len(got) != 1 || !strings.HasPrefix(got[0].Message, "writes NetworkMode") {
			t.Errorf("%q: want one NetworkMode site, got %+v", body, got)
		}
	}
}

// TestMeta_HostPortDetectorPassesTheShapesFixturesUse proves the guard accepts
// what the fixtures write today, so it cannot pass by rejecting everything.
func TestMeta_HostPortDetectorPassesTheShapesFixturesUse(t *testing.T) {
	t.Parallel()
	got := hostPortSitesIn(t, `	const anyPort = "0"
	_ = testcontainers.ContainerRequest{
		ExposedPorts: []string{"3000/tcp"},
		HostConfigModifier: func(hc *container.HostConfig) {
			hc.SecurityOpt = []string{"seccomp=unconfined"}
			hc.CapAdd = []string{"IPC_LOCK"}
		},
		ConfigModifier: func(c *container.Config) { c.User = "0" },
	}
	_ = []mobynet.PortBinding{{HostPort: "0"}, {HostPort: ""}, {HostPort: anyPort}}
	if hc.NetworkMode == "host" {
		return nil
	}`)
	if len(got) != 0 {
		t.Fatalf("the guard objected to shapes that hold no fixed host port: %+v", got)
	}
}

func TestMeta_DockerClientFilterRecognizesFixtureImports(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		src  string
		want bool
	}{
		{dockerFixtureSource(""), true},
		{"package f\n\nimport tcpostgres \"github.com/testcontainers/testcontainers-go/modules/postgres\"\n", true},
		{"package f\n\nimport \"github.com/docker/docker/client\"\n", true},
		{"package f\n\nimport \"net/http\"\n\nconst image = \"github.com/testcontainers/testcontainers-go\"\n", false},
	} {
		if got := importsDockerClient(parseSnippet(t, tc.src)); got != tc.want {
			t.Errorf("importsDockerClient = %v, want %v, for:\n%s", got, tc.want, tc.src)
		}
	}
}
