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

func TestDocumentDiagnosticsRejectUnsupportedShell(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `shell = "fish"

[recipes.test]
cmd = "true"
`)
	assertOneDiagnostic(t, diagnostics, `shell must be sh or bash, got "fish"`)
	assertDiagnosticRange(t, diagnostics[0], 0, len(`shell = `), len(`shell = "fish"`))
}

func TestDocumentDiagnosticsRangesQuotedInvalidShellValue(t *testing.T) {
	for _, value := range []string{`"fish shell"`, `"fi#sh"`} {
		t.Run(value, func(t *testing.T) {
			line := `shell = ` + value
			diagnostics := documentDiagnostics(t.Context(), line+`

[recipes.test]
cmd = "true"
`)
			assertOneDiagnostic(t, diagnostics, `shell must be sh or bash, got `+value)
			assertDiagnosticRange(t, diagnostics[0], 0, len(`shell = `), len(line))
		})
	}
}

func TestDocumentDiagnosticsRejectInvalidDurationDefault(t *testing.T) {
	cases := []struct {
		name    string
		typ     string
		value   string
		message string
	}{
		{name: "missing unit", typ: "duration", value: "10", message: `recipe "benchmark" arguments: duration default: duration: want duration, got "10"`},
		{name: "fractional seconds", typ: "duration:seconds", value: "1500ms", message: `recipe "benchmark" arguments: duration default: duration: want whole-second duration, got "1500ms"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line := `default = "` + tc.value + `"`
			diagnostics := documentDiagnostics(t.Context(), `[recipes.benchmark.arguments.duration]
type = "`+tc.typ+`"
`+line+`

[recipes.benchmark]
cmd = "bench {duration}"
`)
			assertOneDiagnostic(t, diagnostics, tc.message)
			assertDiagnosticRange(t, diagnostics[0], 2, len(`default = `), len(line))
		})
	}
}

func TestDocumentDiagnosticsReportInvalidDurationDefaultWithWarnings(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.benchmark.arguments.host]
type = "string"

[recipes.benchmark.arguments.duration]
type = "duration"
default = "1000"

[recipes.benchmark]
cmd = '''bench {host} {duration}'''
`)
	message := `recipe "benchmark" arguments: duration default: duration: want duration, got "1000"`
	if !hasDiagnosticMessage(diagnostics, message) {
		t.Fatalf("diagnostics = %#v, want invalid duration default", diagnostics)
	}
	if len(diagnostics) != 2 {
		t.Fatalf("diagnostics = %#v, want duration error and unsafe placeholder warning", diagnostics)
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.Message == message {
			assertDiagnosticRange(t, diagnostic, 5, len(`default = `), len(`default = "1000"`))
			return
		}
	}
	t.Fatal("duration diagnostic not found")
}

func TestDocumentDiagnosticsReportInvalidDurationDefaultWithUnrelatedError(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.benchmark.arguments.duration]
type = "duration"
default = "1000"

[recipes.benchmark]
cmd = "bench {duration}"
typo = true
`)
	messages := []string{
		`unknown field recipes.benchmark.typo`,
		`recipe "benchmark" arguments: duration default: duration: want duration, got "1000"`,
	}
	for _, message := range messages {
		if !hasDiagnosticMessage(diagnostics, message) {
			t.Fatalf("diagnostics = %#v, want %q", diagnostics, message)
		}
	}
	if len(diagnostics) != len(messages) {
		t.Fatalf("diagnostics = %#v, want %d diagnostics", diagnostics, len(messages))
	}
}

func TestDocumentDiagnosticsRangeIgnoresFakeTOMLInMultilineString(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.fake]
cmd = '''
[recipes.benchmark.arguments.duration]
default = "not-real"
'''

[recipes.benchmark.arguments.duration]
type = "duration"
default = "1000"

[recipes.benchmark]
cmd = "bench {duration}"
`)
	assertOneDiagnostic(t, diagnostics, `recipe "benchmark" arguments: duration default: duration: want duration, got "1000"`)
	assertDiagnosticRange(t, diagnostics[0], 8, len(`default = `), len(`default = "1000"`))
}

func TestDocumentDiagnosticsRangeQuotedDottedArgumentPath(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes."go.test".arguments."duration.value"]
type = "duration"
default = "1000"

[recipes."go.test"]
cmd = "bench {duration.value}"
`)
	assertOneDiagnostic(t, diagnostics, `recipe "go.test" arguments: duration.value default: duration.value: want duration, got "1000"`)
	assertDiagnosticRange(t, diagnostics[0], 2, len(`default = `), len(`default = "1000"`))
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

func TestDocumentDiagnosticsRejectUnknownRequirementField(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.build]
cmd = "go build"

[recipes.build.requires]
commandz = ["go"]
`)
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %#v, want one diagnostic", diagnostics)
	}
	if diagnostics[0].Message != "unknown field recipes.build.requires.commandz" {
		t.Fatalf("message = %q", diagnostics[0].Message)
	}
	assertDiagnosticRange(t, diagnostics[0], 4, 0, len("commandz"))
}

func TestDocumentDiagnosticsAcceptInlineStageCommandFields(t *testing.T) {
	for _, stage := range []string{"pre", "post"} {
		for _, field := range []string{"cmd", "timeout"} {
			t.Run(stage+"."+field, func(t *testing.T) {
				inline := `{ cmd = "printf stage" }`
				if field == "timeout" {
					inline = `{ cmd = "printf stage", timeout = "5s" }`
				}
				diagnostics := documentDiagnostics(t.Context(), `[recipes.inherited-merge]
cmd = "printf merged"

[recipes.kitchen-sink]
`+stage+` = [`+inline+`]
cmd = "true"
`)
				if len(diagnostics) != 0 {
					t.Fatalf("diagnostics = %#v, want none", diagnostics)
				}
			})
		}
	}
}

func TestDocumentDiagnosticsRangeUnknownInlineStageCommandField(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		message string
		line    int
		start   int
		end     int
	}{
		{
			name: "array",
			text: `[recipes.inherited-merge]
cmd = "printf merged"

[recipes.kitchen-sink]
post = [
  { cmd = "printf post", unknown = true },
]
cmd = "true"
`,
			message: "unknown field recipes.kitchen-sink.post.unknown",
			line:    5,
			start:   len(`  { cmd = "printf post", `),
			end:     len(`  { cmd = "printf post", unknown`),
		},
		{
			name: "single table",
			text: `[recipes.inherited-merge]
cmd = "printf merged"

[recipes.kitchen-sink]
pre = { cmd = "printf pre", unknown = true }
cmd = "true"
`,
			message: "unknown field recipes.kitchen-sink.pre.unknown",
			line:    4,
			start:   len(`pre = { cmd = "printf pre", `),
			end:     len(`pre = { cmd = "printf pre", unknown`),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diagnostics := documentDiagnostics(t.Context(), tc.text)
			assertOneDiagnostic(t, diagnostics, tc.message)
			assertDiagnosticRange(t, diagnostics[0], tc.line, tc.start, tc.end)
		})
	}
}

func TestDocumentDiagnosticsRejectInvalidRequirementConfig(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.build]
cmd = "go build"

[recipes.build.requires]
commands = ["go", "go"]
`)
	assertOneDiagnostic(t, diagnostics, `recipe "build" requires: commands[1] duplicates commands[0] "go"`)
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

func TestDocumentDiagnosticsAcceptRetryCommandHelper(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test]
pre = "@retry[count=3,delay=1s] benchmark_prepare"
cmd = "go test"
`)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsRejectRetryUnknownRecipeTarget(t *testing.T) {
	text := `[recipes.test]
pre = "@retry[count=3,delay=1s] @missing"
cmd = "go test"
`
	diagnostics := documentDiagnostics(t.Context(), text)
	assertOneDiagnostic(t, diagnostics, "unknown recipe reference @missing")
}

func TestDocumentDiagnosticsRejectUnknownBracketRecipeReference(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test]
pre = ["@missing[component=godoxy]"]
cmd = "go test"
`)
	assertOneDiagnostic(t, diagnostics, "unknown recipe reference @missing")
	assertDiagnosticRange(t, diagnostics[0], 1, len(`pre = ["`), len(`pre = ["@missing[component=godoxy]`))
}

func TestDocumentDiagnosticsRejectStructuredStageMissingPlaceholder(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test]
cmd = "go test"

[recipes.test.pre]
cmd = "echo {missing}"
`)
	assertOneDiagnostic(t, diagnostics, "unknown variable {missing}")
}

func TestDocumentDiagnosticsAcceptPostStageStatusPlaceholders(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test]
cmd = "go test"
post = ['printf "%s:%s\n" "{status:pre}" "{status:cmd}"']
`)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsAcceptCmdPreStatusPlaceholder(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test]
cmd = "go test {status:pre}"
`)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsAcceptStructuredPostStageStatusPlaceholders(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test]
cmd = "go test"

[recipes.test.post]
cmd = 'printf "%s:%s\n" "{status:pre}" "{status:cmd}"'
`)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsRejectInvalidStageStatusPlaceholder(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test]
cmd = "go test"
post = "echo {status:post}"
`)
	assertOneDiagnostic(t, diagnostics, "{status:post} supports only pre or cmd")
}

func TestDocumentDiagnosticsRejectStageStatusPlaceholderOutsidePost(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test]
cmd = "go test {status:cmd}"
post = "echo done"
`)
	assertOneDiagnostic(t, diagnostics, "{status:cmd} is supported only in post")
}

func TestDocumentDiagnosticsRejectArgvPreCommand(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test]
pre = [
  ["@missing"]
]
cmd = "go test"
`)
	assertOneDiagnostic(t, diagnostics, "[0]: command arrays are no longer supported; use a shell string")
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

func TestDocumentDiagnosticsAcceptDefaultedGoModuleArgumentValues(t *testing.T) {
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
cmd = 'go test "{pkg}"'

[recipes.run]
cmd = 'go run "{command}"'

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

func TestDocumentDiagnosticsAcceptDiscoveryArgumentValues(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test.arguments.pkg]
values = "@go-modules"
default = "."

[recipes.test]
cmd = 'go test "{pkg}"'
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

[recipes.test]
cmd = 'go test "{pkg}"'
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
cmd = "true"

[recipes.test.arguments.pkg]
values = "@go-modules"
default = "."

[recipes.test]
cmd = 'go test "{pkg}"'
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

[recipes.test]
cmd = "true"
`)
	assertOneDiagnostic(t, diagnostics, `component: invalid value "foo"`)
	assertDiagnosticRange(t, diagnostics[0], 10, len(`@minify `), len(`@minify component=foo`))
}

func TestDocumentDiagnosticsRejectUnknownRecipePresetArgument(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.benchmark.arguments.connections]
type = "int"

[recipes.benchmark.presets.stable.arguments]
requests = 20000
`)
	assertOneDiagnostic(t, diagnostics, `recipe "benchmark" presets: stable: unknown argument "requests"`)
	assertDiagnosticRange(t, diagnostics[0], 4, 0, len("requests"))
}

func TestDocumentDiagnosticsRejectRecipeWithoutCommand(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.broken]
help = "Missing command."
`)
	assertOneDiagnostic(t, diagnostics, `recipe "broken" has no cmd`)
}

func TestDocumentDiagnosticsRejectRecipeWithoutCommandAndHelp(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.broken]
sync_out = ["generated.txt"]

[recipes.broken.arguments.target]
default = "api"
`)
	assertOneDiagnostic(t, diagnostics, `recipe "broken" has no cmd`)
}

func TestDocumentDiagnosticsRejectIncludedRecipeWithoutCommand(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "common.shadowtree.toml"), []byte(`
[recipes.broken.arguments.target]
default = "api"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	text := `include = ["./common.shadowtree.toml"]
`

	diagnostics := documentDiagnosticsWithOptions(t.Context(), text, diagnosticOptions{URI: fileURI(filepath.Join(root, ".shadowtree.toml"))})
	assertOneDiagnostic(t, diagnostics, `recipe "broken" has no cmd`)
}

func TestDocumentDiagnosticsRejectInvalidArgumentRange(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.benchmark.arguments.connections]
type = "int"
min = 10
max = 1

[recipes.benchmark]
cmd = "true"
`)
	assertOneDiagnostic(t, diagnostics, `recipe "benchmark" arguments: connections: max must be greater than or equal to min`)
	assertDiagnosticRange(t, diagnostics[0], 3, len("max = "), len("max = 1"))
}

func TestDocumentDiagnosticsRejectPresetArgumentNameAtArgumentTable(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.benchmark.arguments.preset]
type = "string"

[recipes.benchmark.presets.stable.arguments]
preset = "fast"
`)
	assertOneDiagnostic(t, diagnostics, `recipe "benchmark" presets: argument name "preset" is reserved when presets are configured`)
	assertDiagnosticRange(t, diagnostics[0], 0, 1, len(`[recipes.benchmark.arguments.preset`))
}

func TestDocumentDiagnosticsRejectUnknownRecipeReferencePreset(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.benchmark.arguments.connections]
type = "int"

[recipes.benchmark.presets.stable.arguments]
connections = 64

[recipes.benchmark]
cmd = "true"

[recipes.check]
cmd = "@benchmark[preset=stress]"
`)
	assertOneDiagnostic(t, diagnostics, `unknown preset "stress"`)
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

[recipes.test]
cmd = "true"
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
cmd = 'go test "{pkg}" {@}'

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
cmd = 'go test "{pkg}" {@}'

[recipes.build.arguments.pkg]
type = "rel_path"
position = 1
default = "."
`)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsAllowShellVariablesInRecipeReferenceArguments(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.bench]
cmd = '''
pkg=./internal/runner
bench=BenchmarkRun
@test "$pkg" -run '^$' -bench "$bench" -benchtime=1x -count=1 {@}
'''

[recipes.test]
cmd = 'go test "{pkg}" {@}'

[recipes.test.arguments.pkg]
type = "rel_path"
position = 1
default = "."
`)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsValidateSingleQuotedDollarRecipeReferenceArguments(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test]
cmd = '''
@compile flag='$not_bool'
'''

[recipes.compile]
cmd = "go build"

[recipes.compile.arguments.flag]
type = "bool"
`)
	assertOneDiagnostic(t, diagnostics, `flag: want bool, got "$not_bool"`)
}

func TestDocumentDiagnosticsValidateRecipeReferenceArgumentRange(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test]
cmd = '''
@compile workers=0
'''

[recipes.compile]
cmd = "go build"

[recipes.compile.arguments.workers]
type = "int"
min = 1
`)
	assertOneDiagnostic(t, diagnostics, `workers: want >= 1, got "0"`)
}

func TestDocumentDiagnosticsRejectUnknownNamedArgWithDynamicValue(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test]
cmd = '''
@compile typo="$MODE"
'''

[recipes.compile]
cmd = "go build"

[recipes.compile.arguments.mode]
type = "string"
`)
	assertOneDiagnostic(t, diagnostics, `unknown argument "typo"`)
}

func TestDocumentDiagnosticsRejectUnknownNamedArgWithVariadicArgs(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test]
cmd = '''
@build ./internal/recipe count=1
'''

[recipes.build]
cmd = 'go test "{pkg}" {@}'

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
			name: "recipe shell_prelude",
			text: `[recipes.test]
shell_prelude = "echo {non_existent}"
cmd = "true"
`,
			line:  1,
			start: len(`shell_prelude = "echo `),
			end:   len(`shell_prelude = "echo {non_existent}`),
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
			name: "log",
			text: `[recipes.test]
cmd = "true"
log = "logs/{non_existent}.log"
`,
			line:  2,
			start: len(`log = "logs/`),
			end:   len(`log = "logs/{non_existent}`),
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
	diagnostics := documentDiagnostics(t.Context(), `shell_prelude = "echo {PROJECT} {GENERATED}"

[vars]
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
shell_prelude = 'echo {PROJECT} {GENERATED} {local} "{pkg}"'
for_each = "@enum one two"
workdir = "{item}"
pre = ['echo {PROJECT} {GENERATED} {local} "{pkg}"']
cmd = 'go test {PROJECT} {GENERATED} {local} "{pkg}" {item} {item_help} {item_index} {@}'
post = ['echo {PROJECT} {GENERATED} {local} "{pkg}"']
sync_out = ["out/{PROJECT}/{GENERATED}/{local}/{pkg}"]
log = "logs/{run_id}.log"
`)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsAllowRecipePlaceholdersInTopShellPrelude(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `shell_prelude = "echo {pkg}"

[recipes.test.arguments.pkg]
default = "."

[recipes.test]
cmd = "true"
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
cmd = 'go build "{pkg}"'
`, diagnosticOptions{URI: fileURI(filepath.Join(root, ".shadowtree.toml"))})
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsIgnoreNonShadowtreePlaceholders(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.test]
cmd = "echo ${HOME}"
`)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestDocumentDiagnosticsPlaceholderModes(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{
			name: "unknown with mode",
			text: `[recipes.test.arguments.pkg]
default = "."

[recipes.test]
cmd = "echo {missing:shell}"
`,
			want: "unknown variable {missing}",
		},
		{
			name: "unsupported mode",
			text: `[recipes.test.arguments.pkg]
default = "."

[recipes.test]
cmd = "echo {pkg:json}"
`,
			want: `{pkg:json} uses unsupported placeholder mode "json"`,
		},
		{
			name: "shell mode outside shell field",
			text: `[recipes.test.arguments.pkg]
default = "."

[recipes.test]
workdir = "{pkg:shell}"
cmd = "true"
`,
			want: "{pkg:shell} mode is supported only in shell commands",
		},
		{
			name: "shell mode inside quotes",
			text: `[recipes.test.arguments.pkg]
default = "."

[recipes.test]
cmd = '''echo "{pkg:shell}"'''
`,
			want: "{pkg:shell} must not be inside quotes",
		},
		{
			name: "dq mode outside double quotes",
			text: `[recipes.test.arguments.pkg]
default = "."

[recipes.test]
cmd = "echo {pkg:dq}"
`,
			want: "{pkg:dq} must be inside double quotes",
		},
		{
			name: "variadic mode",
			text: `[recipes.test]
cmd = "echo {@:shell}"
`,
			want: "{@:shell} does not support placeholder modes",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diagnostics := documentDiagnostics(t.Context(), tc.text)
			assertOneDiagnostic(t, diagnostics, tc.want)
			if diagnostics[0].Severity != diagnosticSeverityError {
				t.Fatalf("severity = %d, want error", diagnostics[0].Severity)
			}
		})
	}
}

func TestDocumentDiagnosticsWarnUnsafeFreeArgumentPlaceholders(t *testing.T) {
	diagnostics := documentDiagnostics(t.Context(), `[recipes.deploy.arguments.host]
type = "string"

[recipes.deploy.arguments.config]
type = "rel_path"

[recipes.deploy.arguments.count]
type = "int"

[recipes.deploy.arguments.enabled]
type = "bool"

[recipes.deploy.arguments.ratio]
type = "float"

[recipes.deploy.arguments.duration]
type = "duration"

[recipes.deploy.arguments.timeout]
type = "duration:seconds"

[recipes.deploy.arguments.mode]
type = "string"
values = "@enum dev prod"

[recipes.deploy]
cmd = '''deploy {host} {config} {count} {enabled} {ratio} {duration} {timeout} {mode} "{host}" -H{host:shell} "{host:dq}" {host:raw}'''
`)
	if len(diagnostics) != 2 {
		t.Fatalf("diagnostics = %#v, want two warnings", diagnostics)
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity != diagnosticSeverityWarning {
			t.Fatalf("diagnostic = %#v, want warning severity", diagnostic)
		}
		if !strings.Contains(diagnostic.Message, "expands raw in shell") {
			t.Fatalf("message = %q, want unsafe placeholder warning", diagnostic.Message)
		}
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

func TestDocumentDiagnosticsAcceptIncludedRecipeReferenceAndPlaceholders(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "common.shadowtree.toml"), []byte(`
[vars]
INCLUDED_VAR = "value"

[recipes.common.arguments.target]
help = "Target."

[recipes.common]
cmd = "echo {INCLUDED_VAR} {target}"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	text := `include = ["./common.shadowtree.toml"]

[recipes.test]
cmd = "@common[target=api]"
post = ["echo {INCLUDED_VAR}"]
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

func TestDocumentDiagnosticsRejectCrossConfigOutsideSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeLSPTargetConfig(t, outside, "gen-schema")
	if err := os.Symlink(filepath.Join(outside, "webui"), filepath.Join(root, "webui")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	text := `[recipes.test]
cmd = "@webui:gen-schema"
`

	diagnostics := documentDiagnosticsWithOptions(t.Context(), text, diagnosticOptions{URI: fileURI(filepath.Join(root, ".shadowtree.toml"))})
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %#v, want one diagnostic", diagnostics)
	}
	if !strings.Contains(diagnostics[0].Message, "invalid recipe reference @webui:gen-schema") ||
		!strings.Contains(diagnostics[0].Message, "path is outside source") {
		t.Fatalf("message = %q, want outside source", diagnostics[0].Message)
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
