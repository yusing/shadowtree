package shadowtreelsp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestDocumentDiagnosticsRejectNonPositivePosition(t *testing.T) {
	for _, value := range []string{"-1", "0"} {
		t.Run(value, func(t *testing.T) {
			text := `[recipes.build]
cmd = "go build"

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
cmd = "go build"

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
cmd = "go build"

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

func TestServerShutdownResponseIncludesNullResult(t *testing.T) {
	var input bytes.Buffer
	if err := writeMessage(&input, rpcMessage{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "shutdown"}); err != nil {
		t.Fatal(err)
	}
	if err := writeMessage(&input, rpcMessage{JSONRPC: "2.0", Method: "exit"}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := Serve(t.Context(), &input, &output); err != nil {
		t.Fatal(err)
	}

	body, err := readMessage(bufio.NewReader(bytes.NewReader(output.Bytes())))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error,omitempty"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	if string(response.Result) != "null" || response.Error != nil {
		t.Fatalf("response = %s, want result null and no error", body)
	}
}

func TestServerUnknownRequestReturnsMethodNotFound(t *testing.T) {
	var input bytes.Buffer
	if err := writeMessage(&input, rpcMessage{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "shadowtree/unknown"}); err != nil {
		t.Fatal(err)
	}
	if err := writeMessage(&input, rpcMessage{JSONRPC: "2.0", Method: "exit"}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := Serve(t.Context(), &input, &output); err != nil {
		t.Fatal(err)
	}

	body, err := readMessage(bufio.NewReader(bytes.NewReader(output.Bytes())))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Error *rpcError `json:"error,omitempty"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	if response.Error == nil || response.Error.Code != -32601 {
		t.Fatalf("response = %s, want method-not-found error", body)
	}
}

func TestServerIgnoresMalformedNotification(t *testing.T) {
	var input bytes.Buffer
	if err := writeMessage(&input, rpcMessage{
		JSONRPC: "2.0",
		Method:  "textDocument/didOpen",
		Params:  json.RawMessage(`[]`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := writeMessage(&input, rpcMessage{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "shutdown"}); err != nil {
		t.Fatal(err)
	}
	if err := writeMessage(&input, rpcMessage{JSONRPC: "2.0", Method: "exit"}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := Serve(t.Context(), &input, &output); err != nil {
		t.Fatal(err)
	}

	body, err := readMessage(bufio.NewReader(bytes.NewReader(output.Bytes())))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error,omitempty"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	if string(response.Result) != "null" || response.Error != nil {
		t.Fatalf("response = %s, want shutdown result null", body)
	}
}

func TestReadMessageRejectsOversizedContentLength(t *testing.T) {
	header := "Content-Length: " + strconv.Itoa(maxLSPMessageBytes+1) + "\r\n\r\n"
	_, err := readMessage(bufio.NewReader(strings.NewReader(header)))
	if err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("err = %v, want content length limit", err)
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
cmd = "go build"
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
cmd = "go test"
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
cmd = "go test"

[recipes.build]
cmd = "go build"
`)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsRejectUnknownBracketRecipeReference(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test]
pre = ["@missing[component=godoxy]"]
cmd = "go test"
`)
	assertOneDiagnostic(t, diagnostics, "unknown recipe reference @missing")
	assertDiagnosticRange(t, diagnostics[0], 1, len(`pre = ["`), len(`pre = ["@missing[component=godoxy]`))
}

func TestDocumentDiagnosticsRejectArgvPreCommand(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test]
pre = [
  ["@missing"]
]
cmd = "go test"
`)
	assertOneDiagnostic(t, diagnostics, "command arrays are no longer supported; use a shell string")
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
cmd = "true"
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
cmd = "go test"
`)
	assertOneDiagnostic(t, diagnostics, "unknown recipe reference @missing")
	assertDiagnosticRange(t, diagnostics[0], 1, len(`values = "`), len(`values = "@missing`))
}

func TestDocumentDiagnosticsRejectUnknownScriptValuesRecipeReference(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test.arguments.target]
values = '''
@missing
'''

[recipes.test]
cmd = "go test"
`)
	assertOneDiagnostic(t, diagnostics, "unknown recipe reference @missing")
	assertDiagnosticRange(t, diagnostics[0], 2, 0, len(`@missing`))
}

func TestDocumentDiagnosticsAcceptEnumArgumentValues(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test.arguments.target]
values = "@enum api worker \"admin ui\""

[recipes.test]
cmd = "go test"
`)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsAcceptDiscoveryArgumentValues(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test.arguments.target]
values = "@go-modules"

[recipes.build.arguments.project]
values = "@go-main-packages"

[recipes.lint.arguments.pkg]
values = "@go-packages"

[recipes.test]
cmd = "go test"

[recipes.build]
cmd = "go build"

[recipes.lint]
cmd = "go vet"
`)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsAllowOpenDiscoveryArgumentValues(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test.arguments.pkg]
type = "rel_path"
values = "@go-packages"

[recipes.run.arguments.command]
type = "rel_path"
values = '@go-main-packages; @glob "*.go"'

[recipes.test]
cmd = "go test {pkg}"

[recipes.run]
cmd = "go run {command}"

[recipes.check]
cmd = '''
@test[pkg=./internal/...]
@run[command=./cmd/api/main.go]
'''
`)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsAcceptDiscoveryArgumentValuesWithoutRecipeCommand(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test.arguments.pkg]
values = "@go-modules"
default = "."
`)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsAcceptScriptDiscoveryArgumentValues(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test.arguments.pkg]
values = '''
@go-modules
'''
default = "."
`)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsAcceptGoModulesAfterTripleQuotedCommandList(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.install]
post = ['''
echo installed
''']

[recipes.test.arguments.pkg]
values = "@go-modules"
default = "."
`)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsAcceptComposedBuiltinArgumentValues(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test.arguments.target]
values = "@go-modules; @enum all='all modules'"

[recipes.test]
cmd = "go test"
`)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsAcceptComposedBuiltinForEach(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.lint]
for_each = "@go-modules; @enum all='all modules'"
cmd = "go test"
`)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsAcceptDiscoveryForEach(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.lint]
for_each = "@go-modules"
cmd = "go test"
`)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsAcceptScriptDiscoveryForEach(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.lint]
for_each = '''
@go-modules
'''
cmd = "go test"
`)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsRejectInvalidForEachBuiltin(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.lint]
for_each = "@go-modules x"
cmd = "go test"
`)
	assertOneDiagnostic(t, diagnostics, `recipe "lint" for_each: @go-modules does not take arguments`)
}

func TestDocumentDiagnosticsRejectInvalidArgumentValuesBuiltin(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test.arguments.target]
values = "@glob"

[recipes.test]
cmd = "go test"
`)
	assertOneDiagnostic(t, diagnostics, `recipe "test" arguments: target values: @glob requires one argument`)
}

func TestDocumentDiagnosticsRejectInvalidDiscoveryArgumentValuesBuiltin(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test.arguments.target]
values = "@go-modules x"

[recipes.test]
cmd = "go test"
`)
	assertOneDiagnostic(t, diagnostics, `recipe "test" arguments: target values: @go-modules does not take arguments`)
}

func TestDocumentDiagnosticsRejectArgvArgumentValues(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test.arguments.target]
values = ["@enum", "api"]

[recipes.test]
cmd = "go test"
`)
	assertOneDiagnostic(t, diagnostics, "command arrays are no longer supported; use a shell string")
}

func TestDocumentDiagnosticsRejectEmptyArgvArgumentValues(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test.arguments.target]
values = []

[recipes.test]
cmd = "go test"
`)
	assertOneDiagnostic(t, diagnostics, "command arrays are no longer supported; use a shell string")
}

func TestDocumentDiagnosticsRejectShellOperatorComposedBuiltinValues(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test.arguments.target]
values = "@go-modules && @enum all='all modules'"

[recipes.test]
cmd = "go test"
`)
	assertOneDiagnostic(t, diagnostics, `recipe "test" arguments: target values: builtin values must contain only simple builtin commands`)
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
cmd = "go test"
`)
	assertOneDiagnostic(t, diagnostics, "unknown recipe reference @missing")
	assertDiagnosticRange(t, diagnostics[0], 2, 0, len(`@missing`))
}

func TestDocumentDiagnosticsRejectInvalidValuesScriptRecipeReferenceEnumArgumentValue(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.minify.arguments.component]
type = "string"
values = "@enum godoxy agent socket-proxy cli"

[recipes.minify]
cmd = "true"

[recipes.test.arguments.target]
type = "string"
values = '''
@minify component=foo
'''
`)
	assertOneDiagnostic(t, diagnostics, `component: invalid value "foo"`)
	assertDiagnosticRange(t, diagnostics[0], 10, len(`@minify `), len(`@minify component=foo`))
}

func TestDocumentDiagnosticsRejectInvalidValuesScriptRecipeReferenceEnumBracketArgumentValue(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.minify.arguments.component]
type = "string"
values = "@enum godoxy agent socket-proxy cli"

[recipes.minify]
cmd = "true"

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
cmd = "go build"
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
cmd = "go test {pkg} {@}"

[recipes.build.arguments.pkg]
type = "rel_path"
position = 1
default = "."
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
cmd = "go test {pkg} {@}"

[recipes.build.arguments.pkg]
type = "rel_path"
position = 1
default = "."
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
cmd = "go test {pkg} {@}"

[recipes.build.arguments.pkg]
type = "rel_path"
position = 1
default = "."
`)
	assertOneDiagnostic(t, diagnostics, `unknown argument "count"`)
	assertDiagnosticRange(t, diagnostics[0], 2, len(`@build ./internal/recipe `), len(`@build ./internal/recipe count=1`))
}

func TestDocumentDiagnosticsRejectUnknownPlaceholder(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		line  int
		start int
		end   int
	}{
		{
			name: "cmd",
			text: `[recipes.test]
cmd = "echo {non_existent}"
`,
			line:  1,
			start: len(`cmd = "echo `),
			end:   len(`cmd = "echo {non_existent}`),
		},
		{
			name: "workdir",
			text: `[recipes.test]
workdir = "services/{non_existent}"
cmd = "true"
`,
			line:  1,
			start: len(`workdir = "services/`),
			end:   len(`workdir = "services/{non_existent}`),
		},
		{
			name: "pre",
			text: `[recipes.test]
pre = ["echo {non_existent}"]
cmd = "true"
`,
			line:  1,
			start: len(`pre = ["echo `),
			end:   len(`pre = ["echo {non_existent}`),
		},
		{
			name: "post",
			text: `[recipes.test]
cmd = "true"
post = ["echo {non_existent}"]
`,
			line:  2,
			start: len(`post = ["echo `),
			end:   len(`post = ["echo {non_existent}`),
		},
		{
			name: "for_each",
			text: `[recipes.test]
for_each = "@enum {non_existent}"
cmd = "echo {item}"
`,
			line:  1,
			start: len(`for_each = "@enum `),
			end:   len(`for_each = "@enum {non_existent}`),
		},
		{
			name: "sync_out",
			text: `[recipes.test]
cmd = "true"
sync_out = ["out/{non_existent}.txt"]
`,
			line:  2,
			start: len(`sync_out = ["out/`),
			end:   len(`sync_out = ["out/{non_existent}`),
		},
		{
			name: "global vars",
			text: `[vars]
docs_dir = "{non_existent}/wiki"
`,
			line:  1,
			start: len(`docs_dir = "`),
			end:   len(`docs_dir = "{non_existent}`),
		},
		{
			name: "global env",
			text: `[env]
DOCS_DIR = "{non_existent}/wiki"
`,
			line:  1,
			start: len(`DOCS_DIR = "`),
			end:   len(`DOCS_DIR = "{non_existent}`),
		},
		{
			name: "recipe vars",
			text: `[recipes.test.vars]
docs_dir = "{non_existent}/wiki"

[recipes.test]
cmd = "true"
`,
			line:  1,
			start: len(`docs_dir = "`),
			end:   len(`docs_dir = "{non_existent}`),
		},
		{
			name: "recipe env",
			text: `[recipes.test.env]
DOCS_DIR = "{non_existent}/wiki"

[recipes.test]
cmd = "true"
`,
			line:  1,
			start: len(`DOCS_DIR = "`),
			end:   len(`DOCS_DIR = "{non_existent}`),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diagnostics := documentDiagnostics(t.Context(), tc.text)
			assertOneDiagnostic(t, diagnostics, "unknown variable {non_existent}")
			assertDiagnosticRange(t, diagnostics[0], tc.line, tc.start, tc.end)
		})
	}
}

func TestDocumentDiagnosticsAcceptKnownPlaceholders(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[vars]
PROJECT = "./cmd/shadowtree"
DOCS = "{PROJECT}/docs"

[env]
DOCS_DIR = "{DOCS}"

[var_commands]
GENERATED = "printf generated"

[recipes.test.vars]
local = "./internal/shadowtreelsp"
target = "{PROJECT}/{GENERATED}/{local}"

[recipes.test.env]
TARGET = "{target}/{pkg}"

[recipes.test.arguments.pkg]
default = "."

[recipes.test]
for_each = "@enum one two"
workdir = "{item}"
pre = ["echo {PROJECT} {GENERATED} {local} {pkg}"]
cmd = "go test {PROJECT} {GENERATED} {local} {pkg} {item} {item_help} {item_index} {@}"
post = ["echo {PROJECT} {GENERATED} {local} {pkg}"]
sync_out = ["out/{PROJECT}/{GENERATED}/{local}/{pkg}"]
`)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsAcceptMergedBuiltinArgumentPlaceholders(t *testing.T) {
	root := t.TempDir()
	diagnostics := documentDiagnosticsWithOptions(t.Context(), `profile = "go"

[recipes.test.env]
PKG = "{pkg}"

[recipes.build]
cmd = "go build {pkg}"
`, diagnosticOptions{URI: fileURI(filepath.Join(root, ".shadowtree.toml"))})
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsIgnoreNonShadowtreePlaceholders(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test]
shell_prelude = "echo {non_existent}"
cmd = "echo ${HOME}"
`)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsDoNotTreatArgumentNamedVarsAsVarsTable(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test.arguments.vars]
help = "{non_existent}"

[recipes.test]
cmd = "true"
`)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsRejectRecursiveVars(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		messages []string
	}{
		{
			name: "global self reference",
			text: `[vars]
root = "{root}"
`,
			messages: []string{"recursive variable {root}"},
		},
		{
			name: "global indirect cycle",
			text: `[vars]
root = "{docs}"
docs = "{root}/docs"
`,
			messages: []string{"recursive variable {root}", "recursive variable {docs}"},
		},
		{
			name: "recipe self reference",
			text: `[recipes.test.vars]
local = "{local}"

[recipes.test]
cmd = "true"
`,
			messages: []string{"recursive variable {local}"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diagnostics := documentDiagnostics(t.Context(), tc.text)
			for _, message := range tc.messages {
				if !hasDiagnosticMessage(diagnostics, message) {
					t.Fatalf("diagnostics = %#v, missing %q", diagnostics, message)
				}
			}
		})
	}
}

func TestDocumentDiagnosticsIgnoreRecipeReferenceKeysOutsideRecipeTables(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[vars]
cmd = "@missing"

[recipes.test]
cmd = "go test"
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
cmd = "go test"
`)
	assertOneDiagnostic(t, diagnostics, "unknown recipe reference @echo")
	assertDiagnosticRange(t, diagnostics[0], 1, len(`pre = ["`), len(`pre = ["@echo`))
}

func TestDocumentDiagnosticsAcceptDynamicRecipeReference(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test]
pre = ["@{target}"]
cmd = "go test"
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
cmd = "@webui:gen-schema"
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
cmd = "@webui:missing"
`

	diagnostics := documentDiagnosticsWithOptions(t.Context(), text, diagnosticOptions{URI: fileURI(filepath.Join(root, ".shadowtree.toml"))})
	assertOneDiagnostic(t, diagnostics, "unknown recipe reference @webui:missing")
	assertDiagnosticRange(t, diagnostics[0], 1, len(`cmd = "`), len(`cmd = "@webui:missing`))
}

func TestDocumentDiagnosticsRejectCrossConfigMissingConfig(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "webui"), 0o755); err != nil {
		t.Fatal(err)
	}
	text := `[recipes.test]
cmd = "@webui:gen-schema"
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
values = "@enum godoxy agent socket-proxy cli"

[recipes.minify]
cmd = "true"
`
	if err := os.WriteFile(filepath.Join(target, ".shadowtree.toml"), []byte(targetConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	text := `[recipes.test]
cmd = "@webui:minify[component=foo]"
`

	diagnostics := documentDiagnosticsWithOptions(t.Context(), text, diagnosticOptions{URI: fileURI(filepath.Join(root, ".shadowtree.toml"))})
	assertOneDiagnostic(t, diagnostics, `component: invalid value "foo"`)
	assertDiagnosticRange(t, diagnostics[0], 1, len(`cmd = "@webui:minify[`), len(`cmd = "@webui:minify[component=foo`))
}

func TestDocumentDiagnosticsDoNotRunCommandBackedArgumentValues(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	text := `[recipes.build.arguments.component]
type = "string"
values = ` + strconv.Quote("touch "+marker+"\nprintf api") + `

[recipes.build]
cmd = "go build"

[recipes.test]
cmd = "@build[component=api]"
`
	diagnostics := documentDiagnostics(t.Context(), text)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("command-backed values ran, stat err = %v", err)
	}
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
cmd = "true"
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
cmd = "go build"
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
cmd = "go build"

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

func hasDiagnosticMessage(diagnostics []lspDiagnostic, message string) bool {
	return slices.ContainsFunc(diagnostics, func(diagnostic lspDiagnostic) bool {
		return diagnostic.Message == message
	})
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
	text := "[recipes." + recipeName + "]\nhelp = \"Target recipe help.\"\ncmd = \"true\"\n"
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
