package recipe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

const DefaultRustToolchain = "1.96.0"

const (
	RustToolchainDefault = "shadowtree-default"
	RustToolchainFile    = "rust-toolchain"
	RustToolchainTOML    = "rust-toolchain.toml"
)

const RustTargetCacheConcurrency = "exclusive"

var exactRustToolchainPattern = regexp.MustCompile(`^([0-9]+\.[0-9]+\.[0-9]+)(?:-([A-Za-z0-9_]+(?:-[A-Za-z0-9_]+)+))?$`)

// RustProject is the canonical Cargo and toolchain metadata consumed by Rust
// recipes and sandbox providers.
type RustProject struct {
	WorkspaceRoot          string
	RootManifest           string
	MemberManifests        []string
	Lockfile               string
	Toolchain              string
	ToolchainProvenance    string
	CompilerCommit         string
	HostTriple             string
	TargetTriple           string
	CargoHome              string
	RegistryCache          string
	GitCache               string
	TargetDir              string
	ProjectCacheKey        string
	LockedPreparation      bool
	FetchCommand           Command
	CacheCompatibility     []string
	TargetCacheConcurrency string
}

type cargoMetadata struct {
	WorkspaceRoot   string `json:"workspace_root"`
	TargetDirectory string `json:"target_directory"`
	Packages        []struct {
		ManifestPath string `json:"manifest_path"`
	} `json:"packages"`
}

type rustToolchainFile struct {
	Toolchain struct {
		Channel string `toml:"channel"`
	} `toml:"toolchain"`
}

type cargoConfig struct {
	Build struct {
		Target string `toml:"target"`
	} `toml:"build"`
}

type rustSelection struct {
	dir        string
	manifest   string
	toolchain  string
	provenance string
}

// RustToolchain validates the project marker and returns the selected exact
// toolchain without invoking Cargo.
func RustToolchain(dir string) (string, error) {
	selection, err := selectRustProject(dir)
	if err != nil {
		return "", err
	}
	return selection.toolchain, nil
}

// ResolveRustProject resolves one Cargo workspace/package and exact toolchain.
// env is the invocation environment used for Cargo/rustc and target selection.
func ResolveRustProject(ctx context.Context, dir string, env, buildArgs []string) (RustProject, error) {
	if err := ctx.Err(); err != nil {
		return RustProject{}, err
	}
	selection, err := selectRustProject(dir)
	if err != nil {
		return RustProject{}, err
	}
	absDir := selection.dir
	manifest := selection.manifest
	toolchain := selection.toolchain
	provenance := selection.provenance
	rustcOutput, err := runRustTool(ctx, env, toolchain, "rustc", "--version", "--verbose")
	if err != nil {
		return RustProject{}, fmt.Errorf("resolve Rust toolchain %q from %s: %w", toolchain, provenance, err)
	}
	release, commit, host := parseRustcVerbose(string(rustcOutput))
	if release == "" || commit == "" || host == "" {
		return RustProject{}, fmt.Errorf("resolve Rust toolchain %q from %s: rustc did not report an exact release, commit, and host", toolchain, provenance)
	}
	wantRelease, wantHost := rustToolchainParts(toolchain)
	if release != wantRelease {
		return RustProject{}, fmt.Errorf("resolve Rust toolchain %q from %s: rustc selected release %q", toolchain, provenance, release)
	}
	if wantHost != "" && host != wantHost {
		return RustProject{}, fmt.Errorf("resolve Rust toolchain %q from %s: rustc selected host %q", toolchain, provenance, host)
	}

	metadataOutput, err := runRustTool(ctx, env, toolchain, "cargo", "metadata", "--no-deps", "--format-version", "1", "--manifest-path", manifest)
	if err != nil {
		return RustProject{}, fmt.Errorf("resolve Cargo metadata for %s: %w", manifest, err)
	}
	var metadata cargoMetadata
	if err := json.Unmarshal(metadataOutput, &metadata); err != nil {
		return RustProject{}, fmt.Errorf("decode Cargo metadata for %s: %w", manifest, err)
	}
	if metadata.WorkspaceRoot == "" || metadata.TargetDirectory == "" || len(metadata.Packages) == 0 {
		return RustProject{}, fmt.Errorf("Cargo metadata for %s omitted workspace root, target directory, or packages", manifest)
	}
	workspaceRoot := filepath.Clean(metadata.WorkspaceRoot)
	rootManifest := filepath.Join(workspaceRoot, "Cargo.toml")
	members := make([]string, 0, len(metadata.Packages))
	for _, pkg := range metadata.Packages {
		members = append(members, filepath.Clean(pkg.ManifestPath))
	}
	slices.Sort(members)
	target, err := selectedRustTarget(absDir, env, buildArgs, host)
	if err != nil {
		return RustProject{}, err
	}
	cargoHome, err := rustCargoHome(env)
	if err != nil {
		return RustProject{}, err
	}
	lockfile := filepath.Join(workspaceRoot, "Cargo.lock")
	if info, err := os.Stat(lockfile); err != nil || info.IsDir() {
		lockfile = ""
	}
	keySum := sha256.Sum256([]byte(workspaceRoot))
	fetch := rustToolCommand(toolchain, "cargo", "fetch", "--locked", "--manifest-path", rootManifest)
	return RustProject{
		WorkspaceRoot:          workspaceRoot,
		RootManifest:           rootManifest,
		MemberManifests:        members,
		Lockfile:               lockfile,
		Toolchain:              toolchain,
		ToolchainProvenance:    provenance,
		CompilerCommit:         commit,
		HostTriple:             host,
		TargetTriple:           target,
		CargoHome:              cargoHome,
		RegistryCache:          filepath.Join(cargoHome, "registry"),
		GitCache:               filepath.Join(cargoHome, "git"),
		TargetDir:              filepath.Clean(metadata.TargetDirectory),
		ProjectCacheKey:        hex.EncodeToString(keySum[:]),
		LockedPreparation:      lockfile != "",
		FetchCommand:           fetch,
		CacheCompatibility:     []string{toolchain, commit, host, target, workspaceRoot},
		TargetCacheConcurrency: RustTargetCacheConcurrency,
	}, nil
}

func selectRustProject(dir string) (rustSelection, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return rustSelection{}, fmt.Errorf("resolve Rust directory %q: %w", dir, err)
	}
	manifest, ok := nearestFileUpward(absDir, "Cargo.toml")
	if !ok {
		return rustSelection{}, fmt.Errorf("Rust profile requires Cargo.toml at or above %s", absDir)
	}
	toolchain, provenance, err := resolveRustToolchain(absDir)
	if err != nil {
		return rustSelection{}, err
	}
	return rustSelection{dir: absDir, manifest: manifest, toolchain: toolchain, provenance: provenance}, nil
}

func resolveRustToolchain(dir string) (string, string, error) {
	for current := dir; ; current = filepath.Dir(current) {
		if path := filepath.Join(current, RustToolchainTOML); regularFile(path) {
			var declaration rustToolchainFile
			if _, err := toml.DecodeFile(path, &declaration); err != nil {
				return "", "", fmt.Errorf("parse %s: %w", path, err)
			}
			return normalizeRustToolchain(path, declaration.Toolchain.Channel)
		}
		if path := filepath.Join(current, RustToolchainFile); regularFile(path) {
			data, err := os.ReadFile(path)
			if err != nil {
				return "", "", fmt.Errorf("read %s: %w", path, err)
			}
			return normalizeRustToolchain(path, strings.TrimSpace(string(data)))
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return DefaultRustToolchain, RustToolchainDefault, nil
}

func normalizeRustToolchain(path, value string) (string, string, error) {
	if !exactRustToolchainPattern.MatchString(value) {
		return "", "", fmt.Errorf("unsupported Rust toolchain in %s: %q; use an exact version such as %s", path, value, DefaultRustToolchain)
	}
	return value, path, nil
}

func rustToolchainParts(toolchain string) (release, host string) {
	matches := exactRustToolchainPattern.FindStringSubmatch(toolchain)
	if len(matches) != 3 {
		return "", ""
	}
	return matches[1], matches[2]
}

func runRustTool(ctx context.Context, env []string, toolchain, tool string, args ...string) ([]byte, error) {
	commandArgs := rustToolCommand(toolchain, tool, args...)
	cmd := exec.CommandContext(ctx, commandArgs[0], commandArgs[1:]...)
	if env != nil {
		cmd.Env = env
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("%s: %s", strings.Join(commandArgs, " "), message)
	}
	return output, nil
}

func rustToolCommand(toolchain, tool string, args ...string) Command {
	command := Command{tool, "+" + toolchain}
	return append(command, args...)
}

func parseRustcVerbose(output string) (release, commit, host string) {
	for line := range strings.Lines(output) {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "release: "); ok {
			release = value
		}
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "commit-hash: "); ok {
			commit = value
		}
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "host: "); ok {
			host = value
		}
	}
	return release, commit, host
}

func selectedRustTarget(dir string, env, buildArgs []string, host string) (string, error) {
	if target, ok, err := rustTargetArgument(buildArgs); err != nil {
		return "", err
	} else if ok {
		return target, nil
	}
	if target := envValue(env, "CARGO_BUILD_TARGET"); target != "" {
		return target, nil
	}
	for current := dir; ; current = filepath.Dir(current) {
		for _, name := range []string{filepath.Join(".cargo", "config.toml"), filepath.Join(".cargo", "config")} {
			path := filepath.Join(current, name)
			if !regularFile(path) {
				continue
			}
			var cfg cargoConfig
			if _, err := toml.DecodeFile(path, &cfg); err != nil {
				return "", fmt.Errorf("parse Cargo target in %s: %w", path, err)
			}
			if cfg.Build.Target != "" {
				return cfg.Build.Target, nil
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	cargoHome, err := rustCargoHome(env)
	if err != nil {
		return "", err
	}
	for _, name := range []string{"config.toml", "config"} {
		path := filepath.Join(cargoHome, name)
		if !regularFile(path) {
			continue
		}
		var cfg cargoConfig
		if _, err := toml.DecodeFile(path, &cfg); err != nil {
			return "", fmt.Errorf("parse Cargo target in %s: %w", path, err)
		}
		if cfg.Build.Target != "" {
			return cfg.Build.Target, nil
		}
	}
	return host, nil
}

func rustTargetArgument(args []string) (string, bool, error) {
	target := ""
	found := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if value, ok := strings.CutPrefix(arg, "--target="); ok {
			if value == "" {
				return "", false, errors.New("Cargo --target requires a non-empty value")
			}
			target, found = value, true
			continue
		}
		if arg != "--target" {
			continue
		}
		if i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "-") {
			return "", false, errors.New("Cargo --target requires a value")
		}
		i++
		target, found = args[i], true
	}
	return target, found, nil
}

func rustCargoHome(env []string) (string, error) {
	if value := envValue(env, "CARGO_HOME"); value != "" {
		abs, err := filepath.Abs(value)
		if err != nil {
			return "", fmt.Errorf("resolve CARGO_HOME %q: %w", value, err)
		}
		return abs, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve Cargo home: %w", err)
	}
	return filepath.Join(home, ".cargo"), nil
}

func envValue(env []string, name string) string {
	prefix := name + "="
	for i := len(env) - 1; i >= 0; i-- {
		if value, ok := strings.CutPrefix(env[i], prefix); ok {
			return value
		}
	}
	return ""
}

func nearestFileUpward(dir, name string) (string, bool) {
	for current := dir; ; current = filepath.Dir(current) {
		path := filepath.Join(current, name)
		if regularFile(path) {
			return path, true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", false
		}
	}
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
