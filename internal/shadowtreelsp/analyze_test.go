package shadowtreelsp

import (
	"slices"
	"strings"
	"testing"
)

func TestCompletionsIncludeTopLevelTablesAfterOpenBracket(t *testing.T) {
	items := completionsAt("[", lspPosition{Line: 0, Character: 1})
	assertLabels(t, items, "env", "vars", "var_commands", "recipes")
}

func TestCompletionsIncludeRecipeSubtables(t *testing.T) {
	text := `[recipes.build]
cmd = ["go", "build"]
[recipes.build.
`
	items := completionsAt(text, lspPosition{Line: 2, Character: len("[recipes.build.")})
	assertLabels(t, items,
		"vars",
		"env",
		"arguments",
	)
}

func TestCompletionsIncludeKeysForCurrentTable(t *testing.T) {
	text := `[recipes.build]
`
	items := completionsAt(text, lspPosition{Line: 1, Character: 0})
	assertLabels(t, items, "cmd", "default_args", "sandboxed", "sync_out")
}

func TestCompletionsIncludePlaceholderVariables(t *testing.T) {
	text := `[vars]
PROJECT = "./cmd/shadowtree"

[recipes.build.vars]
BIN = "shadowtree"

[recipes.build.arguments.pkg]
type = "string"

[recipes.build]
cmd = '''go build {'''
`
	items := completionsAt(text, lspPosition{Line: 10, Character: len("cmd = '''go build {")})
	assertLabels(t, items, "{PROJECT}", "{BIN}", "{pkg}")
}

func TestCompletionsIncludeRecipeReferences(t *testing.T) {
	text := `[recipes.gen-swagger]
cmd = ["go", "generate", "./..."]

[recipes.vet]
cmd = ["go", "vet"]

[recipes.test]
pre = ["echo 123", "@gen"]
`
	items := completionsAt(text, lspPosition{Line: 7, Character: len(`pre = ["echo 123", "@`)})
	assertLabels(t, items, "@gen-swagger", "@vet")

	items = completionsAt(text, lspPosition{Line: 7, Character: len(`pre = ["echo 123", "@gen`)})
	assertLabels(t, items, "@gen-swagger")
}

func TestCompletionsFilterRecipeReferencePrefix(t *testing.T) {
	text := `[recipes.vet]
cmd = ["go", "vet"]

[recipes.check]
pre = ["@v"]
`
	items := completionsAt(text, lspPosition{Line: 4, Character: len(`pre = ["@v`)})
	assertLabels(t, items, "@vet")
}

func TestCompletionsIncludeStringRecipeReferences(t *testing.T) {
	text := `[recipes.test]
cmd = ["go", "test"]

[recipes.check]
cmd = "@t"
`
	items := completionsAt(text, lspPosition{Line: 4, Character: len(`cmd = "@t`)})
	assertLabels(t, items, "@test")
}

func TestCompletionsIgnoreRecipeReferencesOutsideRecipeTables(t *testing.T) {
	text := `[recipes.test]
cmd = ["go", "test"]

[vars]
cmd = "@t"
`
	items := completionsAt(text, lspPosition{Line: 4, Character: len(`cmd = "@t`)})
	if len(items) != 0 {
		t.Fatalf("items = %#v, want none", items)
	}
}

func TestRecipeReferenceTextEditReplacesNameAfterAt(t *testing.T) {
	text := `[recipes.gen-swagger]
cmd = ["go", "generate", "./..."]

[recipes.test]
pre = [["@gen"]]
`
	result := completionResult(text, lspPosition{Line: 4, Character: len(`pre = [["@gen`)})
	edit := completionTextEdit(t, result, "@gen-swagger")
	if edit["newText"] != "gen-swagger" {
		t.Fatalf("newText = %#v, want recipe name", edit["newText"])
	}
	assertEditRange(t, edit, len(`pre = [["@`), len(`pre = [["@gen`))
}

func TestCompletionsIncludeSupportedShellValues(t *testing.T) {
	items := completionsAt("shell = ", lspPosition{Line: 0, Character: len("shell = ")})
	assertLabels(t, items, "sh", "bash", "fish")
}

func TestQuotedValueTextEditAddsQuotesWhenMissing(t *testing.T) {
	edit := quotedValueTextEdit("shell = ", lspPosition{Line: 0, Character: len("shell = ")}, "sh")
	if edit["newText"] != `"sh"` {
		t.Fatalf("newText = %#v, want quoted shell", edit["newText"])
	}
}

func TestQuotedValueTextEditReplacesInsideExistingQuotes(t *testing.T) {
	edit := quotedValueTextEdit(`shell = ""`, lspPosition{Line: 0, Character: len(`shell = "`)}, "sh")
	if edit["newText"] != "sh" {
		t.Fatalf("newText = %#v, want bare shell inside quotes", edit["newText"])
	}
}

func TestKeyTextEditReplacesPrefixBeforeExistingAssignment(t *testing.T) {
	edit := keyTextEdit(`shel = "sh"`, lspPosition{Line: 0, Character: len("shel")}, "shell", `shell = "sh"`)
	if edit["newText"] != "shell" {
		t.Fatalf("newText = %#v, want key only", edit["newText"])
	}
	assertEditRange(t, edit, 0, 4)
}

func TestPlaceholderTextEditReplacesPrefixBeforeExistingBrace(t *testing.T) {
	edit := placeholderTextEdit(`cmd = "{projec}"`, lspPosition{Line: 0, Character: len(`cmd = "{projec`)}, "{project}")
	if edit["newText"] != "project" {
		t.Fatalf("newText = %#v, want placeholder name only", edit["newText"])
	}
	assertEditRange(t, edit, len(`cmd = "{`), len(`cmd = "{projec`))
}

func TestTableTextEditReplacesSegmentBeforeExistingBracket(t *testing.T) {
	edit := tableTextEdit(`[recipes.build.arg]`, lspPosition{Line: 0, Character: len(`[recipes.build.arg`)}, "arguments.")
	if edit["newText"] != "arguments." {
		t.Fatalf("newText = %#v, want table segment", edit["newText"])
	}
	assertEditRange(t, edit, len(`[recipes.build.`), len(`[recipes.build.arg`))
}

func TestCompletionTextEditUsesUTF16Offsets(t *testing.T) {
	line := `[recipes.café.arg]`
	result := completionResult(line, lspPosition{
		Line:      0,
		Character: byteToUTF16Offset(line, len(`[recipes.café.arg`)),
	})
	edit := completionTextEdit(t, result, "arguments")
	assertEditRange(t, edit,
		byteToUTF16Offset(line, len(`[recipes.café.`)),
		byteToUTF16Offset(line, len(`[recipes.café.arg`)),
	)
}

func TestSemanticTokensIncludeVariablesAndPlaceholders(t *testing.T) {
	text := `[vars]
PROJECT = "./cmd/shadowtree"

[recipes.build]
cmd = '''go build {PROJECT}'''
`
	tokens := decodeSemanticTokens(semanticTokens(text))
	if !hasSemanticToken(tokens, 1, 0, len("PROJECT"), semanticTokenVariable) {
		t.Fatalf("missing variable definition token in %#v", tokens)
	}
	if !hasSemanticToken(tokens, 4, len("cmd = '''go build "), len("{PROJECT}"), semanticTokenVariable) {
		t.Fatalf("missing placeholder token in %#v", tokens)
	}
}

func TestSemanticTokensHighlightRecipeReferences(t *testing.T) {
	text := `[recipes.test]
pre = ["echo 123", "@{target}"]
`
	tokens := decodeSemanticTokens(semanticTokens(text))
	assertSemanticToken(t, tokens, 1, len(`pre = ["echo 123", "`), len("@{target}"), semanticTokenRecipeReference)
	if hasSemanticToken(tokens, 1, len(`pre = ["echo 123", "@`), len("{target}"), semanticTokenVariable) {
		t.Fatalf("@{target} was also highlighted as a placeholder in %#v", tokens)
	}
}

func TestSemanticTokensHighlightCheckRecipeReferences(t *testing.T) {
	text := `[recipes.check]
pre = ["@vet"]
cmd = ["@test"]
default_args = ["./..."]
`
	tokens := decodeSemanticTokens(semanticTokens(text))
	assertSemanticToken(t, tokens, 1, len(`pre = ["`), len("@vet"), semanticTokenRecipeReference)
	assertSemanticToken(t, tokens, 2, len(`cmd = ["`), len("@test"), semanticTokenRecipeReference)
}

func TestSemanticTokensHighlightScalarRecipeReferences(t *testing.T) {
	text := `[recipes.check]
cmd = "@test"

[recipes.check.arguments.target]
values = "@targets"
`
	tokens := decodeSemanticTokens(semanticTokens(text))
	assertSemanticToken(t, tokens, 1, len(`cmd = "`), len("@test"), semanticTokenRecipeReference)
	assertSemanticToken(t, tokens, 4, len(`values = "`), len("@targets"), semanticTokenRecipeReference)
}

func TestSemanticTokensIgnoreShellStringStartingWithAt(t *testing.T) {
	text := `[recipes.check]
pre = ["@echo hi"]
cmd = ["go", "test"]
`
	tokens := decodeSemanticTokens(semanticTokens(text))
	if hasSemanticToken(tokens, 1, len(`pre = ["`), len("@echo hi"), semanticTokenRecipeReference) {
		t.Fatalf("@echo hi was highlighted as a recipe reference in %#v", tokens)
	}
}

func TestSemanticTokensHighlightPlaceholderAfterAtOutsideRecipeReference(t *testing.T) {
	text := `[vars]
URL = "@{host}"
`
	tokens := decodeSemanticTokens(semanticTokens(text))
	assertSemanticToken(t, tokens, 1, len(`URL = "@`), len("{host}"), semanticTokenVariable)
}

func TestSemanticTokensIgnoreRecipeReferenceKeysOutsideRecipeTables(t *testing.T) {
	text := `[vars]
cmd = "@missing"
`
	tokens := decodeSemanticTokens(semanticTokens(text))
	if hasSemanticToken(tokens, 1, len(`cmd = "`), len("@missing"), semanticTokenRecipeReference) {
		t.Fatalf("@missing was highlighted as a recipe reference in %#v", tokens)
	}
}

func TestSemanticTokensUseUTF16Offsets(t *testing.T) {
	text := `[recipes.build]
cmd = '''echo café {PROJECT}'''
`
	tokens := decodeSemanticTokens(semanticTokens(text))
	line := textLine(text, 1)
	assertSemanticToken(t, tokens, 1,
		byteToUTF16Offset(line, len("cmd = '''echo café ")),
		len("{PROJECT}"),
		semanticTokenVariable,
	)
}

func TestSemanticTokensHighlightShellScriptBody(t *testing.T) {
	text := `shell = "sh"

[recipes.install]
cmd = '''
set -eu
destdir=${DESTDIR:-}
install -d "$destdir/bin" # create bin
if [ -d "$destdir" ]; then
	echo ok
fi
'''
`
	tokens := decodeSemanticTokens(semanticTokens(text))
	assertSemanticToken(t, tokens, 4, 0, len("set"), semanticTokenFunction)
	assertSemanticToken(t, tokens, 4, len("set "), len("-eu"), semanticTokenParameter)
	assertSemanticToken(t, tokens, 5, len("destdir="), len("${DESTDIR:-}"), semanticTokenVariable)
	assertSemanticToken(t, tokens, 6, 0, len("install"), semanticTokenFunction)
	assertSemanticToken(t, tokens, 6, len("install "), len("-d"), semanticTokenParameter)
	assertSemanticToken(t, tokens, 6, strings.Index(textLine(text, 6), "$destdir"), len("$destdir"), semanticTokenVariable)
	assertSemanticToken(t, tokens, 6, strings.Index(textLine(text, 6), "#"), len("# create bin"), semanticTokenComment)
	assertSemanticToken(t, tokens, 7, 0, len("if"), semanticTokenKeyword)
	assertSemanticToken(t, tokens, 7, strings.Index(textLine(text, 7), "$destdir"), len("$destdir"), semanticTokenVariable)
	assertSemanticToken(t, tokens, 7, strings.Index(textLine(text, 7), ";"), 1, semanticTokenOperator)
	assertSemanticToken(t, tokens, 7, strings.Index(textLine(text, 7), "then"), len("then"), semanticTokenKeyword)
	assertSemanticToken(t, tokens, 9, 0, len("fi"), semanticTokenKeyword)
}

func TestSemanticTokensUseRecipeFishShell(t *testing.T) {
	text := `[recipes.complete]
shell = "fish"
values = '''
set -l out $argv
if test -n "$out"
	echo $out
end
if status is-interactive
	echo interactive
end
function echo-error -a message
	echo "$message" >&2
end
set -x _2api_CODEX_KEY $2api_CODEX_KEY
'''
`
	tokens := decodeSemanticTokens(semanticTokens(text))
	assertSemanticToken(t, tokens, 3, 0, len("set"), semanticTokenKeyword)
	assertSemanticToken(t, tokens, 3, len("set "), len("-l"), semanticTokenParameter)
	assertSemanticToken(t, tokens, 3, len("set -l out "), len("$argv"), semanticTokenVariable)
	assertSemanticToken(t, tokens, 4, 0, len("if"), semanticTokenKeyword)
	assertSemanticToken(t, tokens, 4, len("if "), len("test"), semanticTokenFunction)
	assertSemanticToken(t, tokens, 4, strings.Index(textLine(text, 4), "$out"), len("$out"), semanticTokenVariable)
	assertSemanticToken(t, tokens, 6, 0, len("end"), semanticTokenKeyword)
	assertSemanticToken(t, tokens, 7, 0, len("if"), semanticTokenKeyword)
	assertSemanticToken(t, tokens, 7, len("if "), len("status"), semanticTokenFunction)
	assertSemanticToken(t, tokens, 7, len("if status "), len("is-interactive"), semanticTokenParameter)
	assertSemanticToken(t, tokens, 10, 0, len("function"), semanticTokenKeyword)
	assertSemanticToken(t, tokens, 10, len("function "), len("echo-error"), semanticTokenFunction)
	assertSemanticToken(t, tokens, 10, len("function echo-error "), len("-a"), semanticTokenParameter)
	assertSemanticToken(t, tokens, 10, len("function echo-error -a "), len("message"), semanticTokenVariable)
	assertSemanticToken(t, tokens, 11, strings.Index(textLine(text, 11), "$message"), len("$message"), semanticTokenVariable)
	assertSemanticToken(t, tokens, 13, len("set -x "), len("_2api_CODEX_KEY"), semanticTokenVariable)
	assertSemanticToken(t, tokens, 13, len("set -x _2api_CODEX_KEY "), len("$2api_CODEX_KEY"), semanticTokenVariable)
}

func assertEditRange(t *testing.T, edit map[string]any, start, end int) {
	t.Helper()
	editRange, ok := edit["range"].(map[string]any)
	if !ok {
		t.Fatalf("range has type %T", edit["range"])
	}
	startPos, ok := editRange["start"].(lspPosition)
	if !ok {
		t.Fatalf("start has type %T", editRange["start"])
	}
	endPos, ok := editRange["end"].(lspPosition)
	if !ok {
		t.Fatalf("end has type %T", editRange["end"])
	}
	if startPos.Character != start || endPos.Character != end {
		t.Fatalf("range = %d..%d, want %d..%d", startPos.Character, endPos.Character, start, end)
	}
}

func completionTextEdit(t *testing.T, result map[string]any, label string) map[string]any {
	t.Helper()
	items, ok := result["items"].([]map[string]any)
	if !ok {
		t.Fatalf("items has type %T", result["items"])
	}
	for _, item := range items {
		if item["label"] != label {
			continue
		}
		edit, ok := item["textEdit"].(map[string]any)
		if !ok {
			t.Fatalf("textEdit has type %T", item["textEdit"])
		}
		return edit
	}
	t.Fatalf("missing completion %q in %#v", label, items)
	return nil
}

func assertLabels(t *testing.T, items []completion, labels ...string) {
	t.Helper()
	var got []string
	for _, item := range items {
		got = append(got, item.Label)
	}
	for _, label := range labels {
		if !slices.Contains(got, label) {
			t.Fatalf("missing label %q in %#v", label, got)
		}
	}
}

func decodeSemanticTokens(data []uint32) []semanticToken {
	var tokens []semanticToken
	line, start := 0, 0
	for i := 0; i+4 < len(data); i += 5 {
		line += int(data[i])
		if data[i] == 0 {
			start += int(data[i+1])
		} else {
			start = int(data[i+1])
		}
		tokens = append(tokens, semanticToken{
			Line:   line,
			Start:  start,
			Length: int(data[i+2]),
			Type:   int(data[i+3]),
		})
	}
	return tokens
}

func assertSemanticToken(t *testing.T, tokens []semanticToken, line, start, length, tokenType int) {
	t.Helper()
	if !hasSemanticToken(tokens, line, start, length, tokenType) {
		t.Fatalf("missing semantic token line=%d start=%d length=%d type=%d in %#v", line, start, length, tokenType, tokens)
	}
}

func hasSemanticToken(tokens []semanticToken, line, start, length, tokenType int) bool {
	for _, token := range tokens {
		if token.Line == line && token.Start == start && token.Length == length && token.Type == tokenType {
			return true
		}
	}
	return false
}

func textLine(text string, line int) string {
	lines := strings.Split(text, "\n")
	return lineAt(lines, line)
}
