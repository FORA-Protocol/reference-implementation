package db_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
	"strings"
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
)

// sqlc diff in both quality targets binds these generated constants to the
// migration sources. SQL NULL represents UNSPECIFIED, so it is not an enum label.
func TestDenialEnumMatchesProtocol(t *testing.T) {
	want := map[string]bool{}
	for n, name := range forav1.DenialReason_name {
		if n != 0 {
			value := strings.TrimPrefix(name, "DENIAL_REASON_")
			want[value] = true
			if !sqlc.ForaDenialReason(value).Valid() {
				t.Errorf("generated enum rejects protocol reason %s", value)
			}
		}
	}
	f, err := parser.ParseFile(token.NewFileSet(), "sqlc/models.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	ast.Inspect(f, func(node ast.Node) bool {
		v, ok := node.(*ast.ValueSpec)
		if !ok {
			return true
		}
		typ, ok := v.Type.(*ast.Ident)
		if !ok || typ.Name != "ForaDenialReason" {
			return true
		}
		for _, expr := range v.Values {
			lit, ok := expr.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				t.Fatal("sqlc enum value is not a string literal")
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatal(err)
			}
			got[value] = true
		}
		return false
	})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("database enum = %v; protocol enum = %v", got, want)
	}
}
