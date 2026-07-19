package recipe

import "slices"

func rustBuiltins(toolchain string) map[string]Recipe {
	cargo := func(subcommand string, args ...string) Command {
		return append(Command{"cargo", "+" + toolchain, subcommand}, args...)
	}
	recipes := map[string]Recipe{
		"check": {
			Help: "Check Rust code.",
			Cmd:  cargo("check", "{@}"),
		},
		"test": {
			Help: "Run Rust tests.",
			Cmd:  cargo("test", "{@}"),
		},
		"build": {
			Help: "Build Rust packages.",
			Cmd:  cargo("build", "{@}"),
		},
		"run": {
			Help: "Run a Rust binary.",
			Cmd:  cargo("run", "{@}"),
		},
		"fmt": {
			Help: "Format Rust code.",
			Pre:  StageCommands{{Cmd: ScriptCommand(`cargo +` + toolchain + ` fmt --version >/dev/null 2>&1 || { printf '%s\n' 'rustfmt is unavailable; install the rustfmt component for toolchain ` + toolchain + `' >&2; exit 1; }`)}},
			Cmd:  cargo("fmt", "{@}"),
		},
		"clippy": {
			Help: "Run Rust Clippy checks.",
			Pre:  StageCommands{{Cmd: ScriptCommand(`cargo +` + toolchain + ` clippy --version >/dev/null 2>&1 || { printf '%s\n' 'Clippy is unavailable; install the clippy component for toolchain ` + toolchain + `' >&2; exit 1; }`)}},
			Cmd:  cargo("clippy", "{@}"),
		},
	}
	for _, name := range []string{"check", "test", "build", "clippy"} {
		rec := recipes[name]
		all := rec
		all.Cmd = slices.Insert(slices.Clone(rec.Cmd), 3, "--workspace")
		recipes[name] = withAllPlan(rec, "workspace", RustWorkspaceTargets, all)
	}
	fmtRecipe := recipes["fmt"]
	allFmt := fmtRecipe
	allFmt.Cmd = slices.Insert(slices.Clone(fmtRecipe.Cmd), 3, "--all")
	recipes["fmt"] = withAllPlan(fmtRecipe, "workspace", RustWorkspaceTargets, allFmt)
	recipes["run"] = withUnsupportedAll(recipes["run"], "a Cargo workspace can contain multiple binaries; select one explicitly with --bin")
	return recipes
}
