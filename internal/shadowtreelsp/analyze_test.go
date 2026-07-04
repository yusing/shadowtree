package shadowtreelsp

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCompletionsIncludeTopLevelTablesAfterOpenBracket(t *testing.T) {
	items := completionsAt(t.Context(), "[", lspPosition{Line: 0, Character: 1})
	assertLabels(t, items, "env", "vars", "var_commands", "recipes")
}

func TestCompletionsIncludeRecipeSubtables(t *testing.T) {
	text := `[recipes.build]
cmd = ["go", "build"]
[recipes.build.
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 2, Character: len("[recipes.build.")})
	assertLabels(t, items,
		"vars",
		"env",
		"arguments",
	)
}

func TestCompletionsIncludeKeysForCurrentTable(t *testing.T) {
	text := `[recipes.build]
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 1, Character: 0})
	assertLabels(t, items, "cmd", "default_args", "sandboxed", "sync_out")
}

func TestCompletionsIncludePlaceholderVariables(t *testing.T) {
	text := `[vars]
PROJECT = "./cmd/shadowtree"

[recipes.build.vars]
BIN = "shadowtree"

[recipes.build.arguments.pkg]
help = "Package to build."
type = "string"

[recipes.build]
cmd = '''go build {'''
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 11, Character: len("cmd = '''go build {")})
	assertLabels(t, items, "{PROJECT}", "{BIN}", "{pkg}")
	assertCompletionDetail(t, items, "{pkg}", "Package to build.")

	result := completionResult(t.Context(), text, lspPosition{Line: 11, Character: len("cmd = '''go build {")})
	item := completionItem(t, result, "{pkg}")
	if item["detail"] != "Package to build." {
		t.Fatalf("detail = %#v, want argument help", item["detail"])
	}
	edit := completionTextEdit(t, result, "{pkg}")
	if edit["newText"] != "pkg}" {
		t.Fatalf("newText = %#v, want placeholder suffix", edit["newText"])
	}
}

func TestCompletionsIncludeVariadicArgsPlaceholder(t *testing.T) {
	text := `[recipes.test]
cmd = ["go", "test", "{@`
	items := completionsAt(t.Context(), text, lspPosition{Line: 1, Character: len(`cmd = ["go", "test", "{@`)})
	assertLabels(t, items, "@")

	result := completionResult(t.Context(), text, lspPosition{Line: 1, Character: len(`cmd = ["go", "test", "{@`)})
	edit := completionTextEdit(t, result, "@")
	if edit["newText"] != "@}" {
		t.Fatalf("newText = %#v, want variadic placeholder suffix", edit["newText"])
	}
}

func TestCompletionsExcludeVariadicArgsPlaceholderInSyncOut(t *testing.T) {
	text := `[recipes.test]
sync_out = ["{`
	items := completionsAt(t.Context(), text, lspPosition{Line: 1, Character: len(`sync_out = ["{`)})
	for _, item := range items {
		if item.Label == "@" {
			t.Fatalf("unexpected variadic placeholder completion in %#v", items)
		}
	}
}

func TestCompletionsIncludeRecipeReferences(t *testing.T) {
	text := `[recipes.gen-swagger]
help = "Generate Swagger docs."
cmd = ["go", "generate", "./..."]

[recipes.vet]
cmd = ["go", "vet"]

[recipes.test]
pre = ["echo 123", "@gen"]
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 8, Character: len(`pre = ["echo 123", "@`)})
	assertLabels(t, items, "@gen-swagger", "@vet")
	assertCompletionDetail(t, items, "@gen-swagger", "Generate Swagger docs.")

	items = completionsAt(t.Context(), text, lspPosition{Line: 8, Character: len(`pre = ["echo 123", "@gen`)})
	assertLabels(t, items, "@gen-swagger")
}

func TestRecipeReferenceCompletionUsesRecipeHelpAndTextEdit(t *testing.T) {
	text := `[recipes.gen-swagger]
help = "Generate Swagger docs."
cmd = ["go", "generate", "./..."]

[recipes.test]
pre = [["@gen"]]
`
	result := completionResult(t.Context(), text, lspPosition{Line: 5, Character: len(`pre = [["@gen`)})
	item := completionItem(t, result, "@gen-swagger")
	if item["detail"] != "Generate Swagger docs." {
		t.Fatalf("detail = %#v, want recipe help", item["detail"])
	}
	edit, ok := item["textEdit"].(map[string]any)
	if !ok {
		t.Fatalf("textEdit has type %T", item["textEdit"])
	}
	if edit["newText"] != "gen-swagger" {
		t.Fatalf("newText = %#v, want recipe name", edit["newText"])
	}
}

func TestCompletionsFilterRecipeReferencePrefix(t *testing.T) {
	text := `[recipes.vet]
cmd = ["go", "vet"]

[recipes.check]
pre = ["@v"]
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 4, Character: len(`pre = ["@v`)})
	assertLabels(t, items, "@vet")
}

func TestCompletionsIncludeStringRecipeReferences(t *testing.T) {
	text := `[recipes.test]
cmd = ["go", "test"]

[recipes.check]
cmd = "@t"
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 4, Character: len(`cmd = "@t`)})
	assertLabels(t, items, "@test")
}

func TestCompletionsIncludeEnumInArgumentValuesReferences(t *testing.T) {
	text := `[recipes.test.arguments.target]
values = "@"

[recipes.test]
cmd = ["go", "test"]
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 1, Character: len(`values = "@`)})
	assertLabels(t, items, "@enum", "@glob", "@lines", "@recipes", "@test", "@vars")
	assertCompletionDetail(t, items, "@enum", "Static argument values (builtin)")
}

func TestCompletionsIncludeEnumInScriptArgumentValuesReferences(t *testing.T) {
	text := `[recipes.test.arguments.target]
values = '''
@e
'''

[recipes.test]
cmd = ["go", "test"]
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 2, Character: len(`@e`)})
	assertLabels(t, items, "@enum")
	assertCompletionDetail(t, items, "@enum", "Static argument values (builtin)")
}

func TestCompletionsExcludeEnumOutsideArgumentValuesReferences(t *testing.T) {
	text := `[recipes.test]
cmd = "@"
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 1, Character: len(`cmd = "@`)})
	assertLabels(t, items, "@test")
}

func TestCompletionsExcludeGoBuiltinsWhenConfigOmitsProfile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	text := `[recipes.assets]
cmd = ["npm", "test"]

[recipes.check]
cmd = "@f"
`
	items := completionsAtWithOptions(
		t.Context(),
		text,
		lspPosition{Line: 4, Character: len(`cmd = "@f`)},
		completionOptions{ConfigPath: filepath.Join(root, ".shadowtree.toml")},
	)
	for _, item := range items {
		if item.Label == "@fmt" {
			t.Fatalf("items include Go builtin @fmt: %#v", items)
		}
	}
}

func TestCompletionsIncludeGoBuiltinsWhenConfigSetsProfile(t *testing.T) {
	root := t.TempDir()
	text := `profile = "go"

[recipes.check]
cmd = "@f"
`
	items := completionsAtWithOptions(
		t.Context(),
		text,
		lspPosition{Line: 3, Character: len(`cmd = "@f`)},
		completionOptions{ConfigPath: filepath.Join(root, ".shadowtree.toml")},
	)
	assertLabels(t, items, "@fmt")
}

func TestCompletionsIncludeNodeProfileValue(t *testing.T) {
	text := `profile = "`
	items := completionsAt(t.Context(), text, lspPosition{Line: 0, Character: len(text)})

	assertLabels(t, items, "go", "node")
}

func TestCompletionsIncludeNodeBuiltinsWhenConfigSetsProfile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"scripts":{"test":"vitest"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	text := `profile = "node"

[recipes.check]
cmd = "@i"
`
	items := completionsAtWithOptions(
		t.Context(),
		text,
		lspPosition{Line: 3, Character: len(`cmd = "@i`)},
		completionOptions{ConfigPath: filepath.Join(root, ".shadowtree.toml")},
	)
	assertLabels(t, items, "@install")
}

func TestCompletionsIncludeLocalRecipeReferencesWhenTOMLIsIncomplete(t *testing.T) {
	text := `[recipes.build]
cmd = ["go", "build"]

[recipes.check]
cmd = "@b"

[recipes.
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 4, Character: len(`cmd = "@b`)})
	assertLabels(t, items, "@build")
}

func TestCompletionsIncludeRecipeReferenceArguments(t *testing.T) {
	text := `[recipes.build.arguments.component]
type = "string"

[recipes.build.arguments.mode]
type = "bool"

[recipes.build]
cmd = ["go", "build"]

[recipes.test]
pre = ["@build["]
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 10, Character: len(`pre = ["@build[`)})
	assertLabels(t, items, "component=", "mode=")
}

func TestCompletionsIncludeRecipeReferenceArgumentValues(t *testing.T) {
	text := `[recipes.build.arguments.mode]
type = "bool"

[recipes.build]
cmd = ["go", "build"]

[recipes.test]
pre = ["@build[mode="]
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 7, Character: len(`pre = ["@build[mode=`)})
	assertLabels(t, items, "true", "false")
}

func TestCompletionsIncludeScriptRecipeReferences(t *testing.T) {
	text := `[recipes.gen-swagger]
help = "Generate Swagger docs."
cmd = ["go", "generate", "./..."]

[recipes.vet]
cmd = ["go", "vet"]

[recipes.test]
cmd = '''
if [ -f schema.json ]; then
  @gen
fi
'''
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 10, Character: len(`  @gen`)})
	assertLabels(t, items, "@gen-swagger")
	assertCompletionDetail(t, items, "@gen-swagger", "Generate Swagger docs.")
}

func TestCompletionsIncludeShellPreludeRecipeReferences(t *testing.T) {
	text := `shell_prelude = '''
@g
'''

[recipes.gen-swagger]
help = "Generate Swagger docs."
cmd = ["go", "generate", "./..."]

[recipes.test]
cmd = ["go", "test"]
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 1, Character: len(`@g`)})
	assertLabels(t, items, "@gen-swagger")
	assertCompletionDetail(t, items, "@gen-swagger", "Generate Swagger docs.")
}

func TestCompletionsIncludeRecipeShellPreludeRecipeReferences(t *testing.T) {
	text := `[recipes.gen-swagger]
help = "Generate Swagger docs."
cmd = ["go", "generate", "./..."]

[recipes.test]
shell_prelude = '''
@g
'''
cmd = ["go", "test"]
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 6, Character: len(`@g`)})
	assertLabels(t, items, "@gen-swagger")
	assertCompletionDetail(t, items, "@gen-swagger", "Generate Swagger docs.")
}

func TestCompletionsIncludeScriptRecipeReferenceArguments(t *testing.T) {
	text := `[recipes.build.arguments.component]
type = "string"

[recipes.build.arguments.mode]
type = "bool"

[recipes.build]
cmd = ["go", "build"]

[recipes.test]
cmd = '''
@build[
'''
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 11, Character: len(`@build[`)})
	assertLabels(t, items, "component=", "mode=")

	result := completionResult(t.Context(), text, lspPosition{Line: 11, Character: len(`@build[`)})
	edit := completionTextEdit(t, result, "component=")
	if edit["newText"] != "build[component=" {
		t.Fatalf("newText = %#v, want grouped recipe argument", edit["newText"])
	}
	assertEditRange(t, edit, len(`@`), len(`@build[`))
}

func TestCompletionsIncludeScriptRecipeReferenceArgumentValues(t *testing.T) {
	text := `[recipes.build.arguments.mode]
type = "bool"

[recipes.build]
cmd = ["go", "build"]

[recipes.test]
cmd = '''
@build[mode=
'''
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 8, Character: len(`@build[mode=`)})
	assertLabels(t, items, "true", "false")

	result := completionResult(t.Context(), text, lspPosition{Line: 8, Character: len(`@build[mode=`)})
	edit := completionTextEdit(t, result, "true")
	if edit["newText"] != "build[mode=true" {
		t.Fatalf("newText = %#v, want grouped recipe argument value", edit["newText"])
	}
	assertEditRange(t, edit, len(`@`), len(`@build[mode=`))
}

func TestCompletionsIncludeScriptRecipeReferenceSpacedArguments(t *testing.T) {
	text := `[recipes.build.arguments.component]
type = "string"

[recipes.build.arguments.mode]
type = "bool"

[recipes.build]
cmd = ["go", "build"]

[recipes.test]
cmd = '''
@build m
'''
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 11, Character: len(`@build m`)})
	assertLabels(t, items, "mode=")
}

func TestCompletionsIncludeScriptRecipeReferenceSpacedArgumentValues(t *testing.T) {
	text := `[recipes.build.arguments.mode]
type = "bool"

[recipes.build]
cmd = ["go", "build"]

[recipes.test]
cmd = '''
@build mode=
'''
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 8, Character: len(`@build mode=`)})
	assertLabels(t, items, "true", "false")
}

func TestCompletionsIncludeValuesScriptRecipeReferenceDynamicArgumentValues(t *testing.T) {
	text := `[recipes.minify.arguments.component]
type = "string"
values = "printf '%s\\n' godoxy agent socket-proxy cli"

[recipes.minify]
cmd = ["true"]

[recipes.test.arguments.target]
type = "string"
values = '''
@minify component="
'''
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 10, Character: len(`@minify component="`)})
	assertLabels(t, items, "godoxy", "agent", "socket-proxy", "cli")
}

func TestCompletionsIncludeValuesScriptRecipeReferenceDynamicBracketArgumentValues(t *testing.T) {
	text := `[recipes.minify.arguments.component]
type = "string"
values = "printf '%s\\n' godoxy agent socket-proxy cli"

[recipes.minify]
cmd = ["true"]

[recipes.test.arguments.target]
type = "string"
values = '''
@minify[component="
'''
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 10, Character: len(`@minify[component="`)})
	assertLabels(t, items, "godoxy", "agent", "socket-proxy", "cli")
}

func TestCompletionsIncludeShellPreludeRecipeReferenceArguments(t *testing.T) {
	text := `shell_prelude = '''
@build[
'''

[recipes.build.arguments.component]
type = "string"

[recipes.build.arguments.mode]
type = "bool"

[recipes.build]
cmd = ["go", "build"]
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 1, Character: len(`@build[`)})
	assertLabels(t, items, "component=", "mode=")
}

func TestCompletionsIncludeShellPreludeRecipeReferenceArgumentValues(t *testing.T) {
	text := `shell_prelude = '''
@build mode=
'''

[recipes.build.arguments.mode]
type = "bool"

[recipes.build]
cmd = ["go", "build"]
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 1, Character: len(`@build mode=`)})
	assertLabels(t, items, "true", "false")
}

func TestCompletionsIncludePrePostScriptRecipeReferenceArguments(t *testing.T) {
	text := `[recipes.build.arguments.component]
type = "string"

[recipes.build.arguments.mode]
type = "bool"

[recipes.build]
cmd = ["go", "build"]

[recipes.test]
pre = ["@build["]
post = ["@build mode="]
cmd = ["true"]
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 10, Character: len(`pre = ["@build[`)})
	assertLabels(t, items, "component=", "mode=")

	items = completionsAt(t.Context(), text, lspPosition{Line: 11, Character: len(`post = ["@build mode=`)})
	assertLabels(t, items, "true", "false")
}

func TestCompletionsIgnoreScriptRecipeReferenceArguments(t *testing.T) {
	text := `[recipes.gen]
cmd = ["true"]

[recipes.test]
cmd = '''
echo @g
'''
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 5, Character: len(`echo @g`)})
	if len(items) != 0 {
		t.Fatalf("items = %#v, want none", items)
	}
}

func TestCompletionsIncludeCrossConfigDirectories(t *testing.T) {
	root := t.TempDir()
	writeLSPTargetConfig(t, root, "gen-schema")
	text := `[recipes.test]
cmd = ["@web"]
`
	items := completionsAtWithOptions(
		t.Context(),
		text,
		lspPosition{Line: 1, Character: len(`cmd = ["@web`)},
		completionOptions{ConfigPath: filepath.Join(root, ".shadowtree.toml")},
	)
	assertLabels(t, items, "@webui:")
}

func TestCompletionsIncludeCrossConfigRecipeReferences(t *testing.T) {
	root := t.TempDir()
	writeLSPTargetConfig(t, root, "gen-schema")
	text := `[recipes.test]
cmd = ["@webui:g"]
`
	items := completionsAtWithOptions(
		t.Context(),
		text,
		lspPosition{Line: 1, Character: len(`cmd = ["@webui:g`)},
		completionOptions{ConfigPath: filepath.Join(root, ".shadowtree.toml")},
	)
	assertLabels(t, items, "@webui:gen-schema")
	assertCompletionDetail(t, items, "@webui:gen-schema", "Target recipe help.")
}

func TestCompletionsIncludeCrossConfigRecipeReferenceArguments(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "webui")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".shadowtree.toml"), []byte(`
[recipes.gen-schema.arguments.mode]
type = "bool"

[recipes.gen-schema]
cmd = ["true"]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	text := `[recipes.test]
cmd = ["@webui:gen-schema["]
`
	items := completionsAtWithOptions(
		t.Context(),
		text,
		lspPosition{Line: 1, Character: len(`cmd = ["@webui:gen-schema[`)},
		completionOptions{ConfigPath: filepath.Join(root, ".shadowtree.toml")},
	)
	assertLabels(t, items, "mode=")

	text = `[recipes.test]
cmd = ["@webui:gen-schema[mode="]
`
	items = completionsAtWithOptions(
		t.Context(),
		text,
		lspPosition{Line: 1, Character: len(`cmd = ["@webui:gen-schema[mode=`)},
		completionOptions{ConfigPath: filepath.Join(root, ".shadowtree.toml")},
	)
	assertLabels(t, items, "true", "false")
}

func TestCompletionsIncludeScriptCrossConfigRecipeReferences(t *testing.T) {
	root := t.TempDir()
	writeLSPTargetConfig(t, root, "gen-schema")
	text := `[recipes.test]
cmd = '''
@webui:g
'''
`
	items := completionsAtWithOptions(
		t.Context(),
		text,
		lspPosition{Line: 2, Character: len(`@webui:g`)},
		completionOptions{ConfigPath: filepath.Join(root, ".shadowtree.toml")},
	)
	assertLabels(t, items, "@webui:gen-schema")
	assertCompletionDetail(t, items, "@webui:gen-schema", "Target recipe help.")
}

func TestRecipeReferenceArgumentTextEditReplacesActiveFragment(t *testing.T) {
	text := `[recipes.build.arguments.mode]
type = "bool"

[recipes.build]
cmd = ["go", "build"]

[recipes.test]
pre = ["@build[m"]
`
	result := completionResult(t.Context(), text, lspPosition{Line: 7, Character: len(`pre = ["@build[m`)})
	edit := completionTextEdit(t, result, "mode=")
	if edit["newText"] != "build[mode=" {
		t.Fatalf("newText = %#v, want grouped recipe argument", edit["newText"])
	}
	assertEditRange(t, edit, len(`pre = ["@`), len(`pre = ["@build[m`))
}

func TestCompletionsIgnoreRecipeReferencesOutsideRecipeTables(t *testing.T) {
	text := `[recipes.test]
cmd = ["go", "test"]

[vars]
cmd = "@t"
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 4, Character: len(`cmd = "@t`)})
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
	result := completionResult(t.Context(), text, lspPosition{Line: 4, Character: len(`pre = [["@gen`)})
	edit := completionTextEdit(t, result, "@gen-swagger")
	if edit["newText"] != "gen-swagger" {
		t.Fatalf("newText = %#v, want recipe name", edit["newText"])
	}
	assertEditRange(t, edit, len(`pre = [["@`), len(`pre = [["@gen`))
}

func TestCompletionsIncludeSupportedShellValues(t *testing.T) {
	items := completionsAt(t.Context(), "shell = ", lspPosition{Line: 0, Character: len("shell = ")})
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
	result := completionResult(t.Context(), line, lspPosition{
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

func TestSemanticTokensIncludeVariadicArgsPlaceholder(t *testing.T) {
	text := `[recipes.test]
cmd = ["go", "test", "{@}"]
`
	tokens := decodeSemanticTokens(semanticTokens(text))
	if !hasSemanticToken(tokens, 1, len(`cmd = ["go", "test", "`), len("{@}"), semanticTokenVariable) {
		t.Fatalf("missing variadic placeholder token in %#v", tokens)
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
	assertSemanticToken(t, tokens, 1, len(`pre = ["`), len("@vet"), semanticTokenFunction)
	assertSemanticToken(t, tokens, 2, len(`cmd = ["`), len("@test"), semanticTokenFunction)
}

func TestSemanticTokensHighlightScalarRecipeReferences(t *testing.T) {
	text := `[recipes.check]
cmd = "@test"

[recipes.check.arguments.target]
values = "@targets"
`
	tokens := decodeSemanticTokens(semanticTokens(text))
	assertSemanticToken(t, tokens, 1, len(`cmd = "`), len("@test"), semanticTokenFunction)
	assertSemanticToken(t, tokens, 4, len(`values = "`), len("@targets"), semanticTokenFunction)
}

func TestSemanticTokensHighlightRecipeReferenceArguments(t *testing.T) {
	text := `[recipes.test]
pre = ["@build[project=internal/, binary=abc]"]
`
	tokens := decodeSemanticTokens(semanticTokens(text))
	linePrefix := len(`pre = ["`)
	assertSemanticToken(t, tokens, 1, linePrefix, len("@build"), semanticTokenFunction)
	assertSemanticToken(t, tokens, 1, linePrefix+len("@build["), len("project"), semanticTokenParameter)
	assertSemanticToken(t, tokens, 1, linePrefix+len("@build[project="), len("internal/"), semanticTokenString)
	assertSemanticToken(t, tokens, 1, linePrefix+len("@build[project=internal/, "), len("binary"), semanticTokenParameter)
	assertSemanticToken(t, tokens, 1, linePrefix+len("@build[project=internal/, binary="), len("abc"), semanticTokenString)
}

func TestSemanticTokensHighlightScriptRecipeReferences(t *testing.T) {
	text := `[recipes.test]
cmd = '''
if [ -f schema.json ]; then
  @build[project=internal/,binary=abc]
  @build project=internal/ binary=abc
fi
'''
`
	tokens := decodeSemanticTokens(semanticTokens(text))
	linePrefix := len("  ")
	assertSemanticToken(t, tokens, 3, linePrefix, len("@build"), semanticTokenFunction)
	assertSemanticToken(t, tokens, 3, linePrefix+len("@build["), len("project"), semanticTokenParameter)
	assertSemanticToken(t, tokens, 3, linePrefix+len("@build[project="), len("internal/"), semanticTokenString)
	assertSemanticToken(t, tokens, 3, linePrefix+len("@build[project=internal/,"), len("binary"), semanticTokenParameter)
	assertSemanticToken(t, tokens, 3, linePrefix+len("@build[project=internal/,binary="), len("abc"), semanticTokenString)

	refStart := linePrefix
	refEnd := linePrefix + len("@build[project=internal/,binary=abc]")
	for _, token := range tokens {
		if token.Line != 3 || token.Start >= refEnd || token.Start+token.Length <= refStart {
			continue
		}
		switch {
		case token.Start == linePrefix && token.Length == len("@build") && token.Type == semanticTokenFunction:
		case token.Start == linePrefix+len("@build[") && token.Length == len("project") && token.Type == semanticTokenParameter:
		case token.Start == linePrefix+len("@build[project=") && token.Length == len("internal/") && token.Type == semanticTokenString:
		case token.Start == linePrefix+len("@build[project=internal/,") && token.Length == len("binary") && token.Type == semanticTokenParameter:
		case token.Start == linePrefix+len("@build[project=internal/,binary=") && token.Length == len("abc") && token.Type == semanticTokenString:
		default:
			t.Fatalf("unexpected token overlapping script recipe reference: %#v in %#v", token, tokens)
		}
	}

	assertSemanticToken(t, tokens, 4, linePrefix, len("@build"), semanticTokenFunction)
	assertSemanticToken(t, tokens, 4, linePrefix+len("@build "), len("project"), semanticTokenParameter)
	assertSemanticToken(t, tokens, 4, linePrefix+len("@build project="), len("internal/"), semanticTokenString)
	assertSemanticToken(t, tokens, 4, linePrefix+len("@build project=internal/ "), len("binary"), semanticTokenParameter)
	assertSemanticToken(t, tokens, 4, linePrefix+len("@build project=internal/ binary="), len("abc"), semanticTokenString)

	spacedRefStart := linePrefix
	spacedRefEnd := linePrefix + len("@build project=internal/ binary=abc")
	for _, token := range tokens {
		if token.Line != 4 || token.Start >= spacedRefEnd || token.Start+token.Length <= spacedRefStart {
			continue
		}
		switch {
		case token.Start == linePrefix && token.Length == len("@build") && token.Type == semanticTokenFunction:
		case token.Start == linePrefix+len("@build ") && token.Length == len("project") && token.Type == semanticTokenParameter:
		case token.Start == linePrefix+len("@build project=") && token.Length == len("internal/") && token.Type == semanticTokenString:
		case token.Start == linePrefix+len("@build project=internal/ ") && token.Length == len("binary") && token.Type == semanticTokenParameter:
		case token.Start == linePrefix+len("@build project=internal/ binary=") && token.Length == len("abc") && token.Type == semanticTokenString:
		default:
			t.Fatalf("unexpected token overlapping spaced script recipe reference: %#v in %#v", token, tokens)
		}
	}
}

func TestSemanticTokensIgnoreScriptRecipeReferenceText(t *testing.T) {
	text := `[recipes.test]
cmd = '''
FOO="@missing"
echo "@also_missing"
# @comment
'''
`
	tokens := decodeSemanticTokens(semanticTokens(text))
	if hasSemanticToken(tokens, 2, len(`FOO="`), len("@missing"), semanticTokenFunction) {
		t.Fatalf("assignment was highlighted as recipe reference in %#v", tokens)
	}
	if hasSemanticToken(tokens, 3, len(`echo "`), len("@also_missing"), semanticTokenFunction) {
		t.Fatalf("quoted text was highlighted as recipe reference in %#v", tokens)
	}
	if hasSemanticToken(tokens, 4, len(`# `), len("@comment"), semanticTokenFunction) {
		t.Fatalf("comment was highlighted as recipe reference in %#v", tokens)
	}
}

func TestSemanticTokensHighlightCrossConfigRecipeReferences(t *testing.T) {
	text := `[recipes.test]
cmd = ["@webui:gen-schema[mode=dev]"]
`
	tokens := decodeSemanticTokens(semanticTokens(text))
	linePrefix := len(`cmd = ["`)
	assertSemanticToken(t, tokens, 1, linePrefix, len("@webui:gen-schema"), semanticTokenFunction)
	assertSemanticToken(t, tokens, 1, linePrefix+len("@webui:gen-schema["), len("mode"), semanticTokenParameter)
	assertSemanticToken(t, tokens, 1, linePrefix+len("@webui:gen-schema[mode="), len("dev"), semanticTokenString)
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

func TestSemanticTokensHighlightShellTestCommandSubstitution(t *testing.T) {
	text := `shell = "sh"

[recipes.install]
cmd = '''
if [ "$(id -u)" -eq 0 ]; then
	echo root
fi
'''
`
	tokens := decodeSemanticTokens(semanticTokens(text))
	line := textLine(text, 4)
	assertNoOverlappingSemanticTokens(t, tokens)
	assertSemanticToken(t, tokens, 4, strings.Index(line, "if"), len("if"), semanticTokenKeyword)
	assertSemanticToken(t, tokens, 4, strings.Index(line, "["), len("["), semanticTokenFunction)
	assertSemanticToken(t, tokens, 4, strings.Index(line, "$("), len("$("), semanticTokenOperator)
	assertSemanticToken(t, tokens, 4, strings.Index(line, "id"), len("id"), semanticTokenFunction)
	assertSemanticToken(t, tokens, 4, strings.Index(line, "-u"), len("-u"), semanticTokenParameter)
	assertSemanticToken(t, tokens, 4, strings.Index(line, ")"), len(")"), semanticTokenOperator)
}

func TestSemanticTokensHighlightBashBacktickCommandSubstitution(t *testing.T) {
	text := `shell = "bash"

[recipes.install]
cmd = '''
owner=` + "`id -u`" + `
'''
`
	tokens := decodeSemanticTokens(semanticTokens(text))
	line := textLine(text, 4)
	assertNoOverlappingSemanticTokens(t, tokens)
	assertSemanticToken(t, tokens, 4, strings.Index(line, "`"), len("`"), semanticTokenOperator)
	assertSemanticToken(t, tokens, 4, strings.LastIndex(line, "`"), len("`"), semanticTokenOperator)
	assertSemanticToken(t, tokens, 4, strings.Index(line, "id"), len("id"), semanticTokenFunction)
	assertSemanticToken(t, tokens, 4, strings.Index(line, "-u"), len("-u"), semanticTokenParameter)
	if hasSemanticToken(tokens, 4, strings.Index(line, "id"), len("id"), semanticTokenString) {
		t.Fatalf("backtick command substitution was highlighted as string in %#v", tokens)
	}
}

func TestSemanticTokensHighlightUnquotedShellCommandSubstitutionWithoutOverlap(t *testing.T) {
	text := `shell = "sh"

[recipes.install]
cmd = '''
echo $(id -u)
'''
`
	tokens := decodeSemanticTokens(semanticTokens(text))
	line := textLine(text, 4)
	assertNoOverlappingSemanticTokens(t, tokens)
	assertSemanticToken(t, tokens, 4, strings.Index(line, "$("), len("$("), semanticTokenOperator)
	assertSemanticToken(t, tokens, 4, strings.Index(line, "id"), len("id"), semanticTokenFunction)
	assertSemanticToken(t, tokens, 4, strings.Index(line, "-u"), len("-u"), semanticTokenParameter)
	assertSemanticToken(t, tokens, 4, strings.LastIndex(line, ")"), len(")"), semanticTokenOperator)
}

func TestSemanticTokensHighlightNestedParameterExpansionWithoutOverlap(t *testing.T) {
	text := `shell = "sh"

[recipes.install]
cmd = '''
echo ${DESTDIR:-$HOME}
'''
`
	tokens := decodeSemanticTokens(semanticTokens(text))
	line := textLine(text, 4)
	assertNoOverlappingSemanticTokens(t, tokens)
	assertSemanticToken(t, tokens, 4, strings.Index(line, "${"), len("${DESTDIR:-$HOME}"), semanticTokenVariable)
	if hasSemanticToken(tokens, 4, strings.Index(line, "$HOME"), len("$HOME"), semanticTokenVariable) {
		t.Fatalf("nested parameter expansion overlapped outer expansion in %#v", tokens)
	}
}

func TestSemanticTokensHighlightPrePostScriptBodies(t *testing.T) {
	text := `[recipes.install]
pre = ['''
echo pre
''']
post = ['''
echo post
''']
cmd = ["true"]
`
	tokens := decodeSemanticTokens(semanticTokens(text))
	assertSemanticToken(t, tokens, 2, 0, len("echo"), semanticTokenFunction)
	assertSemanticToken(t, tokens, 5, 0, len("echo"), semanticTokenFunction)
}

func TestSemanticTokensHighlightInlinePrePostScriptBodies(t *testing.T) {
	text := `[recipes.install]
pre = ["echo pre"]
post = ["echo post"]
cmd = ["true"]
`
	tokens := decodeSemanticTokens(semanticTokens(text))
	assertSemanticToken(t, tokens, 1, len(`pre = ["`), len("echo"), semanticTokenFunction)
	assertSemanticToken(t, tokens, 2, len(`post = ["`), len("echo"), semanticTokenFunction)
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
	item := completionItem(t, result, label)
	edit, ok := item["textEdit"].(map[string]any)
	if !ok {
		t.Fatalf("textEdit has type %T", item["textEdit"])
	}
	return edit
}

func completionItem(t *testing.T, result map[string]any, label string) map[string]any {
	t.Helper()
	items, ok := result["items"].([]map[string]any)
	if !ok {
		t.Fatalf("items has type %T", result["items"])
	}
	for _, item := range items {
		if item["label"] != label {
			continue
		}
		return item
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

func assertCompletionDetail(t *testing.T, items []completion, label, detail string) {
	t.Helper()
	for _, item := range items {
		if item.Label != label {
			continue
		}
		if item.Detail != detail {
			t.Fatalf("%s detail = %q, want %q", label, item.Detail, detail)
		}
		return
	}
	t.Fatalf("missing label %q in %#v", label, items)
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

func assertNoOverlappingSemanticTokens(t *testing.T, tokens []semanticToken) {
	t.Helper()
	for i, token := range tokens {
		for _, other := range tokens[i+1:] {
			if token.Line != other.Line {
				continue
			}
			if token.Start < other.Start+other.Length && other.Start < token.Start+token.Length {
				t.Fatalf("overlapping semantic tokens %#v and %#v in %#v", token, other, tokens)
			}
		}
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
