package layout

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// viewContractPath is the wire contract shared with the dashboard; its
// layoutViewContract.test.ts checks LayoutView/RetryAck and the status unions
// against the same file, so a renamed, newly omitempty or added field (for
// example available or resultGeneration) fails on whichever side drifted.
var viewContractPath = filepath.Join("..", "..", "dashboard", "src", "graph", "arranged", "layoutView.contract.json")

type wireKeys struct {
	Required []string `json:"required"`
	Optional []string `json:"optional"`
}

type viewContract struct {
	View          wireKeys `json:"view"`
	RetryAck      wireKeys `json:"retryAck"`
	DisplayStatus []string `json:"displayStatus"`
	Provenance    []string `json:"provenance"`
}

func jsonKeys(t *testing.T, v any) wireKeys {
	t.Helper()
	out := wireKeys{Required: []string{}, Optional: []string{}}
	typ := reflect.TypeOf(v)
	for i := range typ.NumField() {
		f := typ.Field(i)
		name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
		require.NotEmpty(t, name, "%s.%s needs an explicit JSON name", typ.Name(), f.Name)
		require.NotEqual(t, "-", name, "%s.%s is not on the wire", typ.Name(), f.Name)
		if slices.Contains(strings.Split(opts, ","), "omitempty") {
			out.Optional = append(out.Optional, name)
		} else {
			out.Required = append(out.Required, name)
		}
	}
	slices.Sort(out.Required)
	slices.Sort(out.Optional)
	return out
}

// constValues reads every declared constant of typeName from the package
// source, so a new status cannot be added without updating the contract.
func constValues(t *testing.T, typeName string) []string {
	t.Helper()
	pkgs, err := parser.ParseDir(token.NewFileSet(), ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	require.NoError(t, err)
	var out []string
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.CONST {
					continue
				}
				for _, spec := range gen.Specs {
					vs := spec.(*ast.ValueSpec)
					if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != typeName {
						continue
					}
					for _, val := range vs.Values {
						lit, ok := val.(*ast.BasicLit)
						require.True(t, ok && lit.Kind == token.STRING, "%s constants must be string literals", typeName)
						s, err := strconv.Unquote(lit.Value)
						require.NoError(t, err)
						out = append(out, s)
					}
				}
			}
		}
	}
	require.NotEmpty(t, out, "no %s constants found", typeName)
	slices.Sort(out)
	return out
}

func TestViewWireContract_MatchesDashboardFixture(t *testing.T) {
	raw, err := os.ReadFile(viewContractPath)
	require.NoError(t, err)
	var fixture viewContract
	require.NoError(t, json.Unmarshal(raw, &fixture))

	got := viewContract{
		View:          jsonKeys(t, View{}),
		RetryAck:      jsonKeys(t, RetryAck{}),
		DisplayStatus: constValues(t, "DisplayStatus"),
		Provenance:    constValues(t, "Provenance"),
	}
	require.Equal(t, fixture, got,
		"layout.View/RetryAck JSON or status constants drifted from %s; update it and dashboard/src/graph/arranged/types.ts together", viewContractPath)
}

// A View marshals exactly its declared required keys when every optional field is
// empty: an unavailable or pending response still carries available,
// resultGeneration, unreviewed and provenance for the client to act on.
func TestViewWireContract_EmptyViewCarriesEveryRequiredKey(t *testing.T) {
	raw, err := json.Marshal(View{DisplayStatus: DisplayUnavailable})
	require.NoError(t, err)
	var body map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &body))
	keys := make([]string, 0, len(body))
	for k := range body {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	require.Equal(t, jsonKeys(t, View{}).Required, keys)
	require.JSONEq(t, "false", string(body["available"]))
}
