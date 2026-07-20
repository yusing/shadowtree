package systemsandbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/yusing/shadowtree/internal/recipe"
)

// ImagePlan is the complete inspectable immutable-image plan for one recipe.
type ImagePlan struct {
	BaseImage       string
	Platform        string
	Stages          []ImageStage
	FinalTag        string
	DependencySeeds []DependencySeed
	Caches          []CachePlan
	Toolchains      []ResolvedToolchain
	ToolchainKey    string
	Dependencies    []DependencyPlan
}

// DependencySeed describes image-owned Node/Bun dependency state copied into
// the private workspace before the canonical project bind is used.
type DependencySeed struct {
	SourcePath string
	TargetPath string
	Provider   string
	Origin     string
}

// ImageStage owns exactly one immutable image transformation.
type ImageStage struct {
	Name          string
	Platform      string
	Key           string
	ParentKey     string
	Tag           string
	Labels        map[string]string
	Containerfile string
	Context       map[string][]byte
	ContextHashes map[string]string
	Metadata      map[string]string
}

// BuildImages reuses label-validated stages and builds each missing stage from
// the nearest valid lower stage before publishing the recipe-scoped alias.
func BuildImages(ctx context.Context, runtime RuntimeName, plan ImagePlan, progress io.Writer) error {
	return buildImagesWith(ctx, runtime, plan, progress, directCommand, directStreamingCommand)
}

func buildImagesWith(ctx context.Context, runtime RuntimeName, plan ImagePlan, progress io.Writer, run commandRunner, stream streamingCommandRunner) error {
	if progress == nil {
		progress = io.Discard
	}
	for _, stage := range plan.Stages {
		fmt.Fprintf(progress, "shadowtree: image stage %s lookup\n", stage.Name)
		labels, exists, err := inspectImageLabels(ctx, runtime, stage.Tag, run)
		if err != nil {
			return fmt.Errorf("runtime %s stage %s lookup: %w", runtime, stage.Name, err)
		}
		if exists {
			if !labelsMatch(labels, stage.Labels) {
				return fmt.Errorf("runtime %s stage %s tag collision at %s; remove or retag the conflicting image", runtime, stage.Name, stage.Tag)
			}
			fmt.Fprintf(progress, "shadowtree: image stage %s reused\n", stage.Name)
			continue
		}
		fmt.Fprintf(progress, "shadowtree: image stage %s build\n", stage.Name)
		if err := buildStage(ctx, runtime, stage, progress, stream); err != nil {
			return fmt.Errorf("runtime %s stage %s build: %w", runtime, stage.Name, err)
		}
		labels, exists, err = inspectImageLabels(ctx, runtime, stage.Tag, run)
		if err != nil || !exists || !labelsMatch(labels, stage.Labels) {
			return fmt.Errorf("runtime %s stage %s build did not publish the requested labelled image", runtime, stage.Name)
		}
	}
	if len(plan.Stages) == 0 {
		return errors.New("image plan has no stages")
	}
	labels, exists, err := inspectImageLabels(ctx, runtime, plan.FinalTag, run)
	if err != nil {
		return fmt.Errorf("runtime %s final image lookup: %w", runtime, err)
	}
	want := plan.Stages[len(plan.Stages)-1].Labels
	if exists {
		if !labelsMatch(labels, want) {
			return fmt.Errorf("runtime %s final tag collision at %s; remove or retag the conflicting image", runtime, plan.FinalTag)
		}
		return nil
	}
	fmt.Fprintln(progress, "shadowtree: publishing recipe image alias")
	output, err := run(ctx, string(runtime), "image", "tag", plan.Stages[len(plan.Stages)-1].Tag, plan.FinalTag)
	if err != nil {
		return fmt.Errorf("runtime %s publish final tag: %s", runtime, commandFailure(err, output))
	}
	labels, exists, err = inspectImageLabels(ctx, runtime, plan.FinalTag, run)
	if err != nil || !exists || !labelsMatch(labels, want) {
		return fmt.Errorf("runtime %s final tag %s did not resolve to the requested dependency image", runtime, plan.FinalTag)
	}
	return nil
}

func inspectImageLabels(ctx context.Context, runtime RuntimeName, tag string, run commandRunner) (map[string]string, bool, error) {
	output, err := run(ctx, string(runtime), "image", "inspect", "--format", "{{json .Config.Labels}}", tag)
	if err != nil {
		message := strings.ToLower(string(output))
		if strings.Contains(message, "no such image") || strings.Contains(message, "image not known") || strings.Contains(message, "image does not exist") || strings.Contains(message, "reference does not exist") {
			return nil, false, nil
		}
		return nil, false, errors.New(commandFailure(err, output))
	}
	labels := map[string]string{}
	if err := json.Unmarshal(bytes.TrimSpace(output), &labels); err != nil {
		return nil, false, fmt.Errorf("parse image labels: %w", err)
	}
	return labels, true, nil
}

func labelsMatch(got, want map[string]string) bool {
	for key, value := range want {
		if got[key] != value {
			return false
		}
	}
	return true
}

func buildStage(ctx context.Context, runtime RuntimeName, stage ImageStage, progress io.Writer, run streamingCommandRunner) error {
	dir, err := os.MkdirTemp("", "shadowtree-image-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "Containerfile"), []byte(stage.Containerfile), 0o600); err != nil {
		return err
	}
	for name, data := range stage.Context {
		if !filepath.IsLocal(name) {
			return fmt.Errorf("unsafe context path %q", name)
		}
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return err
		}
	}
	args := []string{"build", "--platform", stage.Platform, "--file", filepath.Join(dir, "Containerfile"), "--tag", stage.Tag}
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "no_proxy", "all_proxy"} {
		args = append(args, "--build-arg", name+"=")
	}
	for _, key := range slices.Sorted(maps.Keys(stage.Labels)) {
		args = append(args, "--label", key+"="+stage.Labels[key])
	}
	args = append(args, dir)
	output, err := run(ctx, progress, string(runtime), args...)
	if err != nil {
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		message := strings.TrimSpace(string(output))
		if message == "" {
			return err
		}
		return fmt.Errorf("runtime output: %s: %w", strings.Join(strings.Fields(message), " "), err)
	}
	return nil
}

type stageInput struct {
	name     string
	commands []string
	context  map[string][]byte
	metadata map[string]string
}

type dependencyInput struct {
	commands []string
	context  map[string][]byte
	metadata map[string]string
	seed     *DependencySeed
}

type contributionInput struct {
	dependency dependencyInput
	caches     []cacheDescriptor
}

func contributionInputs(resolved recipe.Resolved, source, dir string) (contributionInput, error) {
	workdir, err := relativeImagePath(source, dir)
	if err != nil {
		return contributionInput{}, err
	}
	switch resolved.Profile {
	case recipe.GoProfile:
		toolchain, err := recipe.ResolveGoToolchain(dir, source)
		if err != nil {
			return contributionInput{}, err
		}
		files, err := manifestContext(source, goManifest)
		if err != nil {
			return contributionInput{}, err
		}
		if err := filterGoManifestContext(files, workdir); err != nil {
			return contributionInput{}, err
		}
		lockfile := nearestContextFile(files, workdir, "go.sum")
		metadata := map[string]string{"toolchain": toolchain.Version, "toolchain_provenance": relativeProvenance(source, toolchain.Provenance), "workdir": workdir}
		workspace := nearestContextFile(files, workdir, "go.work")
		if workspace == "" {
			workspace = nearestContextFile(files, workdir, "go.mod")
		}
		workspace = path.Dir(workspace)
		if workspace == "." {
			workspace = ""
		}
		cache := cacheDescriptor{
			provider: "go-build", format: "go-build-v1", workspace: workspace,
			mountPath: "/opt/shadowtree/cache/go-build", toolchain: toolchain.Version,
			concurrency: "shared", environment: map[string]string{"GOCACHE": "/opt/shadowtree/cache/go-build"},
		}
		return contributionInput{dependency: dependencyInput{commands: lockedCommand(lockfile, workdir, "go mod download"), context: files, metadata: metadata}, caches: []cacheDescriptor{cache}}, nil
	case recipe.NodeProfile:
		manager, err := recipe.ResolveNodePackageManagerWithin(dir, source)
		if err != nil {
			return contributionInput{}, err
		}
		managerDir, err := relativeImagePath(source, manager.ProjectDir)
		if err != nil {
			return contributionInput{}, fmt.Errorf("package-manager project path: %w", err)
		}
		files, err := manifestContext(source, nodeManifest)
		if err != nil {
			return contributionInput{}, err
		}
		if err := filterNodeManifestContext(files, managerDir, workdir); err != nil {
			return contributionInput{}, err
		}
		if err := rejectSecretManifestInputs(files); err != nil {
			return contributionInput{}, err
		}
		if err := rejectUnsafeNodeDependencies(files); err != nil {
			return contributionInput{}, err
		}
		metadata := map[string]string{"manager": manager.Name, "manager_version": manager.Version, "manager_identity": manager.Identity, "manager_provenance": relativeProvenance(source, manager.Provenance), "workdir": managerDir}
		commands := nodeLockedCommand(manager.Name, managerDir, files)
		var seed *DependencySeed
		if len(commands) > 0 {
			seed = &DependencySeed{SourcePath: "/opt/shadowtree/dependencies", TargetPath: ".", Provider: manager.Name}
		}
		return contributionInput{dependency: dependencyInput{commands: commands, context: files, metadata: metadata, seed: seed}}, nil
	case recipe.RustProfile:
		toolchain, err := recipe.RustToolchainWithin(dir, source)
		if err != nil {
			return contributionInput{}, err
		}
		release, host, _ := strings.Cut(toolchain, "-")
		if host != "" {
			if _, err := rustHostPlatform(host); err != nil {
				return contributionInput{}, err
			}
		}
		files, err := manifestContext(source, rustManifest)
		if err != nil {
			return contributionInput{}, err
		}
		if err := filterRustManifestContext(files, workdir); err != nil {
			return contributionInput{}, err
		}
		if err := rejectSecretManifestInputs(files); err != nil {
			return contributionInput{}, err
		}
		if err := rejectUnsafeCargoSources(files); err != nil {
			return contributionInput{}, err
		}
		if err := addRustTargetPlaceholders(files); err != nil {
			return contributionInput{}, err
		}
		lockfile := nearestContextFile(files, workdir, "Cargo.lock")
		manifests, err := recipe.RustDependencyManifestPaths(files, workdir)
		if err != nil {
			return contributionInput{}, err
		}
		workspace := rustWorkspacePath(files, manifests, workdir)
		environment := os.Getenv("CARGO_BUILD_TARGET")
		if target := resolved.GlobalEnv["CARGO_BUILD_TARGET"]; target != "" {
			environment = target
		}
		if target := resolved.Recipe.Env["CARGO_BUILD_TARGET"]; target != "" {
			environment = target
		}
		target, err := rustCacheTarget(files, workdir, resolved.Main, resolved.VariadicArgs, environment, host)
		if err != nil {
			return contributionInput{}, err
		}
		metadata := map[string]string{"toolchain": toolchain, "toolchain_release": release, "toolchain_host": host, "workdir": workdir}
		outputPath := filepath.Join(source, filepath.FromSlash(workspace), "target")
		cache := cacheDescriptor{
			provider: "cargo-target", format: "cargo-target-v1", workspace: workspace,
			mountPath: "/opt/shadowtree/cache/cargo-target", outputPath: outputPath,
			toolchain: toolchain, concurrency: recipe.RustTargetCacheConcurrency,
			environment: map[string]string{"CARGO_TARGET_DIR": "/opt/shadowtree/cache/cargo-target"},
			inputs:      map[string]string{"host": host, "target": target},
		}
		return contributionInput{dependency: dependencyInput{commands: lockedCommand(lockfile, workdir, "cargo fetch --locked"), context: files, metadata: metadata}, caches: []cacheDescriptor{cache}}, nil
	default:
		return contributionInput{}, nil
	}
}

func lockedCommand(lockfile, workdir, command string) []string {
	if lockfile == "" {
		return nil
	}
	return []string{"COPY . /opt/shadowtree/dependencies", "WORKDIR " + containerDependencyPath(workdir), "RUN " + command}
}

func nodeLockedCommand(manager, workdir string, files map[string][]byte) []string {
	lockfile, command := "", ""
	switch manager {
	case "pnpm":
		lockfile, command = nearestContextFile(files, workdir, "pnpm-lock.yaml"), "pnpm install --frozen-lockfile --ignore-scripts --store-dir .shadowtree-pnpm-store"
	case "yarn":
		lockfile, command = nearestContextFile(files, workdir, "yarn.lock"), "YARN_CACHE_FOLDER=.shadowtree-yarn-cache yarn install --immutable --mode=skip-builds"
	case "bun":
		lockfile = nearestContextFile(files, workdir, "bun.lock")
		if lockfile == "" {
			lockfile = nearestContextFile(files, workdir, "bun.lockb")
		}
		command = "BUN_INSTALL_CACHE_DIR=.shadowtree-bun-cache bun install --frozen-lockfile --ignore-scripts"
	default:
		lockfile = nearestContextFile(files, workdir, "package-lock.json")
		if lockfile == "" {
			lockfile = nearestContextFile(files, workdir, "npm-shrinkwrap.json")
		}
		command = "npm_config_cache=.shadowtree-npm-cache npm ci --ignore-scripts"
	}
	return lockedCommand(lockfile, workdir, command)
}

type manifestSelector func(path, name string) bool

func goManifest(_ string, name string) bool {
	return slices.Contains([]string{"go.mod", "go.sum", "go.work", "go.work.sum"}, name)
}

func filterGoManifestContext(files map[string][]byte, workdir string) error {
	workspace := nearestContextFile(files, workdir, "go.work")
	allowedDirs := map[string]bool{}
	if workspace != "" {
		root := path.Dir(workspace)
		if root == "." {
			root = ""
		}
		allowedDirs[root] = true
		for _, local := range localGoModulePaths(files[workspace]) {
			dir := path.Clean(path.Join(root, local))
			if !filepath.IsLocal(filepath.FromSlash(dir)) {
				return fmt.Errorf("Go workspace %s local module %q escapes the canonical project", workspace, local)
			}
			allowedDirs[dir] = true
		}
	} else {
		manifest := nearestContextFile(files, workdir, "go.mod")
		if manifest == "" {
			return nil
		}
		dir := path.Dir(manifest)
		if dir == "." {
			dir = ""
		}
		allowedDirs[dir] = true
	}
	for changed := true; changed; {
		changed = false
		for dir := range maps.Clone(allowedDirs) {
			for _, local := range localGoModulePaths(files[slashJoin(dir, "go.mod")]) {
				target := path.Clean(path.Join(dir, local))
				if !filepath.IsLocal(filepath.FromSlash(target)) {
					return fmt.Errorf("Go module %s local replacement %q escapes the canonical project", slashJoin(dir, "go.mod"), local)
				}
				if !allowedDirs[target] {
					allowedDirs[target] = true
					changed = true
				}
			}
		}
	}
	for file := range files {
		dir := path.Dir(file)
		if dir == "." {
			dir = ""
		}
		if !allowedDirs[dir] {
			delete(files, file)
		}
	}
	return nil
}

func localGoModulePaths(data []byte) []string {
	var paths []string
	for line := range strings.Lines(string(data)) {
		line, _, _ = strings.Cut(line, "//")
		for _, field := range strings.Fields(line) {
			field = strings.Trim(field, "()'\"")
			if strings.HasPrefix(field, "./") || strings.HasPrefix(field, "../") {
				paths = append(paths, field)
			}
		}
	}
	return paths
}

func nodeManifest(path, name string) bool {
	if name == ".pnpmfile.cjs" || strings.Contains(path, "/.yarn/plugins/") || strings.HasPrefix(path, ".yarn/plugins/") || strings.Contains(path, "/.yarn/releases/") || strings.HasPrefix(path, ".yarn/releases/") {
		return true
	}
	if slices.Contains([]string{"package.json", "package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "pnpm-workspace.yaml", "yarn.lock", ".yarnrc.yml", ".npmrc", "bun.lock", "bun.lockb"}, name) {
		return true
	}
	return (strings.HasSuffix(name, ".patch") || strings.HasSuffix(name, ".diff")) && (strings.Contains(path, "/patches/") || strings.HasPrefix(path, "patches/"))
}

func rustManifest(path, name string) bool {
	if slices.Contains([]string{"Cargo.toml", "Cargo.lock", "rust-toolchain", "rust-toolchain.toml"}, name) {
		return true
	}
	return (name == "config" || name == "config.toml") && (strings.Contains(path, "/.cargo/") || strings.HasPrefix(path, ".cargo/"))
}

func filterRustManifestContext(files map[string][]byte, workdir string) error {
	manifests, err := recipe.RustDependencyManifestPaths(files, workdir)
	if err != nil {
		return err
	}
	allowedManifests := map[string]bool{}
	allowedDirs := map[string]bool{}
	for _, manifest := range manifests {
		allowedManifests[manifest] = true
		dir := path.Dir(manifest)
		if dir == "." {
			dir = ""
		}
		allowedDirs[dir] = true
	}
	for file := range files {
		name := path.Base(file)
		dir := path.Dir(file)
		if dir == "." {
			dir = ""
		}
		switch {
		case name == "Cargo.toml":
			if !allowedManifests[file] {
				delete(files, file)
			}
		case name == "Cargo.lock":
			if !allowedDirs[dir] {
				delete(files, file)
			}
		case name == recipe.RustToolchainFile || name == recipe.RustToolchainTOML:
			if !slashAncestor(dir, workdir) {
				delete(files, file)
			}
		case name == "config" || name == "config.toml":
			cargoDir := path.Dir(dir)
			if cargoDir == "." {
				cargoDir = ""
			}
			if path.Base(dir) != ".cargo" || !slashAncestor(cargoDir, workdir) {
				delete(files, file)
			}
		}
	}
	return nil
}

func rustWorkspacePath(files map[string][]byte, manifests []string, workdir string) string {
	selected := nearestContextFile(files, workdir, "Cargo.toml")
	root := selected
	for _, manifest := range manifests {
		dir := path.Dir(manifest)
		if dir == "." {
			dir = ""
		}
		selectedDir := path.Dir(selected)
		if selectedDir == "." {
			selectedDir = ""
		}
		if !slashAncestor(dir, selectedDir) {
			continue
		}
		var marker struct {
			Workspace *struct{} `toml:"workspace"`
		}
		if _, err := toml.Decode(string(files[manifest]), &marker); err == nil && marker.Workspace != nil {
			root = manifest
		}
	}
	dir := path.Dir(root)
	if dir == "." {
		return ""
	}
	return dir
}

func rustCacheTarget(files map[string][]byte, workdir string, command, variadicArgs []string, environment, host string) (string, error) {
	if len(command) > 0 && filepath.Base(command[0]) == "cargo" && !recipe.IsScriptCommand(command) {
		args := append(slices.Clone(command[1:]), variadicArgs...)
		for index, arg := range args {
			if target, ok := strings.CutPrefix(arg, "--target="); ok && target != "" {
				if strings.HasPrefix(target, "__shadowtree_missing_argument_") {
					return "", errors.New("cache identity requires the omitted Cargo --target argument")
				}
				return target, nil
			}
			if arg == "--target" && index+1 < len(args) {
				target := args[index+1]
				if strings.HasPrefix(target, "__shadowtree_missing_argument_") {
					return "", errors.New("cache identity requires the omitted Cargo --target argument")
				}
				return target, nil
			}
		}
	}
	if environment != "" {
		return environment, nil
	}
	for current := workdir; ; {
		for _, name := range []string{".cargo/config.toml", ".cargo/config"} {
			candidate := path.Join(current, name)
			if current == "" {
				candidate = name
			}
			if data := files[candidate]; data != nil {
				var config struct {
					Build struct {
						Target string `toml:"target"`
					} `toml:"build"`
				}
				if _, err := toml.Decode(string(data), &config); err == nil && config.Build.Target != "" {
					return config.Build.Target, nil
				}
			}
		}
		if current == "" {
			break
		}
		current = path.Dir(current)
		if current == "." {
			current = ""
		}
	}
	return host, nil
}

func slashAncestor(ancestor, candidate string) bool {
	return ancestor == "" || candidate == ancestor || strings.HasPrefix(candidate, ancestor+"/")
}

func manifestContext(root string, selectFile manifestSelector) (map[string][]byte, error) {
	const (
		maxFileBytes = 8 << 20
		maxTotal     = 32 << 20
	)
	files := map[string][]byte{}
	total := int64(0)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root && slices.Contains([]string{".git", "node_modules", "target", "vendor"}, entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !selectFile(rel, entry.Name()) {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("dependency manifest %s is a symlink", rel)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("dependency manifest %s is not a regular file", rel)
		}
		if info.Size() > maxFileBytes || total+info.Size() > maxTotal {
			return fmt.Errorf("dependency manifest context exceeds safe size limit at %s", rel)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[rel] = data
		total += int64(len(data))
		return nil
	})
	return files, err
}

func rejectSecretManifestInputs(files map[string][]byte) error {
	for path, data := range files {
		lowerPath := strings.ToLower(path)
		if containsURLUserInfo(string(data)) {
			return fmt.Errorf("embedded URL credentials in %s require a proven non-persistent secret transport", path)
		}
		if strings.HasSuffix(lowerPath, "/.pnpmfile.cjs") || lowerPath == ".pnpmfile.cjs" || strings.Contains(lowerPath, "/.yarn/plugins/") || strings.Contains(lowerPath, "/.yarn/releases/") {
			return fmt.Errorf("unsupported executable package-manager configuration %s", path)
		}
		if !(strings.HasSuffix(lowerPath, ".npmrc") || strings.HasSuffix(lowerPath, ".yarnrc.yml") || strings.Contains(lowerPath, "/.cargo/config") || strings.HasPrefix(lowerPath, ".cargo/config")) {
			continue
		}
		content := strings.ToLower(string(data))
		for _, marker := range []string{"_auth", "authtoken", "auth-token", "password", "credential", "private-key", "identityfile", "token =", "token="} {
			if strings.Contains(content, marker) {
				return fmt.Errorf("private credential material in %s requires a proven non-persistent secret transport", path)
			}
		}
	}
	return nil
}

func containsURLUserInfo(content string) bool {
	for remainder := content; ; {
		_, after, ok := strings.Cut(remainder, "://")
		if !ok {
			return false
		}
		authority := after
		if end := strings.IndexAny(authority, "/?# \t\r\n\"'"); end >= 0 {
			authority = authority[:end]
		}
		if strings.Contains(authority, "@") {
			return true
		}
		remainder = after
	}
}

func rejectUnsafeNodeDependencies(files map[string][]byte) error {
	for path, data := range files {
		if filepath.Base(path) != "package.json" {
			continue
		}
		var manifest struct {
			Dependencies         map[string]string `json:"dependencies"`
			DevDependencies      map[string]string `json:"devDependencies"`
			OptionalDependencies map[string]string `json:"optionalDependencies"`
		}
		if err := json.Unmarshal(data, &manifest); err != nil {
			return fmt.Errorf("parse dependency manifest %s: %w", path, err)
		}
		for section, dependencies := range map[string]map[string]string{"dependencies": manifest.Dependencies, "devDependencies": manifest.DevDependencies, "optionalDependencies": manifest.OptionalDependencies} {
			for name, value := range dependencies {
				lower := strings.ToLower(value)
				if strings.HasPrefix(lower, "file:") || strings.HasPrefix(lower, "link:") || strings.HasPrefix(lower, "portal:") {
					return fmt.Errorf("dependency manifest %s %s.%s uses local source %q; automatic image preparation cannot safely pre-key ordinary source", path, section, name, value)
				}
			}
		}
	}
	return nil
}

func filterNodeManifestContext(files map[string][]byte, projectDir, workdir string) error {
	allowedDirs := map[string]bool{projectDir: true}
	for current := workdir; current != ""; current = slashParent(current) {
		if !slashWithin(projectDir, current) {
			break
		}
		allowedDirs[current] = true
		if current == projectDir {
			break
		}
	}
	patterns, err := nodeWorkspacePatterns(files[slashJoin(projectDir, "package.json")])
	if err != nil {
		return fmt.Errorf("parse Node workspace declaration: %w", err)
	}
	patterns = append(patterns, pnpmWorkspacePatterns(files[slashJoin(projectDir, "pnpm-workspace.yaml")])...)
	for file := range files {
		if path.Base(file) != "package.json" || !slashWithin(projectDir, file) {
			continue
		}
		dir := path.Dir(file)
		if dir == "." {
			dir = ""
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(dir, projectDir), "/")
		if matchesWorkspacePattern(rel, patterns) {
			allowedDirs[dir] = true
		}
	}
	for file := range files {
		if !slashWithin(projectDir, file) {
			delete(files, file)
			continue
		}
		dir := path.Dir(file)
		if dir == "." {
			dir = ""
		}
		if allowedDirs[dir] || strings.HasPrefix(file, slashJoin(projectDir, ".yarn/")) {
			continue
		}
		delete(files, file)
	}
	return nil
}

func nodeWorkspacePatterns(data []byte) ([]string, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var manifest struct {
		Workspaces json.RawMessage `json:"workspaces"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}
	if len(manifest.Workspaces) == 0 {
		return nil, nil
	}
	var patterns []string
	if err := json.Unmarshal(manifest.Workspaces, &patterns); err == nil {
		return patterns, nil
	}
	var object struct {
		Packages []string `json:"packages"`
	}
	if err := json.Unmarshal(manifest.Workspaces, &object); err != nil {
		return nil, err
	}
	return object.Packages, nil
}

func pnpmWorkspacePatterns(data []byte) []string {
	var patterns []string
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "-") {
			continue
		}
		value := strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "-")), "'\"")
		if value != "" {
			patterns = append(patterns, value)
		}
	}
	return patterns
}

func matchesWorkspacePattern(dir string, patterns []string) bool {
	matched := false
	for _, pattern := range patterns {
		exclude := strings.HasPrefix(pattern, "!")
		pattern = strings.TrimPrefix(pattern, "!")
		ok, _ := path.Match(pattern, dir)
		if !ok && strings.HasSuffix(pattern, "/**") {
			ok = dir == strings.TrimSuffix(pattern, "/**") || strings.HasPrefix(dir, strings.TrimSuffix(pattern, "**"))
		}
		if ok {
			matched = !exclude
		}
	}
	return matched
}

func slashWithin(root, candidate string) bool {
	if root == "" {
		return candidate == "" || !strings.HasPrefix(candidate, "../")
	}
	return candidate == root || strings.HasPrefix(candidate, root+"/")
}

func slashJoin(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

func slashParent(dir string) string {
	parent := path.Dir(dir)
	if parent == "." || parent == dir {
		return ""
	}
	return parent
}

func rejectUnsafeCargoSources(files map[string][]byte) error {
	for path, data := range files {
		lowerPath := strings.ToLower(path)
		if !(strings.Contains(lowerPath, "/.cargo/config") || strings.HasPrefix(lowerPath, ".cargo/config")) {
			continue
		}
		var config struct {
			Paths  []string `toml:"paths"`
			Source map[string]struct {
				Directory     string `toml:"directory"`
				LocalRegistry string `toml:"local-registry"`
			} `toml:"source"`
		}
		if _, err := toml.Decode(string(data), &config); err != nil {
			return fmt.Errorf("parse Cargo configuration %s: %w", path, err)
		}
		if len(config.Paths) > 0 {
			return fmt.Errorf("Cargo configuration %s uses local path overrides; automatic image preparation cannot safely pre-key ordinary source", path)
		}
		for name, source := range config.Source {
			if source.Directory != "" || source.LocalRegistry != "" {
				return fmt.Errorf("Cargo configuration %s source %q uses a local directory or registry; automatic image preparation cannot safely pre-key ordinary source", path, name)
			}
		}
	}
	return nil
}

func addRustTargetPlaceholders(files map[string][]byte) error {
	targets, err := recipe.RustDependencyTargetPaths(files)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if _, exists := files[target]; !exists {
			files[target] = []byte("// Shadowtree manifest-only dependency placeholder.\n")
		}
	}
	return nil
}

func nearestContextFile(files map[string][]byte, start, name string) string {
	current := filepath.ToSlash(filepath.Clean(filepath.FromSlash(start)))
	if current == "." {
		current = ""
	}
	for {
		candidate := name
		if current != "" {
			candidate = current + "/" + name
		}
		if _, ok := files[candidate]; ok {
			return candidate
		}
		if current == "" {
			return ""
		}
		parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(current)))
		if parent == "." || parent == current {
			current = ""
		} else {
			current = parent
		}
	}
}

func relativeImagePath(root, path string) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("path %q is outside canonical project %q", path, root)
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		return "", nil
	}
	return rel, nil
}

func relativeProvenance(root, provenance string) string {
	path, suffix, _ := strings.Cut(provenance, "#")
	if path == "shadowtree-default" {
		return path
	}
	rel, err := relativeImagePath(root, path)
	if err != nil {
		return provenance
	}
	if suffix != "" {
		return rel + "#" + suffix
	}
	return rel
}

func containerDependencyPath(workdir string) string {
	path := "/opt/shadowtree/dependencies"
	if workdir != "" {
		path += "/" + workdir
	}
	path = strings.ReplaceAll(path, `\`, `\\`)
	return strings.ReplaceAll(path, " ", `\ `)
}

func defaultImagePlatform() string {
	return "linux/" + runtime.GOARCH
}

func rustHostPlatform(host string) (string, error) {
	parts := strings.Split(host, "-")
	if len(parts) < 3 || parts[len(parts)-2] != "linux" {
		return "", fmt.Errorf("Rust toolchain host %q is not supported by the Linux system sandbox", host)
	}
	switch parts[0] {
	case "x86_64":
		return "linux/amd64", nil
	case "aarch64":
		return "linux/arm64", nil
	default:
		return "", fmt.Errorf("Rust toolchain host architecture %q is not supported by the system sandbox", parts[0])
	}
}

func systemPackageCommands(packages []string) []string {
	packages = slices.Sorted(slices.Values(packages))
	if len(packages) == 0 {
		return nil
	}
	quoted := make([]string, len(packages))
	for i, pkg := range packages {
		quoted[i] = shellQuote(pkg)
	}
	return []string{"RUN apt-get update && apt-get install -y --no-install-recommends " + strings.Join(quoted, " ") + " && rm -rf /var/lib/apt/lists/*"}
}

func recipePackageCommands(req recipe.Requirements) ([]string, error) {
	var commands []string
	for _, name := range slices.Sorted(maps.Keys(req.GoCommands)) {
		pkg := req.GoCommands[name]
		if !exactGoCommandPackage(pkg) {
			return nil, fmt.Errorf("go command %q must use an exact non-latest module version, got %q", name, pkg)
		}
		commands = append(commands, "RUN GOBIN=/opt/shadowtree/bin go install "+shellQuote(pkg))
	}
	for _, name := range slices.Sorted(maps.Keys(req.NodeCommands)) {
		pkg := req.NodeCommands[name]
		if !exactNodeCommandPackage(pkg) {
			return nil, fmt.Errorf("node command %q must use an exact non-range package version, got %q", name, pkg)
		}
		commands = append(commands, "RUN npm install --global --prefix /opt/shadowtree --ignore-scripts "+shellQuote(pkg))
	}
	return commands, nil
}

func exactGoCommandPackage(value string) bool {
	path, version, ok := strings.Cut(value, "@")
	return ok && path != "" && strings.HasPrefix(version, "v") && exactNodeVersion(strings.TrimPrefix(version, "v")) && !strings.ContainsAny(value, " \t\r\n")
}

func exactNodeCommandPackage(value string) bool {
	index := strings.LastIndex(value, "@")
	if index <= 0 || index == len(value)-1 || strings.ContainsAny(value, " \t\r\n") {
		return false
	}
	version := value[index+1:]
	return !strings.ContainsAny(version, "^~*<>=|") && version != "latest" && exactNodeVersion(version)
}

func exactNodeVersion(value string) bool {
	base, _, _ := strings.Cut(value, "+")
	base, _, _ = strings.Cut(base, "-")
	parts := strings.Split(base, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" || strings.Trim(part, "0123456789") != "" {
			return false
		}
	}
	return true
}

func renderContainerfile(parent string, labels map[string]string, commands []string) string {
	var out strings.Builder
	fmt.Fprintf(&out, "FROM %s\n", parent)
	for _, key := range slices.Sorted(maps.Keys(labels)) {
		fmt.Fprintf(&out, "LABEL %s=%s\n", key, shellQuote(labels[key]))
	}
	for _, command := range commands {
		out.WriteString(command)
		out.WriteByte('\n')
	}
	return out.String()
}

func validateBaseImage(image string) error {
	if image == "" || strings.TrimSpace(image) != image || strings.ContainsAny(image, " \t\r\n{}") {
		return fmt.Errorf("must be one literal image reference")
	}
	if name, digest, ok := strings.Cut(image, "@sha256:"); ok {
		if name == "" || len(digest) != 64 || strings.Trim(digest, "0123456789abcdefABCDEF") != "" {
			return fmt.Errorf("must use a complete sha256 digest")
		}
		return nil
	}
	lastSlash := strings.LastIndexByte(image, '/')
	lastColon := strings.LastIndexByte(image, ':')
	if lastColon <= lastSlash || lastColon == len(image)-1 || image[lastColon+1:] == "latest" {
		return fmt.Errorf("must use a non-latest tag or digest")
	}
	return nil
}

func digestKey(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func digestContext(context map[string][]byte) map[string]string {
	out := map[string]string{}
	for name, data := range context {
		sum := sha256.Sum256(data)
		out[name] = hex.EncodeToString(sum[:])
	}
	return out
}
func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
