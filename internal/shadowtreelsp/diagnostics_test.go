package shadowtreelsp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDocumentDiagnosticsRejectNonPositivePosition(t *testing.T) {
	for _, value := range []string{"-1", "0"} {
		t.Run(value, func(t *testing.T) {
			text := `[recipes.build]
cmd = ["go", "build"]

[recipes.build.arguments.project]
position = ` + value + `
`
			diagnostics := documentDiagnostics(t.Context(), text)
			if len(diagnostics) != 1 {
				t.Fatalf("diagnostics = %#v, want one diagnostic", diagnostics)
			}
			if diagnostics[0].Message != "position must be 1 or greater" {
				t.Fatalf("message = %q", diagnostics[0].Message)
			}
			assertDiagnosticRange(t, diagnostics[0], 4, len("position = "), len("position = ")+len(value))
		})
	}
}

func TestServerPublishesDiagnosticsOnOpen(t *testing.T) {
	text := `[recipes.build]
cmd = ["go", "build"]

[recipes.build.arguments.project]
position = -1
`
	var out bytes.Buffer
	server := &server{
		ctx:       t.Context(),
		output:    &out,
		documents: map[string]string{},
	}
	params := didOpenParams{}
	params.TextDocument.URI = "file:///shadowtree.toml"
	params.TextDocument.Version = new(1)
	params.TextDocument.Text = text
	body, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.handle(rpcMessage{
		JSONRPC: "2.0",
		Method:  "textDocument/didOpen",
		Params:  body,
	}); err != nil {
		t.Fatal(err)
	}

	notification := readTestMessage(t, out.Bytes())
	paramsOut := diagnosticsParams(t, notification)
	if paramsOut.URI != "file:///shadowtree.toml" {
		t.Fatalf("uri = %q", paramsOut.URI)
	}
	if paramsOut.Version == nil || *paramsOut.Version != 1 {
		t.Fatalf("version = %#v, want 1", paramsOut.Version)
	}
	if len(paramsOut.Diagnostics) != 1 || paramsOut.Diagnostics[0].Message != "position must be 1 or greater" {
		t.Fatalf("diagnostics = %#v", paramsOut.Diagnostics)
	}
}

func TestServerClearsDiagnosticsAfterIncrementalUndo(t *testing.T) {
	text := `[recipes.build]
cmd = ["go", "build"]

[recipes.build.arguments.project]
position = -1
`
	var out bytes.Buffer
	server := &server{
		ctx:       t.Context(),
		output:    &out,
		documents: map[string]string{"file:///shadowtree.toml": text},
	}
	params := didChangeParams{}
	params.TextDocument.URI = "file:///shadowtree.toml"
	params.TextDocument.Version = new(2)
	params.ContentChanges = []contentChange{{
		Range: &lspTextRange{
			Start: lspPosition{Line: 4, Character: len("position = ")},
			End:   lspPosition{Line: 4, Character: len("position = -1")},
		},
		Text: "1",
	}}
	body, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.handle(rpcMessage{
		JSONRPC: "2.0",
		Method:  "textDocument/didChange",
		Params:  body,
	}); err != nil {
		t.Fatal(err)
	}
	if server.documents["file:///shadowtree.toml"] != strings.Replace(text, "-1", "1", 1) {
		t.Fatalf("document = %q", server.documents["file:///shadowtree.toml"])
	}

	paramsOut := diagnosticsParams(t, readTestMessage(t, out.Bytes()))
	if paramsOut.Version == nil || *paramsOut.Version != 2 {
		t.Fatalf("version = %#v, want 2", paramsOut.Version)
	}
	if paramsOut.Diagnostics == nil {
		t.Fatalf("diagnostics = nil, want empty slice")
	}
	if len(paramsOut.Diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want cleared", paramsOut.Diagnostics)
	}
}

func TestServerClearsDiagnosticsOnClose(t *testing.T) {
	var out bytes.Buffer
	server := &server{
		ctx:       t.Context(),
		output:    &out,
		documents: map[string]string{"file:///shadowtree.toml": "position = -1"},
	}
	params := didCloseParams{}
	params.TextDocument.URI = "file:///shadowtree.toml"
	body, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.handle(rpcMessage{
		JSONRPC: "2.0",
		Method:  "textDocument/didClose",
		Params:  body,
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := server.documents["file:///shadowtree.toml"]; ok {
		t.Fatalf("document was not removed")
	}

	paramsOut := diagnosticsParams(t, readTestMessage(t, out.Bytes()))
	if paramsOut.URI != "file:///shadowtree.toml" {
		t.Fatalf("uri = %q", paramsOut.URI)
	}
	if paramsOut.Version != nil {
		t.Fatalf("version = %#v, want omitted", paramsOut.Version)
	}
	if paramsOut.Diagnostics == nil {
		t.Fatalf("diagnostics = nil, want empty slice")
	}
	if len(paramsOut.Diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want cleared", paramsOut.Diagnostics)
	}
}

func TestDocumentDiagnosticsRejectSyntaxError(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.build]
cmd = [
`)
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %#v, want one diagnostic", diagnostics)
	}
	if diagnostics[0].Message == "" {
		t.Fatalf("diagnostic has empty message: %#v", diagnostics[0])
	}
}

func TestDocumentDiagnosticsRejectUnknownRecipeField(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.build]
cmd = ["go", "build"]
unknown = true
`)
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %#v, want one diagnostic", diagnostics)
	}
	if diagnostics[0].Message != "unknown field recipes.build.unknown" {
		t.Fatalf("message = %q", diagnostics[0].Message)
	}
	assertDiagnosticRange(t, diagnostics[0], 2, 0, len("unknown"))
}

func TestDocumentDiagnosticsRejectUnknownRecipeReference(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test]
pre = ["echo 123", "@missing"]
cmd = ["go", "test"]
`)
	assertOneDiagnostic(t, diagnostics, "unknown recipe reference @missing")
	assertDiagnosticRange(t, diagnostics[0], 1, len(`pre = ["echo 123", "`), len(`pre = ["echo 123", "@missing`))
}

func TestDocumentDiagnosticsRejectGoBuiltinWhenConfigOmitsProfile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	text := `[recipes.assets]
cmd = "@fmt"
`
	diagnostics := documentDiagnosticsWithOptions(t.Context(), text, diagnosticOptions{URI: fileURI(filepath.Join(root, ".shadowtree.toml"))})
	assertOneDiagnostic(t, diagnostics, "unknown recipe reference @fmt")
}

func TestDocumentDiagnosticsAcceptGoBuiltinWhenConfigSetsProfile(t *testing.T) {
	text := `profile = "go"

[recipes.assets]
cmd = "@fmt"
`
	diagnostics := documentDiagnostics(t.Context(), text)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsAcceptNodeBuiltinWhenConfigSetsProfile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"scripts":{"test":"vitest"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	text := `profile = "node"

[recipes.assets]
cmd = "@install"
`
	diagnostics := documentDiagnosticsWithOptions(t.Context(), text, diagnosticOptions{URI: fileURI(filepath.Join(root, ".shadowtree.toml"))})
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsAcceptKnownBracketRecipeReference(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test]
pre = ["@build[component=godoxy, mode=dev]"]
cmd = ["go", "test"]

[recipes.build]
cmd = ["go", "build"]
`)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsRejectUnknownBracketRecipeReference(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test]
pre = ["@missing[component=godoxy]"]
cmd = ["go", "test"]
`)
	assertOneDiagnostic(t, diagnostics, "unknown recipe reference @missing")
	assertDiagnosticRange(t, diagnostics[0], 1, len(`pre = ["`), len(`pre = ["@missing[component=godoxy]`))
}

func TestDocumentDiagnosticsRejectUnknownMultilineRecipeReference(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test]
pre = [
  ["@missing"]
]
cmd = ["go", "test"]
`)
	assertOneDiagnostic(t, diagnostics, "unknown recipe reference @missing")
	assertDiagnosticRange(t, diagnostics[0], 2, len(`  ["`), len(`  ["@missing`))
}

func TestDocumentDiagnosticsRejectUnknownScalarRecipeReference(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test]
cmd = "@missing"
`)
	assertOneDiagnostic(t, diagnostics, "unknown recipe reference @missing")
	assertDiagnosticRange(t, diagnostics[0], 1, len(`cmd = "`), len(`cmd = "@missing`))
}

func TestDocumentDiagnosticsRejectUnknownScriptRecipeReference(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test]
cmd = '''
if [ -f schema.json ]; then
  @missing value=shadow
fi
'''
`)
	assertOneDiagnostic(t, diagnostics, "unknown recipe reference @missing")
	assertDiagnosticRange(t, diagnostics[0], 3, len(`  `), len(`  @missing`))
}

func TestDocumentDiagnosticsAcceptKnownScriptRecipeReference(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test]
cmd = '''
if [ -f schema.json ]; then
  @gen value=shadow
fi
'''

[recipes.gen]
cmd = ["true"]
`)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsIgnoreScriptRecipeReferenceText(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test]
cmd = '''
FOO="@missing"
echo "@also_missing"
# @comment
'''
`)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsIgnoreScriptHereDocBody(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test]
cmd = '''
cat <<EOF
@missing
EOF
'''
`)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsIgnoreMultilineScriptString(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test]
cmd = '''
printf "%s
@missing
"
'''
`)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsRejectUnknownScalarValuesRecipeReference(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test.arguments.target]
values = "@missing"

[recipes.test]
cmd = ["go", "test"]
`)
	assertOneDiagnostic(t, diagnostics, "unknown recipe reference @missing")
	assertDiagnosticRange(t, diagnostics[0], 1, len(`values = "`), len(`values = "@missing`))
}

func TestDocumentDiagnosticsAcceptEnumArgumentValues(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test.arguments.target]
values = "@enum api worker \"admin ui\""

[recipes.test]
cmd = ["go", "test"]
`)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsRejectInvalidArgumentValuesBuiltin(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test.arguments.target]
values = "@glob"

[recipes.test]
cmd = ["go", "test"]
`)
	assertOneDiagnostic(t, diagnostics, `recipe "test" arguments: target values: @glob requires one argument`)
}

func TestDocumentDiagnosticsRejectEnumOutsideArgumentValues(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test]
cmd = "@enum api worker"
`)
	assertOneDiagnostic(t, diagnostics, "unknown recipe reference @enum")
	assertDiagnosticRange(t, diagnostics[0], 1, len(`cmd = "`), len(`cmd = "@enum`))
}

func TestDocumentDiagnosticsRejectUnknownValuesScriptRecipeReferenceWithQuotedArgument(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test.arguments.target]
values = '''
@missing component="godoxy"
'''

[recipes.test]
cmd = ["go", "test"]
`)
	assertOneDiagnostic(t, diagnostics, "unknown recipe reference @missing")
	assertDiagnosticRange(t, diagnostics[0], 2, 0, len(`@missing`))
}

func TestDocumentDiagnosticsRejectInvalidValuesScriptRecipeReferenceArgumentValue(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.minify.arguments.component]
type = "string"
values = "printf '%s\\n' godoxy agent socket-proxy cli"

[recipes.minify]
cmd = ["true"]

[recipes.test.arguments.target]
type = "string"
values = '''
@minify component=foo
'''
`)
	assertOneDiagnostic(t, diagnostics, `component: invalid value "foo"`)
	assertDiagnosticRange(t, diagnostics[0], 10, len(`@minify `), len(`@minify component=foo`))
}

func TestDocumentDiagnosticsRejectInvalidValuesScriptRecipeReferenceBracketArgumentValue(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.minify.arguments.component]
type = "string"
values = "printf '%s\\n' godoxy agent socket-proxy cli"

[recipes.minify]
cmd = ["true"]

[recipes.test.arguments.target]
type = "string"
values = '''
@minify[component=foo]
'''
`)
	assertOneDiagnostic(t, diagnostics, `component: invalid value "foo"`)
	assertDiagnosticRange(t, diagnostics[0], 10, len(`@minify[`), len(`@minify[component=foo`))
}

func TestDocumentDiagnosticsRejectInvalidScriptRecipeReferenceArgumentType(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test]
cmd = '''
@build mode=nope
'''

[recipes.build.arguments.mode]
type = "bool"

[recipes.build]
cmd = ["go", "build"]
`)
	assertOneDiagnostic(t, diagnostics, `mode: want bool, got "nope"`)
	assertDiagnosticRange(t, diagnostics[0], 2, len(`@build `), len(`@build mode=nope`))
}

func TestDocumentDiagnosticsAllowRecipeReferenceVariadicArgs(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test]
cmd = '''
@build -race ./internal/recipe -run=TestResolve -count 1
'''

[recipes.build]
cmd = ["go", "test"]
default_args = ["{pkg}", "{@}"]

[recipes.build.arguments.pkg]
type = "rel_path"
position = 1
default = "./..."
`)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsAllowRecipeReferenceVariadicArgsAfterSeparator(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test]
cmd = '''
@build pkg=./internal/recipe -- pkg=literal
'''

[recipes.build]
cmd = ["go", "test"]
default_args = ["{pkg}", "{@}"]

[recipes.build.arguments.pkg]
type = "rel_path"
position = 1
default = "./..."
`)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsRejectUnknownNamedArgWithVariadicArgs(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test]
cmd = '''
@build ./internal/recipe count=1
'''

[recipes.build]
cmd = ["go", "test"]
default_args = ["{pkg}", "{@}"]

[recipes.build.arguments.pkg]
type = "rel_path"
position = 1
default = "./..."
`)
	assertOneDiagnostic(t, diagnostics, `unknown argument "count"`)
	assertDiagnosticRange(t, diagnostics[0], 2, len(`@build ./internal/recipe `), len(`@build ./internal/recipe count=1`))
}

func TestDocumentDiagnosticsIgnoreRecipeReferenceKeysOutsideRecipeTables(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[vars]
cmd = "@missing"

[recipes.test]
cmd = ["go", "test"]
`)
	if diagnostics == nil {
		t.Fatalf("diagnostics = nil, want empty slice")
	}
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsRejectUnknownScriptCommandStartingWithAt(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test]
pre = ["@echo hi"]
cmd = ["go", "test"]
`)
	assertOneDiagnostic(t, diagnostics, "unknown recipe reference @echo")
	assertDiagnosticRange(t, diagnostics[0], 1, len(`pre = ["`), len(`pre = ["@echo`))
}

func TestDocumentDiagnosticsAcceptDynamicRecipeReference(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test]
pre = [["@{target}"]]
cmd = ["go", "test"]
`)
	if diagnostics == nil {
		t.Fatalf("diagnostics = nil, want empty slice")
	}
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsAcceptCrossConfigRecipeReference(t *testing.T) {
	root := t.TempDir()
	writeLSPTargetConfig(t, root, "gen-schema")
	text := `[recipes.test]
cmd = ["@webui:gen-schema"]
`

	diagnostics := documentDiagnosticsWithOptions(t.Context(), text, diagnosticOptions{URI: fileURI(filepath.Join(root, ".shadowtree.toml"))})
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsRejectCrossConfigMissingRecipe(t *testing.T) {
	root := t.TempDir()
	writeLSPTargetConfig(t, root, "gen-schema")
	text := `[recipes.test]
cmd = ["@webui:missing"]
`

	diagnostics := documentDiagnosticsWithOptions(t.Context(), text, diagnosticOptions{URI: fileURI(filepath.Join(root, ".shadowtree.toml"))})
	assertOneDiagnostic(t, diagnostics, "unknown recipe reference @webui:missing")
	assertDiagnosticRange(t, diagnostics[0], 1, len(`cmd = ["`), len(`cmd = ["@webui:missing`))
}

func TestDocumentDiagnosticsRejectCrossConfigMissingConfig(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "webui"), 0o755); err != nil {
		t.Fatal(err)
	}
	text := `[recipes.test]
cmd = ["@webui:gen-schema"]
`

	diagnostics := documentDiagnosticsWithOptions(t.Context(), text, diagnosticOptions{URI: fileURI(filepath.Join(root, ".shadowtree.toml"))})
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %#v, want one diagnostic", diagnostics)
	}
	if !strings.Contains(diagnostics[0].Message, "invalid recipe reference @webui:gen-schema") ||
		!strings.Contains(diagnostics[0].Message, ".shadowtree.toml") {
		t.Fatalf("message = %q, want missing config", diagnostics[0].Message)
	}
}

func TestDocumentDiagnosticsRejectCrossConfigInvalidArgumentValue(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "webui")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	targetConfig := `[recipes.minify.arguments.component]
type = "string"
values = "printf '%s\\n' godoxy agent socket-proxy cli"

[recipes.minify]
cmd = ["true"]
`
	if err := os.WriteFile(filepath.Join(target, ".shadowtree.toml"), []byte(targetConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	text := `[recipes.test]
cmd = ["@webui:minify[component=foo]"]
`

	diagnostics := documentDiagnosticsWithOptions(t.Context(), text, diagnosticOptions{URI: fileURI(filepath.Join(root, ".shadowtree.toml"))})
	assertOneDiagnostic(t, diagnostics, `component: invalid value "foo"`)
	assertDiagnosticRange(t, diagnostics[0], 1, len(`cmd = ["@webui:minify[`), len(`cmd = ["@webui:minify[component=foo`))
}

func TestDocumentDiagnosticsRejectCrossConfigInvalidArgumentType(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "webui")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	targetConfig := `[recipes.build.arguments.mode]
type = "bool"

[recipes.build]
cmd = ["true"]
`
	if err := os.WriteFile(filepath.Join(target, ".shadowtree.toml"), []byte(targetConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	text := `[recipes.test]
cmd = '''
@webui:build mode=nope
'''
`

	diagnostics := documentDiagnosticsWithOptions(t.Context(), text, diagnosticOptions{URI: fileURI(filepath.Join(root, ".shadowtree.toml"))})
	assertOneDiagnostic(t, diagnostics, `mode: want bool, got "nope"`)
	assertDiagnosticRange(t, diagnostics[0], 2, len(`@webui:build `), len(`@webui:build mode=nope`))
}

func TestDocumentDiagnosticsAcceptSchemaKey(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `"$schema" = "https://example.com/schema.json"

[recipes.build]
cmd = ["go", "build"]
`)
	if diagnostics == nil {
		t.Fatalf("diagnostics = nil, want empty slice")
	}
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsAcceptValidConfig(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.build]
cmd = ["go", "build"]

[recipes.build.arguments.project]
position = 1
`)
	if diagnostics == nil {
		t.Fatalf("diagnostics = nil, want empty slice")
	}
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func assertDiagnosticRange(t *testing.T, diagnostic lspDiagnostic, line, start, end int) {
	t.Helper()
	editRange := diagnostic.Range
	startPos, ok := editRange["start"].(lspPosition)
	if !ok {
		t.Fatalf("start has type %T", editRange["start"])
	}
	endPos, ok := editRange["end"].(lspPosition)
	if !ok {
		t.Fatalf("end has type %T", editRange["end"])
	}
	got := []int{startPos.Line, startPos.Character, endPos.Character}
	want := []int{line, start, end}
	if !slices.Equal(got, want) {
		t.Fatalf("range = %#v, want %#v", got, want)
	}
}

func assertOneDiagnostic(t *testing.T, diagnostics []lspDiagnostic, message string) {
	t.Helper()
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %#v, want one diagnostic", diagnostics)
	}
	if diagnostics[0].Message != message {
		t.Fatalf("message = %q, want %q", diagnostics[0].Message, message)
	}
}

func diagnosticsParams(t *testing.T, notification rpcMessage) struct {
	URI         string          `json:"uri"`
	Version     *int            `json:"version,omitempty"`
	Diagnostics []lspDiagnostic `json:"diagnostics"`
} {
	t.Helper()
	if notification.Method != "textDocument/publishDiagnostics" {
		t.Fatalf("method = %q", notification.Method)
	}
	var paramsOut struct {
		URI         string          `json:"uri"`
		Version     *int            `json:"version,omitempty"`
		Diagnostics []lspDiagnostic `json:"diagnostics"`
	}
	if err := json.Unmarshal(notification.Params, &paramsOut); err != nil {
		t.Fatal(err)
	}
	return paramsOut
}

func writeLSPTargetConfig(t *testing.T, root, recipeName string) {
	t.Helper()
	target := filepath.Join(root, "webui")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	text := "[recipes." + recipeName + "]\nhelp = \"Target recipe help.\"\ncmd = [\"true\"]\n"
	if err := os.WriteFile(filepath.Join(target, ".shadowtree.toml"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fileURI(path string) string {
	return "file://" + filepath.ToSlash(path)
}

func readTestMessage(t *testing.T, data []byte) rpcMessage {
	t.Helper()
	body, err := readMessage(bufio.NewReader(bytes.NewReader(data)))
	if err != nil {
		t.Fatal(err)
	}
	var msg rpcMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		t.Fatal(err)
	}
	return msg
}
