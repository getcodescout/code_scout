package mcptools

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type rawOut struct {
	Meta json.RawMessage `json:"meta"`
}

type anyOut struct {
	Meta any `json:"meta"`
}

// addTool sends its own bytes instead of the SDK's, so the SDK never checks
// them and addTool has to: json.RawMessage infers as an array of bytes, and a
// tool that sends an object there would go out broken to every client that
// validates.
func TestAddToolStillChecksOutputAgainstItsSchema(t *testing.T) {
	s := mcp.NewServer(&mcp.Implementation{Name: "addtool-test"}, nil)
	addTool(s, &mcp.Tool{Name: "raw", Description: "a RawMessage carrying an object"},
		func(context.Context, *mcp.CallToolRequest, emptyIn) (*mcp.CallToolResult, rawOut, error) {
			return nil, rawOut{Meta: json.RawMessage(`{"a":1}`)}, nil
		})
	addTool(s, &mcp.Tool{Name: "free", Description: "an any carrying an object"},
		func(context.Context, *mcp.CallToolRequest, emptyIn) (*mcp.CallToolResult, anyOut, error) {
			return nil, anyOut{Meta: map[string]any{"a": 1}}, nil
		})

	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := s.Connect(context.Background(), serverT, nil); err != nil {
		t.Fatalf("connect server: %v", err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(context.Background(), clientT, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() { cs.Close() })

	raw := call(t, cs, "raw", map[string]any{})
	if !raw.IsError {
		t.Fatalf("an output that breaks its own schema was sent: %s", resultText(raw))
	}
	if text := resultText(raw); text != "internal error" {
		t.Errorf("the refusal should be a bare internal error, got %q", text)
	}

	free := call(t, cs, "free", map[string]any{})
	if free.IsError {
		t.Fatalf("a valid output was refused: %s", resultText(free))
	}
	if got := resultText(free); got != `{"meta":{"a":1}}` {
		t.Errorf("the text should be the marshalled output, got %q", got)
	}

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	for _, tool := range tools.Tools {
		schema, _ := json.Marshal(tool.OutputSchema)
		if !strings.Contains(string(schema), `"meta"`) {
			t.Errorf("tool %q no longer advertises its output schema: %s", tool.Name, schema)
		}
	}
}

// A tool registered with mcp.AddTool lists, answers and passes every test that
// does not look at a number, while it rounds every number it sends (pinned in
// sdk_shape_test.go). Nothing at run time tells it from one registered through
// addTool, so the rule is checked in the source: no use of mcp.AddTool in this
// package outside addTool, under whatever name the SDK is imported.
func TestOnlyAddToolCallsTheSDKsAddTool(t *testing.T) {
	const sdk = "github.com/modelcontextprotocol/go-sdk/mcp"

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("list the package's files: %v", err)
	}
	fset := token.NewFileSet()
	insideAddTool := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}

		pkg := ""
		for _, imp := range f.Imports {
			if path, _ := strconv.Unquote(imp.Path.Value); path == sdk {
				pkg = "mcp"
				if imp.Name != nil {
					pkg = imp.Name.Name
				}
			}
		}
		if pkg == "" || pkg == "_" {
			continue
		}
		if pkg == "." {
			t.Errorf("%s imports the MCP SDK with a dot, which hides its AddTool from this test", name)
			continue
		}

		for _, decl := range f.Decls {
			fn, isFunc := decl.(*ast.FuncDecl)
			allowed := isFunc && fn.Recv == nil && fn.Name.Name == "addTool"
			ast.Inspect(decl, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "AddTool" {
					return true
				}
				if x, ok := sel.X.(*ast.Ident); !ok || x.Name != pkg {
					return true
				}
				if allowed {
					insideAddTool++
				} else {
					t.Errorf("%s: %s.AddTool outside addTool. Register the tool with addTool, which sends numbers as they were written",
						fset.Position(sel.Pos()), pkg)
				}
				return true
			})
		}
	}
	// The one use that is allowed. Without it, this test is looking for the
	// wrong name and would pass whatever the package did.
	if insideAddTool != 1 {
		t.Errorf("found mcp.AddTool %d times inside addTool, want 1", insideAddTool)
	}
}
