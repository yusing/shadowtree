package recipe

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

type nodePackage struct {
	Scripts              map[string]string `json:"scripts"`
	PackageManager       string            `json:"packageManager"`
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
	PeerDependencies     map[string]string `json:"peerDependencies"`
	ESLintConfig         json.RawMessage   `json:"eslintConfig"`
	Prettier             json.RawMessage   `json:"prettier"`
}

type nodeProject struct {
	Dir string
	Pkg nodePackage
	PM  string
}

type nodePackageManagerLockfile struct {
	file string
	pm   string
}

// NodePackageManagerInfo is the canonical Node/Bun tooling selection.
type NodePackageManagerInfo struct {
	Name       string
	Version    string
	Identity   string
	Provenance string
	ProjectDir string
	Declared   bool
}

var nodePackageManagerReleaseVersions = map[string]string{
	"npm":  "11.4.2",
	"pnpm": "10.12.1",
	"yarn": "4.9.2",
	"bun":  "1.2.18",
}

const (
	// DefaultNodeRelease is the release-pinned system-image Node default.
	DefaultNodeRelease = "24.4.1"
	// DefaultNodeImage is the canonical slim image for DefaultNodeRelease.
	DefaultNodeImage = "node:" + DefaultNodeRelease + "-bookworm-slim"
)

var exactNodePackageManagerVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)

var frameworkDependencyOrder = []string{"next", "vite", "nuxt", "astro", "@sveltejs/kit"}

const nodeForwardArgs = "{@}"

func nodeBuiltins(opts BuiltinOptions) map[string]Recipe {
	project := loadNodeProject(opts.Dir)
	recipes := map[string]Recipe{}

	addNodeRecipe(recipes, "install", "Install Node dependencies.", project.shellCommand(project.PM+" install {@}"))
	addStandardNodeRecipes(recipes, project)
	addPackageScriptRecipes(recipes, project)
	addNodeCheckRecipe(recipes)
	for name, rec := range recipes {
		recipes[name] = withUnsupportedAll(rec, "Node workspace aggregate semantics are not implemented")
	}
	return recipes
}

func loadNodeProject(dir string) nodeProject {
	if dir == "" {
		dir = "."
	}
	project := nodeProject{Dir: dir, PM: "npm"}
	if packageDir, ok := nearestPackageJSONDir(dir); ok {
		project.Dir = packageDir
		data, err := os.ReadFile(filepath.Join(packageDir, "package.json"))
		if err == nil {
			_ = json.Unmarshal(data, &project.Pkg)
		}
	}
	project.PM = detectNodePackageManager(project.Dir, project.Pkg.PackageManager)
	if project.Pkg.Scripts == nil {
		project.Pkg.Scripts = map[string]string{}
	}
	return project
}

func nearestPackageJSONDir(cwd string) (string, bool) {
	dir, err := filepath.Abs(cwd)
	if err != nil {
		dir = cwd
	}
	for {
		if info, err := os.Stat(filepath.Join(dir, "package.json")); err == nil && !info.IsDir() {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func detectNodePackageManager(dir, packageManager string) string {
	if name, ok := nodePackageManagerName(packageManager); ok {
		return name
	}
	if name, ok := nodePackageManagerFromPackageJSONUpward(filepath.Dir(dir)); ok {
		return name
	}
	if name, ok := nodePackageManagerFromLockfileUpward(dir); ok {
		return name
	}
	return "npm"
}

// NodePackageManager reports the package manager Shadowtree detects from dir.
func NodePackageManager(dir string) string {
	info, err := ResolveNodePackageManager(dir)
	if err != nil {
		return loadNodeProject(dir).PM
	}
	return info.Name
}

// ResolveNodePackageManager returns name, exact version, provenance, and the
// project directory using packageManager, lockfile, then npm precedence.
func ResolveNodePackageManager(dir string) (NodePackageManagerInfo, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return NodePackageManagerInfo{}, err
	}
	boundary := filepath.VolumeName(abs) + string(filepath.Separator)
	return resolveNodePackageManager(abs, boundary)
}

// ResolveNodePackageManagerWithin confines manager discovery to one canonical project.
func ResolveNodePackageManagerWithin(dir, boundary string) (NodePackageManagerInfo, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return NodePackageManagerInfo{}, err
	}
	absBoundary, err := filepath.Abs(boundary)
	if err != nil {
		return NodePackageManagerInfo{}, err
	}
	if rel, err := filepath.Rel(absBoundary, absDir); err != nil || !filepath.IsLocal(rel) {
		return NodePackageManagerInfo{}, fmt.Errorf("node workdir %q is outside canonical project %q", absDir, absBoundary)
	}
	return resolveNodePackageManager(absDir, absBoundary)
}

func resolveNodePackageManager(dir, boundary string) (NodePackageManagerInfo, error) {
	projectDir := dir
	for current := dir; ; current = filepath.Dir(current) {
		if info, err := os.Stat(filepath.Join(current, "package.json")); err == nil && !info.IsDir() {
			projectDir = current
			break
		}
		if current == boundary {
			break
		}
	}
	for current := projectDir; ; current = filepath.Dir(current) {
		path := filepath.Join(current, "package.json")
		data, err := os.ReadFile(path)
		if err == nil {
			var declaration struct {
				PackageManager string `json:"packageManager"`
			}
			if err := json.Unmarshal(data, &declaration); err != nil {
				return NodePackageManagerInfo{}, fmt.Errorf("parse %s: %w", path, err)
			}
			if declaration.PackageManager != "" {
				name, version, ok, err := parseNodePackageManager(declaration.PackageManager)
				if err != nil {
					return NodePackageManagerInfo{}, fmt.Errorf("packageManager in %s: %w", path, err)
				}
				if !ok {
					return NodePackageManagerInfo{}, fmt.Errorf("packageManager in %s uses unsupported manager %q", path, declaration.PackageManager)
				}
				if version == "" {
					version = nodePackageManagerReleaseVersions[name]
				}
				return NodePackageManagerInfo{Name: name, Version: version, Identity: name + "@" + version, Provenance: path + "#packageManager", ProjectDir: current, Declared: true}, nil
			}
		}
		parent := filepath.Dir(current)
		if current == boundary || parent == current {
			break
		}
	}
	lockfiles := []nodePackageManagerLockfile{
		{file: "pnpm-lock.yaml", pm: "pnpm"}, {file: "yarn.lock", pm: "yarn"},
		{file: "bun.lockb", pm: "bun"}, {file: "bun.lock", pm: "bun"},
		{file: "package-lock.json", pm: "npm"}, {file: "npm-shrinkwrap.json", pm: "npm"},
	}
	for current := projectDir; ; current = filepath.Dir(current) {
		for _, lockfile := range lockfiles {
			path := filepath.Join(current, lockfile.file)
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				version := nodePackageManagerReleaseVersions[lockfile.pm]
				return NodePackageManagerInfo{Name: lockfile.pm, Version: version, Identity: lockfile.pm + "@" + version, Provenance: path, ProjectDir: current}, nil
			}
		}
		parent := filepath.Dir(current)
		if current == boundary || parent == current {
			break
		}
	}
	manager := DefaultNodePackageManager()
	manager.ProjectDir = projectDir
	return manager, nil
}

// DefaultNodePackageManager returns the release-pinned npm provider used when
// a system composition needs Node tooling independently of a project's manager.
func DefaultNodePackageManager() NodePackageManagerInfo {
	version := nodePackageManagerReleaseVersions["npm"]
	return NodePackageManagerInfo{Name: "npm", Version: version, Identity: "npm@" + version, Provenance: "shadowtree-default"}
}

func parseNodePackageManager(value string) (name, version string, ok bool, err error) {
	name, version, _ = strings.Cut(value, "@")
	name = strings.ToLower(name)
	if _, supported := nodePackageManagerReleaseVersions[name]; !supported {
		return "", "", false, nil
	}
	if version != "" && !exactNodePackageManagerVersion.MatchString(version) {
		return "", "", false, fmt.Errorf("%s requires an exact version, got %q", name, version)
	}
	return name, version, true, nil
}

// NodeInstallCommandForPackageManager returns package-manager-specific guidance
// for installing a recipe-required Node CLI package.
func NodeInstallCommandForPackageManager(pm, pkg string) string {
	switch pm {
	case "pnpm":
		return "pnpm add --global " + pkg
	case "yarn":
		return "yarn global add " + pkg
	case "bun":
		return "bun add --global " + pkg
	default:
		return "npm install -g " + pkg
	}
}

func nodePackageManagerName(packageManager string) (string, bool) {
	name, _, _ := strings.Cut(strings.ToLower(packageManager), "@")
	switch name {
	case "pnpm", "yarn", "bun", "npm":
		return name, true
	default:
		return "", false
	}
}

func nodePackageManagerFromPackageJSONUpward(cwd string) (string, bool) {
	for dir := cwd; ; dir = filepath.Dir(dir) {
		data, err := os.ReadFile(filepath.Join(dir, "package.json"))
		if err == nil {
			var pkg struct {
				PackageManager string `json:"packageManager"`
			}
			if json.Unmarshal(data, &pkg) == nil {
				if name, ok := nodePackageManagerName(pkg.PackageManager); ok {
					return name, true
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
	}
}

func nodePackageManagerFromLockfileUpward(cwd string) (string, bool) {
	lockfiles := []nodePackageManagerLockfile{
		{file: "pnpm-lock.yaml", pm: "pnpm"},
		{file: "yarn.lock", pm: "yarn"},
		{file: "bun.lockb", pm: "bun"},
		{file: "bun.lock", pm: "bun"},
		{file: "package-lock.json", pm: "npm"},
		{file: "npm-shrinkwrap.json", pm: "npm"},
	}
	for dir := cwd; ; dir = filepath.Dir(dir) {
		var pm string
		if slices.ContainsFunc(lockfiles, func(candidate nodePackageManagerLockfile) bool {
			if info, err := os.Stat(filepath.Join(dir, candidate.file)); err == nil && !info.IsDir() {
				pm = candidate.pm
				return true
			}
			return false
		}) {
			return pm, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
	}
}

func addStandardNodeRecipes(recipes map[string]Recipe, project nodeProject) {
	addScriptOrFrameworkRecipe(recipes, project, "dev", "Run the Node development server.", "dev")
	addScriptOrFrameworkRecipe(recipes, project, "build", "Build the Node project.", "build")
	addScriptOrFrameworkRecipe(recipes, project, "start", "Start or preview the Node project.", "start")
	addNodeTestRecipe(recipes, project)
	addNodeLintRecipe(recipes, project)
	addNodeFmtRecipe(recipes, project)
	addNodeTypecheckRecipe(recipes, project)
}

func addScriptOrFrameworkRecipe(recipes map[string]Recipe, project nodeProject, name, help, script string) {
	if hasNodeScript(project, script) {
		addNodeRecipe(recipes, name, "Run package script "+script+".", project.scriptCommand(script))
		return
	}
	if command, ok := frameworkCommand(project, name); ok {
		addNodeRecipe(recipes, name, help, project.toolCommand(command[0], command[1:]...))
	}
}

func addNodeTestRecipe(recipes map[string]Recipe, project nodeProject) {
	switch {
	case hasNodeScript(project, "test"):
		addNodeRecipe(recipes, "test", "Run package script test.", project.scriptCommand("test"))
	case project.PM == "bun":
		if project.hasDependency("vitest") {
			addNodeRecipe(recipes, "test", "Run Vitest.", project.toolCommand("vitest"))
			return
		}
		addNodeRecipe(recipes, "test", "Run Bun tests.", project.shellCommand("bun test {@}"))
	case project.hasDependency("vitest"):
		addNodeRecipe(recipes, "test", "Run Vitest.", project.toolCommand("vitest"))
	case project.hasDependency("jest"):
		addNodeRecipe(recipes, "test", "Run Jest.", project.toolCommand("jest"))
	case project.hasDependency("@playwright/test"):
		addNodeRecipe(recipes, "test", "Run Playwright tests.", project.toolCommand("playwright", "test"))
	}
}

func addNodeLintRecipe(recipes map[string]Recipe, project nodeProject) {
	switch {
	case hasNodeScript(project, "lint"):
		addNodeRecipe(recipes, "lint", "Run package script lint.", project.scriptCommand("lint"))
	case project.hasESLint():
		addNodeRecipe(recipes, "lint", "Run ESLint.", project.toolCommand("eslint", "."))
	case project.hasOxlint():
		addNodeRecipe(recipes, "lint", "Run Oxlint.", project.toolCommand("oxlint"))
	case project.hasBiome():
		addNodeRecipe(recipes, "lint", "Run Biome lint.", project.toolCommand("biome", "lint", "."))
	}
}

func addNodeFmtRecipe(recipes map[string]Recipe, project nodeProject) {
	switch {
	case hasNodeScript(project, "fmt"):
		addNodeRecipe(recipes, "fmt", "Run package script fmt.", project.scriptCommand("fmt"))
	case hasNodeScript(project, "format"):
		addNodeRecipe(recipes, "fmt", "Run package script format.", project.scriptCommand("format"))
	case project.hasPrettier():
		addNodeRecipe(recipes, "fmt", "Run Prettier.", project.toolCommand("prettier", "--write", "."))
	case project.hasOxfmt():
		addNodeRecipe(recipes, "fmt", "Run Oxfmt.", project.toolCommand("oxfmt"))
	case project.hasBiome():
		addNodeRecipe(recipes, "fmt", "Run Biome format.", project.toolCommand("biome", "format", "--write", "."))
	}
}

func addNodeTypecheckRecipe(recipes map[string]Recipe, project nodeProject) {
	switch {
	case hasNodeScript(project, "typecheck"):
		addNodeRecipe(recipes, "typecheck", "Run package script typecheck.", project.scriptCommand("typecheck"))
	case hasNodeScript(project, "type-check"):
		addNodeRecipe(recipes, "typecheck", "Run package script type-check.", project.scriptCommand("type-check"))
	default:
		var commands []string
		if project.hasDependency("vue-tsc") {
			commands = append(commands, project.toolInvocation("vue-tsc", "--noEmit"))
		}
		if project.hasDependency("svelte-check") {
			commands = append(commands, project.toolInvocation("svelte-check"))
		}
		if project.hasDependency("typescript") || project.hasFile("tsconfig.json") {
			commands = append(commands, project.toolInvocation("tsc", "--noEmit"))
		}
		if len(commands) > 0 {
			addNodeRecipe(recipes, "typecheck", "Run Node type checks.", project.shellCommand("set -e\n"+strings.Join(commands, "\n")))
		}
	}
}

func addNodeCheckRecipe(recipes map[string]Recipe) {
	var available []string
	for _, name := range []string{"lint", "typecheck", "test"} {
		if _, ok := recipes[name]; ok {
			available = append(available, name)
		}
	}
	if len(available) == 0 {
		return
	}
	pre := make([]StageCommand, 0, len(available)-1)
	for _, name := range available[:len(available)-1] {
		pre = append(pre, StageCommand{Cmd: Command{"@" + name}})
	}
	addNodeRecipe(recipes, "check", "Run Node checks.", Command{"@" + available[len(available)-1]})
	rec := recipes["check"]
	rec.Pre = pre
	recipes["check"] = rec
}

func addPackageScriptRecipes(recipes map[string]Recipe, project nodeProject) {
	chosen := map[string]string{}
	for script := range project.Pkg.Scripts {
		name := normalizePackageScriptName(script)
		if name == "" || IsReservedRecipeName(name) {
			continue
		}
		existing, ok := chosen[name]
		switch {
		case !ok:
			chosen[name] = script
		case existing == name:
			continue
		case script == name || script < existing:
			chosen[name] = script
		}
	}
	for _, name := range slices.Sorted(maps.Keys(chosen)) {
		if _, exists := recipes[name]; exists {
			continue
		}
		script := chosen[name]
		addNodeRecipe(recipes, name, "Run package script "+script+".", project.scriptCommand(script))
	}
}

func addNodeRecipe(recipes map[string]Recipe, name, help string, cmd Command) {
	recipes[name] = Recipe{
		Help:      help,
		Cmd:       cmd,
		Sandboxed: new(SandboxModeHost),
	}
}

func hasNodeScript(project nodeProject, name string) bool {
	_, ok := project.Pkg.Scripts[name]
	return ok
}

func frameworkCommand(project nodeProject, recipeName string) ([]string, bool) {
	for _, dependency := range frameworkDependencyOrder {
		if !project.hasDependency(dependency) {
			continue
		}
		return frameworkRecipeCommand(dependency, recipeName)
	}
	return nil, false
}

func frameworkRecipeCommand(dependency, recipeName string) ([]string, bool) {
	switch dependency {
	case "next":
		switch recipeName {
		case "dev":
			return []string{"next", "dev"}, true
		case "build":
			return []string{"next", "build"}, true
		case "start":
			return []string{"next", "start"}, true
		}
	case "vite", "@sveltejs/kit":
		switch recipeName {
		case "dev":
			return []string{"vite"}, true
		case "build":
			return []string{"vite", "build"}, true
		case "start":
			return []string{"vite", "preview"}, true
		}
	case "nuxt":
		switch recipeName {
		case "dev":
			return []string{"nuxt", "dev"}, true
		case "build":
			return []string{"nuxt", "build"}, true
		case "start":
			return []string{"nuxt", "preview"}, true
		}
	case "astro":
		switch recipeName {
		case "dev":
			return []string{"astro", "dev"}, true
		case "build":
			return []string{"astro", "build"}, true
		case "start":
			return []string{"astro", "preview"}, true
		}
	}
	return nil, false
}

func (project nodeProject) scriptCommand(script string) Command {
	return project.shellCommand(project.PM + " run " + shellQuote(script) + " -- {@}")
}

func (project nodeProject) toolCommand(bin string, args ...string) Command {
	return project.shellCommand(project.toolInvocation(bin, args...))
}

func (project nodeProject) toolInvocation(bin string, args ...string) string {
	var parts []string
	switch project.PM {
	case "pnpm":
		parts = []string{"pnpm", "exec", bin}
	case "yarn":
		parts = []string{"yarn", "exec", bin}
	case "bun":
		parts = []string{"bunx", bin}
	default:
		parts = []string{"npm", "exec", "--", bin}
	}
	parts = append(parts, args...)
	parts = append(parts, nodeForwardArgs)
	return shellWords(parts)
}

func (project nodeProject) shellCommand(command string) Command {
	return ScriptCommand("cd " + shellQuote(project.Dir) + "\n" + command)
}

func (project nodeProject) hasDependency(name string) bool {
	if _, ok := project.Pkg.Dependencies[name]; ok {
		return true
	}
	if _, ok := project.Pkg.DevDependencies[name]; ok {
		return true
	}
	if _, ok := project.Pkg.OptionalDependencies[name]; ok {
		return true
	}
	if _, ok := project.Pkg.PeerDependencies[name]; ok {
		return true
	}
	return false
}

func (project nodeProject) hasESLint() bool {
	return project.hasDependency("eslint") ||
		project.hasAnyFile("eslint.config.js", "eslint.config.mjs", "eslint.config.cjs", "eslint.config.ts", "eslint.config.mts", "eslint.config.cts") ||
		project.hasGlob(".eslintrc*") ||
		len(project.Pkg.ESLintConfig) > 0
}

func (project nodeProject) hasOxlint() bool {
	return project.hasDependency("oxlint") ||
		project.hasAnyFile("oxlint.config.js", "oxlint.config.mjs", "oxlint.config.cjs", "oxlint.config.ts", "oxlint.config.mts", "oxlint.config.cts", ".oxlintrc.json", ".oxlintrc.jsonc")
}

func (project nodeProject) hasBiome() bool {
	return project.hasDependency("@biomejs/biome") || project.hasAnyFile("biome.json", "biome.jsonc")
}

func (project nodeProject) hasPrettier() bool {
	return project.hasDependency("prettier") ||
		project.hasAnyFile("prettier.config.js", "prettier.config.mjs", "prettier.config.cjs", "prettier.config.ts", "prettier.config.mts", "prettier.config.cts") ||
		project.hasGlob(".prettierrc*") ||
		len(project.Pkg.Prettier) > 0
}

func (project nodeProject) hasOxfmt() bool {
	return project.hasDependency("oxfmt") ||
		project.hasAnyFile("oxfmt.config.js", "oxfmt.config.mjs", "oxfmt.config.cjs", "oxfmt.config.ts", "oxfmt.config.mts", "oxfmt.config.cts", ".oxfmtrc.json", ".oxfmtrc.jsonc")
}

func (project nodeProject) hasAnyFile(names ...string) bool {
	return slices.ContainsFunc(names, project.hasFile)
}

func (project nodeProject) hasFile(name string) bool {
	info, err := os.Stat(filepath.Join(project.Dir, name))
	return err == nil && !info.IsDir()
}

func (project nodeProject) hasGlob(pattern string) bool {
	matches, err := filepath.Glob(filepath.Join(project.Dir, pattern))
	if err != nil {
		return false
	}
	for _, match := range matches {
		info, err := os.Stat(match)
		if err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

func normalizePackageScriptName(script string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range script {
		valid := r == '.' || r == '_' || r == '-' || isASCIILetter(r) || isASCIIDigit(r)
		if valid && r != '-' {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func isASCIILetter(r rune) bool {
	return 'A' <= r && r <= 'Z' || 'a' <= r && r <= 'z'
}

func isASCIIDigit(r rune) bool {
	return '0' <= r && r <= '9'
}

func shellWords(words []string) string {
	quoted := make([]string, 0, len(words))
	for _, word := range words {
		if word == nodeForwardArgs {
			quoted = append(quoted, word)
			continue
		}
		quoted = append(quoted, shellQuote(word))
	}
	return strings.Join(quoted, " ")
}

func shellQuote(value string) string {
	quoted, err := syntax.Quote(value, syntax.LangPOSIX)
	if err == nil {
		return quoted
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
