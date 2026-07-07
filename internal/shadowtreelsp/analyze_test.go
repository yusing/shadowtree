package shadowtreelsp

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestCompletionsIncludeTopLevelTablesAfterOpenBracket(t *testing.T) {
	items := completionsAt(t.Context(), "[", lspPosition{Line: 0, Character: 1})
	assertLabels(t, items, "env", "vars", "var_commands", "recipes")
}

func TestCompletionsIncludeRecipeSubtables(t *testing.T) {
	text := `[recipes.build]
cmd = "go build"
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
	assertLabels(t, items, "cmd", "sandboxed", "sync_out", "log", "log_stages", "log_tee")
}

func TestCompletionsIncludeLogValues(t *testing.T) {
	cases := []struct {
		name   string
		text   string
		line   int
		column int
		labels []string
	}{
		{
			name: "log tee",
			text: `[recipes.test]
log = "run.log"
log_tee = `,
			line:   2,
			column: len(`log_tee = `),
			labels: []string{"true", "false"},
		},
		{
			name: "log stages",
			text: `[recipes.test]
log = "run.log"
log_stages = ["`,
			line:   2,
			column: len(`log_stages = ["`),
			labels: []string{"pre", "cmd", "post"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items := completionsAt(t.Context(), tc.text, lspPosition{Line: tc.line, Character: tc.column})
			assertLabels(t, items, tc.labels...)
		})
	}
}

func TestCompletionsExcludeKeysInSyncOutArrayValues(t *testing.T) {
	text := `[recipes.build]
sync_out = [
  "`
	items := completionsAt(t.Context(), text, lspPosition{Line: 2, Character: len(`  "`)})
	assertNoLabels(t, items, "cmd", "for_each", "sync_out")
}

func TestCompletionsExcludeKeysInMultilineSyncOutArrayValues(t *testing.T) {
	text := `[recipes.build]
sync_out = [
  '''
  bin/
  '''
]`
	items := completionsAt(t.Context(), text, lspPosition{Line: 3, Character: len(`  bin/`)})
	assertNoLabels(t, items, "cmd", "for_each", "sync_out")
}

func TestCompletionsIncludeKeysAfterIncompleteSyncOutArray(t *testing.T) {
	text := `[recipes.generate]
sync_out = [
  "

[recipes.build]
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 5, Character: 0})
	assertLabels(t, items, "cmd", "sandboxed", "sync_out")
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

func TestCompletionsIncludeIncludedPlaceholdersWithSourceHints(t *testing.T) {
	root := t.TempDir()
	includedPath := filepath.Join(root, "common.shadowtree.toml")
	if err := os.WriteFile(includedPath, []byte(`
[vars]
INCLUDED_VAR = "from-var"

[var_commands]
INCLUDED_DYNAMIC = "printf dynamic"

[recipes.included.vars]
INCLUDED_RECIPE_VAR = "from-recipe"

[recipes.included.arguments.target]
help = "Included target."

[recipes.included]
help = "Included recipe."
cmd = "echo {target}"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	text := `include = ["./common.shadowtree.toml"]

[recipes.included]
cmd = "echo {"
`
	items := completionsAtWithOptions(
		t.Context(),
		text,
		lspPosition{Line: 3, Character: len(`cmd = "echo {`)},
		completionOptions{ConfigPath: filepath.Join(root, ".shadowtree.toml")},
	)
	assertLabels(t, items, "{INCLUDED_VAR}", "{INCLUDED_DYNAMIC}", "{INCLUDED_RECIPE_VAR}", "{target}")
	assertCompletionDetail(t, items, "{INCLUDED_VAR}", "Shared placeholder (from common.shadowtree.toml)")
	assertCompletionDetail(t, items, "{INCLUDED_DYNAMIC}", "Shared placeholder (from common.shadowtree.toml)")
	assertCompletionDetail(t, items, "{INCLUDED_RECIPE_VAR}", "Recipe placeholder (from common.shadowtree.toml)")
	assertCompletionDetail(t, items, "{target}", "Included target. (from common.shadowtree.toml)")
}

func TestCompletionsIncludeMergedBuiltinArgumentPlaceholders(t *testing.T) {
	root := t.TempDir()
	text := `profile = "go"

[recipes.build]
cmd = "go build {"
`
	items := completionsAtWithOptions(
		t.Context(),
		text,
		lspPosition{Line: 3, Character: len(`cmd = "go build {`)},
		completionOptions{ConfigPath: filepath.Join(root, ".shadowtree.toml")},
	)
	assertLabels(t, items, "{pkg}")
}

func TestCompletionsIncludeMergedBuiltinArgumentTables(t *testing.T) {
	root := t.TempDir()
	text := `profile = "go"

[recipes.build.arguments.
`
	items := completionsAtWithOptions(
		t.Context(),
		text,
		lspPosition{Line: 2, Character: len(`[recipes.build.arguments.`)},
		completionOptions{ConfigPath: filepath.Join(root, ".shadowtree.toml")},
	)
	assertLabels(t, items, "pkg")
}

func TestCompletionsIncludeRecipeProfileTables(t *testing.T) {
	text := `[recipes.benchmark.arguments.connections]
type = "int"

[recipes.benchmark.profiles.stable.arguments]
connections = 64

[recipes.benchmark.profiles.
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 6, Character: len(`[recipes.benchmark.profiles.`)})
	assertLabels(t, items, "stable")

	items = completionsAt(t.Context(), text, lspPosition{Line: 3, Character: len(`[recipes.benchmark.profiles.stable.`)})
	assertLabels(t, items, "arguments")
}

func TestCompletionsIncludeRecipeProfileArgumentKeys(t *testing.T) {
	text := `[recipes.benchmark.arguments.connections]
type = "int"

[recipes.benchmark.arguments.requests]
type = "int"

[recipes.benchmark.profiles.stable.arguments]
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 6, Character: 0})
	assertLabels(t, items, "connections", "requests")
}

func TestCompletionsIncludeRecipeProfileArgumentValues(t *testing.T) {
	text := `[recipes.test.arguments.race]
type = "bool"

[recipes.test.profiles.stable.arguments]
race = ` + "\n"
	items := completionsAt(t.Context(), text, lspPosition{Line: 4, Character: len(`race = `)})
	assertLabels(t, items, "true", "false")
}

func TestCompletionsIncludeForEachItemPlaceholders(t *testing.T) {
	text := `[recipes.lint]
for_each = "@enum a b"
cmd = "echo {"
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 2, Character: len(`cmd = "echo {`)})
	assertLabels(t, items, "{item}", "{item_help}", "{item_index}")
	assertCompletionDetail(t, items, "{item}", "Current for_each value")
}

func TestCompletionsIncludeForEachItemPlaceholdersInWorkdir(t *testing.T) {
	text := `[recipes.lint]
for_each = "@enum a b"
workdir = "services/{"
cmd = "echo {item}"
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 2, Character: len(`workdir = "services/{`)})
	assertLabels(t, items, "{item}", "{item_help}", "{item_index}")

	result := completionResult(t.Context(), text, lspPosition{Line: 2, Character: len(`workdir = "services/{`)})
	edit := completionTextEdit(t, result, "{item}")
	if edit["newText"] != "item}" {
		t.Fatalf("newText = %#v, want placeholder suffix", edit["newText"])
	}
}

func TestCompletionsIncludeWorkdirPaths(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "services", "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "services", "README.md"), []byte("docs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	text := `[recipes.test]
workdir = s`
	items := completionsAtWithOptions(
		t.Context(),
		text,
		lspPosition{Line: 1, Character: len(`workdir = s`)},
		completionOptions{Dir: root},
	)
	assertLabels(t, items, "services/")
	assertNoLabels(t, items, "services/README.md")

	result := completionResultWithOptions(
		t.Context(),
		text,
		lspPosition{Line: 1, Character: len(`workdir = s`)},
		completionOptions{Dir: root},
	)
	if result["isIncomplete"] != true {
		t.Fatalf("isIncomplete = %#v, want true so editor re-queries after typing placeholders", result["isIncomplete"])
	}
	edit := completionTextEdit(t, result, "services/")
	if edit["newText"] != `"services/"` {
		t.Fatalf("newText = %#v, want quoted workdir path", edit["newText"])
	}

	text = strings.Replace(text, "workdir = s", `workdir = "services/a`, 1)
	result = completionResultWithOptions(
		t.Context(),
		text,
		lspPosition{Line: 1, Character: len(`workdir = "services/a`)},
		completionOptions{Dir: root},
	)
	edit = completionTextEdit(t, result, "services/api/")
	if edit["newText"] != `services/api/"` {
		t.Fatalf("newText = %#v, want replacement plus closing quote", edit["newText"])
	}
}

func TestCompletionsExcludeAbsoluteWorkdirPaths(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "services"), 0o755); err != nil {
		t.Fatal(err)
	}
	text := `[recipes.test]
workdir = /`
	items := completionsAtWithOptions(
		t.Context(),
		text,
		lspPosition{Line: 1, Character: len(`workdir = /`)},
		completionOptions{Dir: root},
	)
	if len(items) != 0 {
		t.Fatalf("items = %#v, want no absolute workdir path completions", items)
	}
}

func TestCompletionsIncludeIncludePathFilesAndDirectories(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "dir", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "common.shadowtree.toml"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dir", "shared.toml"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	text := `include = [""]`
	opts := completionOptions{ConfigPath: filepath.Join(root, ".shadowtree.toml")}
	items := completionsAtWithOptions(t.Context(), text, lspPosition{Line: 0, Character: len(`include = ["`)}, opts)
	assertLabels(t, items, "common.shadowtree.toml", "dir/")
	assertNoLabels(t, items, "README.md")

	text = `include = ["dir/"]`
	items = completionsAtWithOptions(t.Context(), text, lspPosition{Line: 0, Character: len(`include = ["dir/`)}, opts)
	assertLabels(t, items, "dir/shared.toml", "dir/nested/")

	result := completionResultWithOptions(t.Context(), text, lspPosition{Line: 0, Character: len(`include = ["dir/`)}, opts)
	edit := completionTextEdit(t, result, "dir/shared.toml")
	if edit["newText"] != "dir/shared.toml" {
		t.Fatalf("newText = %#v, want include path", edit["newText"])
	}
}

func TestCompletionsExcludeWorkdirPathsInScriptRegion(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "services"), 0o755); err != nil {
		t.Fatal(err)
	}
	text := `[recipes.test]
cmd = '''
workdir = s
'''
`
	items := completionsAtWithOptions(
		t.Context(),
		text,
		lspPosition{Line: 2, Character: len(`workdir = s`)},
		completionOptions{Dir: root},
	)
	assertNoLabels(t, items, "services/")
}

func TestCompletionsExcludeForEachItemPlaceholdersOutsideMainAndWorkdir(t *testing.T) {
	cases := []struct {
		name string
		text string
		line int
		col  int
	}{
		{
			name: "for_each",
			text: `[recipes.lint]
for_each = "{"
cmd = "echo {item}"
`,
			line: 1,
			col:  len(`for_each = "{`),
		},
		{
			name: "pre",
			text: `[recipes.lint]
for_each = "@enum a b"
pre = ["echo {"]
cmd = "echo {item}"
`,
			line: 2,
			col:  len(`pre = ["echo {`),
		},
		{
			name: "post",
			text: `[recipes.lint]
for_each = "@enum a b"
cmd = "echo {item}"
post = ["echo {"]
`,
			line: 3,
			col:  len(`post = ["echo {`),
		},
		{
			name: "sync_out",
			text: `[recipes.lint]
for_each = "@enum a b"
cmd = "echo {item}"
sync_out = ["out/{"]
`,
			line: 3,
			col:  len(`sync_out = ["out/{`),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items := completionsAt(t.Context(), tc.text, lspPosition{Line: tc.line, Character: tc.col})
			assertNoLabels(t, items, "{item}", "{item_help}", "{item_index}")
		})
	}
}

func TestCompletionsIncludeVariadicArgsPlaceholder(t *testing.T) {
	text := `[recipes.test]
cmd = "echo {`
	items := completionsAt(t.Context(), text, lspPosition{Line: 1, Character: len(`cmd = "echo {`)})
	assertLabels(t, items, "{@}")

	result := completionResult(t.Context(), text, lspPosition{Line: 1, Character: len(`cmd = "echo {`)})
	edit := completionTextEdit(t, result, "{@}")
	if edit["newText"] != "@}" {
		t.Fatalf("newText = %#v, want variadic placeholder suffix", edit["newText"])
	}
}

func TestCompletionsIncludeRunIDPlaceholder(t *testing.T) {
	text := `[recipes.test]
log = "logs/{`
	items := completionsAt(t.Context(), text, lspPosition{Line: 1, Character: len(`log = "logs/{`)})
	assertLabels(t, items, "{run_id}")
}

func TestCompletionsExcludeVariadicArgsPlaceholderInSyncOut(t *testing.T) {
	text := `[recipes.test]
sync_out = ["{`
	items := completionsAt(t.Context(), text, lspPosition{Line: 1, Character: len(`sync_out = ["{`)})
	assertNoLabels(t, items, "{@}")
}

func TestCompletionsIncludeRecipeReferences(t *testing.T) {
	text := `[recipes.gen-swagger]
help = "Generate Swagger docs."
cmd = "go generate ./..."

[recipes.vet]
cmd = "go vet"

[recipes.test]
pre = ["echo 123", "@gen"]
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 8, Character: len(`pre = ["echo 123", "@`)})
	assertLabels(t, items, "@gen-swagger", "@vet")
	assertCompletionDetail(t, items, "@gen-swagger", "Generate Swagger docs.")

	items = completionsAt(t.Context(), text, lspPosition{Line: 8, Character: len(`pre = ["echo 123", "@gen`)})
	assertLabels(t, items, "@gen-swagger")
}

func TestCompletionsIncludeIncludedRecipeReferencesWithSourceHints(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "common.shadowtree.toml"), []byte(`
[recipes.common.arguments.target]
help = "Included target."

[recipes.common]
help = "Common recipe."
cmd = "echo {target}"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	text := `include = ["./common.shadowtree.toml"]

[recipes.test]
pre = ["@com"]
`
	opts := completionOptions{ConfigPath: filepath.Join(root, ".shadowtree.toml")}
	items := completionsAtWithOptions(t.Context(), text, lspPosition{Line: 3, Character: len(`pre = ["@com`)}, opts)
	assertLabels(t, items, "@common")
	assertCompletionDetail(t, items, "@common", "Common recipe. (from common.shadowtree.toml)")

	text = strings.Replace(text, `"@com"]`, `"@common["]`, 1)
	items = completionsAtWithOptions(t.Context(), text, lspPosition{Line: 3, Character: len(`pre = ["@common[`)}, opts)
	assertLabels(t, items, "target=")
	assertCompletionDetail(t, items, "target=", "Included target. (from common.shadowtree.toml)")
}

func TestCompletionsKeepIncludedHelpSourceHintsThroughPartialOverrides(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "common.shadowtree.toml"), []byte(`
[recipes.build.arguments.target]
help = "Target from common."

[recipes.build]
help = "Build from common."
cmd = "echo {target}"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	text := `include = ["./common.shadowtree.toml"]

[recipes.build.arguments.target]
default = "."

[recipes.build]
cmd = "go build {target}"

[recipes.test]
pre = ["@b"]
`
	opts := completionOptions{ConfigPath: filepath.Join(root, ".shadowtree.toml")}
	items := completionsAtWithOptions(t.Context(), text, lspPosition{Line: 9, Character: len(`pre = ["@b`)}, opts)
	assertLabels(t, items, "@build")
	assertCompletionDetail(t, items, "@build", "Build from common. (from common.shadowtree.toml)")

	text = strings.Replace(text, `"@b"]`, `"@build["]`, 1)
	items = completionsAtWithOptions(t.Context(), text, lspPosition{Line: 9, Character: len(`pre = ["@build[`)}, opts)
	assertLabels(t, items, "target=")
	assertCompletionDetail(t, items, "target=", "Target from common. (from common.shadowtree.toml)")
}

func TestRecipeReferenceCompletionUsesRecipeHelpAndTextEdit(t *testing.T) {
	text := `[recipes.gen-swagger]
help = "Generate Swagger docs."
cmd = "go generate ./..."

[recipes.test]
pre = ["@gen"]
`
	result := completionResult(t.Context(), text, lspPosition{Line: 5, Character: len(`pre = ["@gen`)})
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
cmd = "go vet"

[recipes.check]
pre = ["@v"]
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 4, Character: len(`pre = ["@v`)})
	assertLabels(t, items, "@vet")
}

func TestCompletionsIncludeBareRecipeReferencePrefixes(t *testing.T) {
	text := `[recipes.vet]
cmd = "go vet"

[recipes.check]
cmd = "@"
pre = ["@
post = ["@"
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 4, Character: len(`cmd = "@`)})
	assertLabels(t, items, "@vet")

	items = completionsAt(t.Context(), text, lspPosition{Line: 5, Character: len(`pre = ["@`)})
	assertLabels(t, items, "@vet")

	items = completionsAt(t.Context(), text, lspPosition{Line: 6, Character: len(`post = ["@`)})
	assertLabels(t, items, "@vet")
}

func TestCompletionsIncludeMultilineCommandListRecipeReferences(t *testing.T) {
	text := `[recipes.build]
cmd = "go build"

[recipes.prune-db-types]
cmd = "go run ./tools/database/go_types_generator"

[recipes.check]
pre = [
  "mkdir -p bin",
  "@build[project=tools/database/go_types_generator]",
  "@prune-db-t"
]
post = [
  "@prune-db-t"
]
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 10, Character: len(`  "@prune-db-t`)})
	assertLabels(t, items, "@prune-db-types")

	items = completionsAt(t.Context(), text, lspPosition{Line: 13, Character: len(`  "@prune-db-t`)})
	assertLabels(t, items, "@prune-db-types")

	result := completionResult(t.Context(), text, lspPosition{Line: 10, Character: len(`  "@prune-db-t`)})
	edit := completionTextEdit(t, result, "@prune-db-types")
	if edit["newText"] != "prune-db-types" {
		t.Fatalf("newText = %#v, want recipe name", edit["newText"])
	}
	assertEditRange(t, edit, len(`  "@`), len(`  "@prune-db-t`))
}

func TestCompletionsIncludeBareScriptRecipeReferencePrefixes(t *testing.T) {
	text := `[recipes.vet]
cmd = "go vet"

[recipes.check]
pre = ['''
@
''']
post = ['''
@
''']
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 5, Character: len(`@`)})
	assertLabels(t, items, "@vet")

	items = completionsAt(t.Context(), text, lspPosition{Line: 8, Character: len(`@`)})
	assertLabels(t, items, "@vet")
}

func TestCompletionsIncludeStringRecipeReferences(t *testing.T) {
	text := `[recipes.test]
cmd = "go test"

[recipes.check]
cmd = "@t"
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 4, Character: len(`cmd = "@t`)})
	assertLabels(t, items, "@test")
}

func TestCompletionsIncludeShellVariablesInScriptRecipeReferenceArgs(t *testing.T) {
	text := `[env]
GOFLAGS = "-count=1"

[recipes.bench.env]
BENCHTIME = "1x"

[recipes.bench]
cmd = '''
pkg=./internal/runner
bench=BenchmarkRun
@test "$p" -bench "$b" -benchtime="$"
'''
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 10, Character: len(`@test "$p`)})
	assertLabels(t, items, "$pkg")

	items = completionsAt(t.Context(), text, lspPosition{Line: 10, Character: len(`@test "$p" -bench "$b`)})
	assertLabels(t, items, "$bench")

	items = completionsAt(t.Context(), text, lspPosition{Line: 10, Character: len(`@test "$p" -bench "$b" -benchtime="$`)})
	assertLabels(t, items, "$GOFLAGS", "$BENCHTIME", "$pkg", "$bench")

	result := completionResult(t.Context(), text, lspPosition{Line: 10, Character: len(`@test "$p`)})
	edit := completionTextEdit(t, result, "$pkg")
	if edit["newText"] != "pkg" {
		t.Fatalf("newText = %#v, want variable name", edit["newText"])
	}
	assertEditRange(t, edit, len(`@test "$`), len(`@test "$p`))
}

func TestCompletionsIncludeIncludedShellVariablesWithSourceHints(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "common.shadowtree.toml"), []byte(`
[env]
INCLUDED_ENV = "top"

[recipes.included.env]
INCLUDED_RECIPE_ENV = "recipe"

[recipes.included]
cmd = "echo"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	text := `include = ["./common.shadowtree.toml"]

[recipes.included]
cmd = '''
echo "$"
'''
`
	items := completionsAtWithOptions(
		t.Context(),
		text,
		lspPosition{Line: 4, Character: len(`echo "$`)},
		completionOptions{ConfigPath: filepath.Join(root, ".shadowtree.toml")},
	)
	assertLabels(t, items, "$INCLUDED_ENV", "$INCLUDED_RECIPE_ENV")
	assertCompletionDetail(t, items, "$INCLUDED_ENV", "Shell variable (from common.shadowtree.toml)")
	assertCompletionDetail(t, items, "$INCLUDED_RECIPE_ENV", "Shell variable (from common.shadowtree.toml)")
}

func TestCompletionsIncludeExportedShellVariables(t *testing.T) {
	text := `[recipes.bench]
cmd = '''
export CALLFILTER_BENCH_ROWS={count}
$
'''
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 3, Character: len(`$`)})
	assertLabels(t, items, "$CALLFILTER_BENCH_ROWS")
}

func TestCompletionsExcludeCommandScopedShellAssignments(t *testing.T) {
	text := `[recipes.bench]
cmd = '''
scoped=123 foo
persistent=456
$
'''
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 4, Character: len(`$`)})
	assertLabels(t, items, "$persistent")
	assertNoLabels(t, items, "$scoped")
}

func TestCompletionsIncludeBracedShellVariables(t *testing.T) {
	text := `[recipes.bench]
cmd = '''
pkg=./internal/runner
@test "${p"
'''
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 3, Character: len(`@test "${p`)})
	assertLabels(t, items, "${pkg}")

	result := completionResult(t.Context(), text, lspPosition{Line: 3, Character: len(`@test "${p`)})
	edit := completionTextEdit(t, result, "${pkg}")
	if edit["newText"] != "pkg}" {
		t.Fatalf("newText = %#v, want braced variable tail", edit["newText"])
	}
	assertEditRange(t, edit, len(`@test "${`), len(`@test "${p`))
}

func TestCompletionsExcludeShellVariablesInSingleQuotes(t *testing.T) {
	text := `[recipes.bench]
cmd = '''
pkg=./internal/runner
@test '$p'
'''
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 3, Character: len(`@test '$p`)})
	assertNoLabels(t, items, "$pkg")
}

func TestCommandReferenceSpansMarkOnlyExpandedShellArgumentsDynamic(t *testing.T) {
	text := `[recipes.test]
cmd = '''
@build literal='$not_dynamic' expanded="$dynamic"
'''
`
	lines := strings.Split(text, "\n")
	refs := commandReferenceSpans(lines)
	if len(refs) != 1 {
		t.Fatalf("refs = %#v, want one", refs)
	}
	if len(refs[0].Args) != 2 {
		t.Fatalf("args = %#v, want two", refs[0].Args)
	}
	if refs[0].Args[0].Text != "literal='$not_dynamic'" || refs[0].Args[1].Text != `expanded="$dynamic"` {
		t.Fatalf("args = %#v", refs[0].Args)
	}
	if refs[0].Args[0].Dynamic {
		t.Fatalf("single-quoted arg marked dynamic: %#v", refs[0].Args[0])
	}
	if !refs[0].Args[1].Dynamic {
		t.Fatalf("double-quoted arg was not marked dynamic: %#v", refs[0].Args[1])
	}
}

func TestCompletionsIncludeEnumInArgumentValuesReferences(t *testing.T) {
	text := `[recipes.test.arguments.target]
values = "@"

[recipes.test]
cmd = "go test"
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 1, Character: len(`values = "@`)})
	assertLabels(t, items, "@enum", "@glob", "@go-main-packages", "@go-modules", "@go-packages", "@lines", "@recipes", "@test", "@vars")
	assertCompletionDetail(t, items, "@enum", "Static argument values (builtin)")
}

func TestCompletionsIncludeEnumInForEachReferences(t *testing.T) {
	text := `[recipes.lint]
for_each = "@"
cmd = "true"
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 1, Character: len(`for_each = "@`)})
	assertLabels(t, items, "@enum", "@glob", "@go-main-packages", "@go-modules", "@go-packages", "@lines", "@recipes", "@vars")
	assertCompletionDetail(t, items, "@go-modules", "Go module directories (builtin)")
}

func TestCompletionsIncludeEnumInScriptArgumentValuesReferences(t *testing.T) {
	text := `[recipes.test.arguments.target]
values = '''
@e
'''

[recipes.test]
cmd = "go test"
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
cmd = "npm test"

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
cmd = "go build"

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
cmd = "go build"

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
cmd = "go build"

[recipes.test]
pre = ["@build[mode="]
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 7, Character: len(`pre = ["@build[mode=`)})
	assertLabels(t, items, "true", "false")
}

func TestCompletionsIncludeArgumentDefaultValues(t *testing.T) {
	text := `[recipes.build.arguments.component]
type = "string"
values = "@enum api worker"
default = `
	items := completionsAt(t.Context(), text, lspPosition{Line: 3, Character: len(`default = `)})
	assertLabels(t, items, "api", "worker")

	result := completionResult(t.Context(), text, lspPosition{Line: 3, Character: len(`default = `)})
	edit := completionTextEdit(t, result, "api")
	if edit["newText"] != `"api"` {
		t.Fatalf("newText = %#v, want quoted default value", edit["newText"])
	}

	text = strings.Replace(text, "default = ", "default = a", 1)
	items = completionsAt(t.Context(), text, lspPosition{Line: 3, Character: len(`default = a`)})
	assertLabels(t, items, "api")

	text = strings.Replace(text, "default = a", `default = "a`, 1)
	result = completionResult(t.Context(), text, lspPosition{Line: 3, Character: len(`default = "a`)})
	edit = completionTextEdit(t, result, "api")
	if edit["newText"] != `api"` {
		t.Fatalf("newText = %#v, want replacement plus closing quote", edit["newText"])
	}
}

func TestCompletionsIncludeEnumArgumentDefaultValueHelp(t *testing.T) {
	text := `[recipes.build.arguments.component]
type = "string"
values = "@enum all='all modules' api='API service'"
default = a`
	items := completionsAt(t.Context(), text, lspPosition{Line: 3, Character: len(`default = a`)})
	assertLabels(t, items, "all", "api")
	assertCompletionDetail(t, items, "api", "API service")
}

func TestCompletionsIncludeGoModulesArgumentDefaultValues(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "services", "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "services", "api", "go.mod"), []byte("module example.com/api\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	text := `[recipes.test.arguments.target]
type = "string"
values = "@go-modules"
default = s`
	items := completionsAtWithOptions(
		t.Context(),
		text,
		lspPosition{Line: 3, Character: len(`default = s`)},
		completionOptions{ConfigPath: filepath.Join(root, ".shadowtree.toml")},
	)
	assertLabels(t, items, "services/api")
	assertCompletionDetail(t, items, "services/api", "example.com/api")
}

func TestCompletionsIncludeComposedBuiltinArgumentDefaultValues(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	text := `[recipes.test.arguments.target]
type = "string"
values = "@go-modules; @enum all='all modules'"
default = a`
	items := completionsAtWithOptions(
		t.Context(),
		text,
		lspPosition{Line: 3, Character: len(`default = a`)},
		completionOptions{ConfigPath: filepath.Join(root, ".shadowtree.toml")},
	)
	assertLabels(t, items, "all")
	assertCompletionDetail(t, items, "all", "all modules")
}

func TestCompletionsIncludeGoMainPackagesArgumentDefaultValues(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "cmd", "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cmd", "api", "main.go"), []byte("// Package main builds the API.\npackage main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	text := `[recipes.build.arguments.project]
type = "string"
values = "@go-main-packages"
default = ./c`
	items := completionsAtWithOptions(
		t.Context(),
		text,
		lspPosition{Line: 3, Character: len(`default = ./c`)},
		completionOptions{ConfigPath: filepath.Join(root, ".shadowtree.toml")},
	)
	assertLabels(t, items, "./cmd/api")
	assertCompletionDetail(t, items, "./cmd/api", "Package main builds the API.")
}

func TestCompletionsDoNotRunCommandBackedArgumentValues(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	text := `[recipes.build.arguments.component]
type = "string"
values = ` + strconv.Quote("touch "+marker+"\nprintf api") + `
default =

[recipes.build]
cmd = "go build"
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 3, Character: len(`default = `)})
	if len(items) != 0 {
		t.Fatalf("items = %#v, want no command-backed value completions", items)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("command-backed values ran, stat err = %v", err)
	}
}

func TestCompletionsEscapeArgumentDefaultStringValues(t *testing.T) {
	text := `[recipes.build.arguments.component]
type = "string"
values = "@enum 'api\"v2' 'path\\to'"
default = `
	result := completionResult(t.Context(), text, lspPosition{Line: 3, Character: len(`default = `)})

	edit := completionTextEdit(t, result, `api"v2`)
	if edit["newText"] != `"api\"v2"` {
		t.Fatalf("newText = %#v, want TOML-escaped quote", edit["newText"])
	}

	edit = completionTextEdit(t, result, `path\to`)
	if edit["newText"] != `"path\\to"` {
		t.Fatalf("newText = %#v, want TOML-escaped backslash", edit["newText"])
	}
}

func TestCompletionsIncludeBoolArgumentDefaultValues(t *testing.T) {
	text := `[recipes.test.arguments.race]
type = "bool"
default = `
	items := completionsAt(t.Context(), text, lspPosition{Line: 2, Character: len(`default = `)})
	assertLabels(t, items, "true", "false")
}

func TestCompletionsIncludeScriptRecipeReferences(t *testing.T) {
	text := `[recipes.gen-swagger]
help = "Generate Swagger docs."
cmd = "go generate ./..."

[recipes.vet]
cmd = "go vet"

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
cmd = "go generate ./..."

[recipes.test]
cmd = "go test"
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 1, Character: len(`@g`)})
	assertLabels(t, items, "@gen-swagger")
	assertCompletionDetail(t, items, "@gen-swagger", "Generate Swagger docs.")
}

func TestCompletionsIncludeRecipeShellPreludeRecipeReferences(t *testing.T) {
	text := `[recipes.gen-swagger]
help = "Generate Swagger docs."
cmd = "go generate ./..."

[recipes.test]
shell_prelude = '''
@g
'''
cmd = "go test"
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
cmd = "go build"

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
cmd = "go build"

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
cmd = "go build"

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
cmd = "go build"

[recipes.test]
cmd = '''
@build mode=
'''
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 8, Character: len(`@build mode=`)})
	assertLabels(t, items, "true", "false")
}

func TestCompletionsIncludeValuesScriptRecipeReferenceEnumArgumentValues(t *testing.T) {
	text := `[recipes.minify.arguments.component]
type = "string"
values = "@enum godoxy agent socket-proxy cli"

[recipes.minify]
cmd = "true"

[recipes.test.arguments.target]
type = "string"
values = '''
@minify component="
'''
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 10, Character: len(`@minify component="`)})
	assertLabels(t, items, "godoxy", "agent", "socket-proxy", "cli")
}

func TestCompletionsIncludeValuesScriptRecipeReferenceEnumBracketArgumentValues(t *testing.T) {
	text := `[recipes.minify.arguments.component]
type = "string"
values = "@enum godoxy agent socket-proxy cli"

[recipes.minify]
cmd = "true"

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
cmd = "go build"
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
cmd = "go build"
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
cmd = "go build"

[recipes.test]
pre = ["@build["]
post = ["@build mode="]
cmd = "true"
`
	items := completionsAt(t.Context(), text, lspPosition{Line: 10, Character: len(`pre = ["@build[`)})
	assertLabels(t, items, "component=", "mode=")

	items = completionsAt(t.Context(), text, lspPosition{Line: 11, Character: len(`post = ["@build mode=`)})
	assertLabels(t, items, "true", "false")
}

func TestCompletionsIgnoreScriptRecipeReferenceArguments(t *testing.T) {
	text := `[recipes.gen]
cmd = "true"

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
cmd = "@web"
`
	items := completionsAtWithOptions(
		t.Context(),
		text,
		lspPosition{Line: 1, Character: len(`cmd = "@web`)},
		completionOptions{ConfigPath: filepath.Join(root, ".shadowtree.toml")},
	)
	assertLabels(t, items, "@webui:")
}

func TestCompletionsIncludeCrossConfigRecipeReferences(t *testing.T) {
	root := t.TempDir()
	writeLSPTargetConfig(t, root, "gen-schema")
	text := `[recipes.test]
cmd = "@webui:g"
`
	items := completionsAtWithOptions(
		t.Context(),
		text,
		lspPosition{Line: 1, Character: len(`cmd = "@webui:g`)},
		completionOptions{ConfigPath: filepath.Join(root, ".shadowtree.toml")},
	)
	assertLabels(t, items, "@webui:gen-schema")
	assertCompletionDetail(t, items, "@webui:gen-schema", "Target recipe help.")
}

func TestCompletionsRejectCrossConfigRecipeReferenceThroughOutsideSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeLSPTargetConfig(t, outside, "gen-schema")
	if err := os.Symlink(filepath.Join(outside, "webui"), filepath.Join(root, "webui")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	text := `[recipes.test]
cmd = "@webui:g"
`
	items := completionsAtWithOptions(
		t.Context(),
		text,
		lspPosition{Line: 1, Character: len(`cmd = "@webui:g`)},
		completionOptions{ConfigPath: filepath.Join(root, ".shadowtree.toml")},
	)
	assertNoLabels(t, items, "@webui:gen-schema")
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

[recipes.gen-schema.arguments.target]
values = "@recipes"

[recipes.gen-schema]
cmd = "true"

[recipes.target-only]
cmd = "true"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	text := `[recipes.test]
cmd = "@webui:gen-schema["
`
	items := completionsAtWithOptions(
		t.Context(),
		text,
		lspPosition{Line: 1, Character: len(`cmd = "@webui:gen-schema[`)},
		completionOptions{ConfigPath: filepath.Join(root, ".shadowtree.toml")},
	)
	assertLabels(t, items, "mode=")

	text = `[recipes.test]
cmd = "@webui:gen-schema[mode="
`
	items = completionsAtWithOptions(
		t.Context(),
		text,
		lspPosition{Line: 1, Character: len(`cmd = "@webui:gen-schema[mode=`)},
		completionOptions{ConfigPath: filepath.Join(root, ".shadowtree.toml")},
	)
	assertLabels(t, items, "true", "false")

	text = `[recipes.test]
cmd = "@webui:gen-schema[target="
`
	items = completionsAtWithOptions(
		t.Context(),
		text,
		lspPosition{Line: 1, Character: len(`cmd = "@webui:gen-schema[target=`)},
		completionOptions{ConfigPath: filepath.Join(root, ".shadowtree.toml")},
	)
	assertLabels(t, items, "target-only")
	assertNoLabels(t, items, "test")
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
cmd = "go build"

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
cmd = "go test"

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
cmd = "go generate ./..."

[recipes.test]
pre = ["@gen"]
`
	result := completionResult(t.Context(), text, lspPosition{Line: 4, Character: len(`pre = ["@gen`)})
	edit := completionTextEdit(t, result, "@gen-swagger")
	if edit["newText"] != "gen-swagger" {
		t.Fatalf("newText = %#v, want recipe name", edit["newText"])
	}
	assertEditRange(t, edit, len(`pre = ["@`), len(`pre = ["@gen`))
}

func TestCompletionsIncludeSupportedShellValues(t *testing.T) {
	items := completionsAt(t.Context(), "shell = ", lspPosition{Line: 0, Character: len("shell = ")})
	assertLabels(t, items, "sh", "bash")
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

func TestQuotedValueTextEditClosesUnclosedQuotes(t *testing.T) {
	edit := quotedValueTextEdit(`shell = "`, lspPosition{Line: 0, Character: len(`shell = "`)}, "sh")
	if edit["newText"] != `sh"` {
		t.Fatalf("newText = %#v, want value and closing quote", edit["newText"])
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
cmd = "{@}"
`
	tokens := decodeSemanticTokens(semanticTokens(text))
	if !hasSemanticToken(tokens, 1, len(`cmd = "`), len("{@}"), semanticTokenVariable) {
		t.Fatalf("missing variadic placeholder token in %#v", tokens)
	}
}

func TestSemanticTokensIncludeVariadicArgsPlaceholderInScriptRecipeReference(t *testing.T) {
	text := `[recipes.check]
cmd = '''
@vet {@}
@test {@}
'''
`
	tokens := decodeSemanticTokens(semanticTokens(text))
	assertSemanticToken(t, tokens, 2, len("@vet "), len("{@}"), semanticTokenVariable)
	assertSemanticToken(t, tokens, 3, len("@test "), len("{@}"), semanticTokenVariable)
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
cmd = "@test"
`
	tokens := decodeSemanticTokens(semanticTokens(text))
	assertSemanticToken(t, tokens, 1, len(`pre = ["`), len("@vet"), semanticTokenFunction)
	assertSemanticToken(t, tokens, 2, len(`cmd = "`), len("@test"), semanticTokenFunction)
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
cmd = "@webui:gen-schema[mode=dev]"
`
	tokens := decodeSemanticTokens(semanticTokens(text))
	linePrefix := len(`cmd = "`)
	assertSemanticToken(t, tokens, 1, linePrefix, len("@webui:gen-schema"), semanticTokenFunction)
	assertSemanticToken(t, tokens, 1, linePrefix+len("@webui:gen-schema["), len("mode"), semanticTokenParameter)
	assertSemanticToken(t, tokens, 1, linePrefix+len("@webui:gen-schema[mode="), len("dev"), semanticTokenString)
}

func TestSemanticTokensIgnoreShellStringStartingWithAt(t *testing.T) {
	text := `[recipes.check]
pre = ["@echo hi"]
cmd = "go test"
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

func TestSemanticTokensDoNotHighlightHeredocBodyAsShell(t *testing.T) {
	text := `shell = "sh"

[recipes.install]
cmd = '''
cat <<EOF
echo literal $HOME $(id -u) # not a comment
EOF
echo after
'''
	`
	tokens := decodeSemanticTokens(semanticTokens(text))
	startMarker := textLine(text, 4)
	body := textLine(text, 5)
	endMarker := textLine(text, 6)
	after := textLine(text, 7)
	index := func(line, value string) int {
		t.Helper()
		col := strings.Index(line, value)
		if col < 0 {
			t.Fatalf("missing %q in %q", value, line)
		}
		return col
	}
	assertNoOverlappingSemanticTokens(t, tokens)
	assertSemanticToken(t, tokens, 4, 0, len("cat"), semanticTokenFunction)
	assertSemanticToken(t, tokens, 4, index(startMarker, "EOF"), len("EOF"), semanticTokenFunction)
	assertSemanticToken(t, tokens, 6, index(endMarker, "EOF"), len("EOF"), semanticTokenFunction)
	assertSemanticToken(t, tokens, 7, 0, len("echo"), semanticTokenFunction)
	if hasSemanticToken(tokens, 5, index(body, "echo"), len("echo"), semanticTokenFunction) {
		t.Fatalf("heredoc body command was highlighted as shell in %#v", tokens)
	}
	if hasSemanticToken(tokens, 5, index(body, "$HOME"), len("$HOME"), semanticTokenVariable) {
		t.Fatalf("heredoc body variable was highlighted as shell in %#v", tokens)
	}
	if hasSemanticToken(tokens, 5, index(body, "id"), len("id"), semanticTokenFunction) {
		t.Fatalf("heredoc body command substitution was highlighted as shell in %#v", tokens)
	}
	if hasSemanticToken(tokens, 5, index(body, "#"), len("# not a comment"), semanticTokenComment) {
		t.Fatalf("heredoc body comment was highlighted as shell in %#v", tokens)
	}
	if hasSemanticToken(tokens, 7, index(after, "after"), len("after"), semanticTokenFunction) {
		t.Fatalf("post-heredoc argument was highlighted as command in %#v", tokens)
	}
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
cmd = "true"
`
	tokens := decodeSemanticTokens(semanticTokens(text))
	assertSemanticToken(t, tokens, 2, 0, len("echo"), semanticTokenFunction)
	assertSemanticToken(t, tokens, 5, 0, len("echo"), semanticTokenFunction)
}

func TestSemanticTokensHighlightInlinePrePostScriptBodies(t *testing.T) {
	text := `[recipes.install]
pre = ["echo pre"]
post = ["echo post"]
cmd = "true"
`
	tokens := decodeSemanticTokens(semanticTokens(text))
	assertSemanticToken(t, tokens, 1, len(`pre = ["`), len("echo"), semanticTokenFunction)
	assertSemanticToken(t, tokens, 2, len(`post = ["`), len("echo"), semanticTokenFunction)
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

func assertNoLabels(t *testing.T, items []completion, labels ...string) {
	t.Helper()
	var got []string
	for _, item := range items {
		got = append(got, item.Label)
	}
	for _, label := range labels {
		if slices.Contains(got, label) {
			t.Fatalf("unexpected label %q in %#v", label, got)
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
