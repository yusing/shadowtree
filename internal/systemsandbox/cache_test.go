package systemsandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/shadowtree/internal/recipe"
)

func TestPlanCachesSharesCompatibleGoRecipesButConfinesProjectRoots(t *testing.T) {
	plan := func(root, name, command string) CachePlan {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n\ngo 1.26\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		resolved, err := recipe.Resolve(name, recipe.Recipe{Cmd: recipe.Command{command}, Sandboxed: new(recipe.SandboxModeSystem)}, nil, nil, nil, "", recipe.GoProfile)
		if err != nil {
			t.Fatal(err)
		}
		image, err := PlanComposition(testImageRequest(resolved), root)
		if err != nil {
			t.Fatal(err)
		}
		if len(image.Caches) != 1 {
			t.Fatalf("cache count = %d, want 1", len(image.Caches))
		}
		return image.Caches[0]
	}
	root := t.TempDir()
	testCache := plan(root, "test", "go")
	buildCache := plan(root, "build", "go")
	if testCache.Key != buildCache.Key || testCache.Name != buildCache.Name {
		t.Fatalf("compatible recipes split cache: test %s build %s", testCache.Key, buildCache.Key)
	}
	if testCache.Environment["GOCACHE"] != testCache.MountPath || testCache.Concurrency != "shared" {
		t.Fatalf("Go cache contract = %#v", testCache)
	}
	other := plan(t.TempDir(), "test", "go")
	if other.Key == testCache.Key {
		t.Fatal("different canonical projects shared a cache key")
	}
}

func TestPlanCachesMountsExclusiveCargoTargetAtWorkspaceRoot(t *testing.T) {
	t.Setenv("CARGO_BUILD_TARGET", "")
	root := t.TempDir()
	for path, content := range map[string]string{
		"Cargo.toml":            "[workspace]\nmembers = [\"crates/app\"]\n",
		"Cargo.lock":            "# lock\n",
		"rust-toolchain.toml":   "[toolchain]\nchannel = \"1.96.0-x86_64-unknown-linux-gnu\"\n",
		"crates/app/Cargo.toml": "[package]\nname = \"app\"\nversion = \"0.1.0\"\n",
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	resolved, err := recipe.Resolve("test", recipe.Recipe{Cmd: recipe.Command{"cargo", "test"}, Workdir: "crates/app", Sandboxed: new(recipe.SandboxModeSystem)}, nil, nil, nil, "", recipe.RustProfile)
	if err != nil {
		t.Fatal(err)
	}
	image, err := PlanComposition(testImageRequest(resolved), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(image.Caches) != 1 {
		t.Fatalf("cache count = %d, want 1", len(image.Caches))
	}
	cache := image.Caches[0]
	if cache.Provider != "cargo-target" || cache.MountPath != "/opt/shadowtree/cache/cargo-target" || cache.OutputPath != filepath.Join(root, "target") || cache.Environment["CARGO_TARGET_DIR"] != cache.MountPath || cache.Concurrency != recipe.RustTargetCacheConcurrency {
		t.Fatalf("Cargo cache = %#v", cache)
	}
	if err := os.MkdirAll(filepath.Join(root, "crates", "app", "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "crates", "app", "src", "main.rs"), []byte("fn main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	build, err := recipe.Resolve("build", recipe.Recipe{Cmd: recipe.Command{"cargo", "build"}, Workdir: "crates/app", Sandboxed: new(recipe.SandboxModeSystem)}, nil, nil, nil, "", recipe.RustProfile)
	if err != nil {
		t.Fatal(err)
	}
	buildImage, err := PlanComposition(testImageRequest(build), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(buildImage.Caches) != 1 || buildImage.Caches[0].Key != cache.Key {
		t.Fatalf("compatible Cargo test/build recipes split cache: test %s build %#v", cache.Key, buildImage.Caches)
	}
	targeted, err := recipe.Resolve("test-arm", recipe.Recipe{Cmd: recipe.Command{"cargo", "test", "--target=thumbv7em-none-eabihf"}, Workdir: "crates/app", Sandboxed: new(recipe.SandboxModeSystem)}, nil, nil, nil, "", recipe.RustProfile)
	if err != nil {
		t.Fatal(err)
	}
	targetedImage, err := PlanComposition(testImageRequest(targeted), root)
	if err != nil {
		t.Fatal(err)
	}
	if targetedImage.Caches[0].Key == cache.Key {
		t.Fatal("different Cargo target triples shared a cache key")
	}
	envTarget := recipe.Recipe{Cmd: recipe.Command{"cargo", "test"}, Workdir: "crates/app", Sandboxed: new(recipe.SandboxModeSystem), Env: map[string]string{"CARGO_BUILD_TARGET": "wasm32-wasip2"}}
	envResolved, err := recipe.Resolve("test-wasm", envTarget, nil, nil, nil, "", recipe.RustProfile)
	if err != nil {
		t.Fatal(err)
	}
	envImage, err := PlanComposition(testImageRequest(envResolved), root)
	if err != nil {
		t.Fatal(err)
	}
	if envImage.Caches[0].Key == cache.Key || envImage.Caches[0].Key == targetedImage.Caches[0].Key {
		t.Fatal("CARGO_BUILD_TARGET did not select a distinct Cargo cache identity")
	}
	unrelated, err := recipe.Resolve("tool", recipe.Recipe{Cmd: recipe.Command{"tool", "--target=thumbv7em-none-eabihf"}, Workdir: "crates/app", Sandboxed: new(recipe.SandboxModeSystem)}, nil, nil, nil, "", recipe.RustProfile)
	if err != nil {
		t.Fatal(err)
	}
	unrelatedImage, err := PlanComposition(testImageRequest(unrelated), root)
	if err != nil {
		t.Fatal(err)
	}
	if unrelatedImage.Caches[0].Key != cache.Key {
		t.Fatal("unrelated --target option split the Cargo cache identity")
	}
}

func TestPlanCachesIgnoresUnrelatedRecipePackages(t *testing.T) {
	root := t.TempDir()
	writeImageTestFile(t, filepath.Join(root, "go.mod"), "module example.com/app\n\ngo 1.26\n")
	plain, err := PlanComposition(testImageRequest(systemImageRecipe(t, recipe.GoProfile, recipe.Requirements{})), root)
	if err != nil {
		t.Fatal(err)
	}
	withTool, err := PlanComposition(testImageRequest(systemImageRecipe(t, recipe.GoProfile, recipe.Requirements{GoCommands: map[string]string{"stringer": "golang.org/x/tools/cmd/stringer@v0.34.0"}})), root)
	if err != nil {
		t.Fatal(err)
	}
	if plain.Caches[0].Key != withTool.Caches[0].Key {
		t.Fatalf("recipe package split compiler cache: %s != %s", plain.Caches[0].Key, withTool.Caches[0].Key)
	}
}

func TestPrepareCachesCreatesAndThenReusesExactLabelledVolume(t *testing.T) {
	plan := testCachePlan(t)
	var labels map[string]string
	var creates int
	var initializes int
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch strings.Join(args[:2], " ") {
		case "volume inspect":
			if labels == nil {
				return []byte("no such volume"), errors.New("missing")
			}
			return json.Marshal(labels)
		case "volume create":
			creates++
			labels = map[string]string{}
			for index := 2; index+1 < len(args); index++ {
				if args[index] == "--label" {
					key, value, _ := strings.Cut(args[index+1], "=")
					labels[key] = value
					index++
				}
			}
			return []byte(plan.Name), nil
		case "run --rm":
			initializes++
			joined := strings.Join(args, " ")
			if !strings.Contains(joined, fmt.Sprintf("chown -R %d:%d", plan.UID, plan.GID)) || !strings.Contains(joined, plan.Name+":/opt/shadowtree/cache-init") || !strings.Contains(joined, "image:test") {
				t.Fatalf("cache initializer command = %v", args)
			}
			return nil, nil
		default:
			t.Fatalf("unexpected runtime command: %v", args)
			return nil, nil
		}
	}
	var progress bytes.Buffer
	if err := prepareCachesWith(t.Context(), Docker, []CachePlan{plan}, "image:test", &progress, run); err != nil {
		t.Fatal(err)
	}
	if err := prepareCachesWith(t.Context(), Docker, []CachePlan{plan}, "image:test", &progress, run); err != nil {
		t.Fatal(err)
	}
	if creates != 1 || initializes != 1 || !strings.Contains(progress.String(), "cache go-build reused") {
		t.Fatalf("creates = %d initializes = %d progress = %q", creates, initializes, progress.String())
	}
}

func TestApplyConfinementPolicyRekeysCachesForEffectiveIdentity(t *testing.T) {
	original := testCachePlan(t)
	if original.UID == 0 && original.GID == 0 {
		t.Skip("host cache identity already uses mapped root")
	}
	plan, err := ApplyConfinementPolicy(ImagePlan{Caches: []CachePlan{original}}, ConfinementPolicy{User: "0:0"})
	if err != nil {
		t.Fatal(err)
	}
	cache := plan.Caches[0]
	if cache.Key == original.Key || cache.Name == original.Name {
		t.Fatal("effective container identity did not change cache identity")
	}
	if cache.UID != 0 || cache.GID != 0 || cache.Labels["shadowtree.uid"] != "0" || cache.Labels["shadowtree.gid"] != "0" {
		t.Fatalf("rekeyed cache identity = %#v", cache)
	}
	if _, diagnostics := cachePlanFromLabels(cache.Name, cache.ProjectRoot, cache.Labels); len(diagnostics) != 0 {
		t.Fatalf("rekeyed cache diagnostics = %v", diagnostics)
	}
	if original.Labels["shadowtree.uid"] == "0" || original.Labels["shadowtree.gid"] == "0" {
		t.Fatal("ApplyConfinementPolicy mutated the input plan labels")
	}
}

func TestResetCachesRefusesActiveOrMismatchedVolume(t *testing.T) {
	plan := testCachePlan(t)
	labels := plan.Labels
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch strings.Join(args[:2], " ") {
		case "volume inspect":
			return json.Marshal(labels)
		case "ps --filter":
			return []byte("container-id\n"), nil
		default:
			return nil, nil
		}
	}
	if err := resetCachesWith(t.Context(), Docker, []CachePlan{plan}, nil, run); err == nil || !strings.Contains(err.Error(), "active") {
		t.Fatalf("resetCachesWith error = %v, want active refusal", err)
	}
	labels = map[string]string{"shadowtree.owner": "someone-else"}
	if err := resetCachesWith(t.Context(), Docker, []CachePlan{plan}, nil, run); err == nil || !strings.Contains(err.Error(), "ownership") {
		t.Fatalf("resetCachesWith error = %v, want ownership refusal", err)
	}
}

func TestInspectProjectCachesIsReadOnlyAndReportsUnknownSize(t *testing.T) {
	plan := testCachePlan(t)
	var calls []string
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		switch strings.Join(args[:2], " ") {
		case "volume ls":
			return []byte(plan.Name + "\n"), nil
		case "volume inspect":
			return json.Marshal(plan.Labels)
		case "ps --filter":
			return nil, nil
		default:
			t.Fatalf("unexpected runtime command: %v", args)
			return nil, nil
		}
	}
	inspections, err := inspectProjectCachesWith(t.Context(), Docker, plan.ProjectRoot, run)
	if err != nil {
		t.Fatal(err)
	}
	if len(inspections) != 1 || inspections[0].SizeBytes != nil || len(inspections[0].Diagnostics) != 0 {
		t.Fatalf("inspections = %#v", inspections)
	}
	for _, call := range calls {
		if strings.Contains(call, " mount ") || strings.HasPrefix(call, "run ") || strings.Contains(call, "volume create") {
			t.Fatalf("inspection mutated runtime: %s", call)
		}
	}
}

func TestResetCachesRemovesInactiveExactVolumeAndTreatsMissingAsReset(t *testing.T) {
	plan := testCachePlan(t)
	exists := true
	var removals int
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch strings.Join(args[:2], " ") {
		case "volume inspect":
			if !exists {
				return []byte("no such volume"), errors.New("missing")
			}
			return json.Marshal(plan.Labels)
		case "ps --filter":
			return nil, nil
		case "volume rm":
			removals++
			exists = false
			return []byte(plan.Name), nil
		default:
			t.Fatalf("unexpected runtime command: %v", args)
			return nil, nil
		}
	}
	if err := resetCachesWith(t.Context(), Docker, []CachePlan{plan}, nil, run); err != nil {
		t.Fatal(err)
	}
	if err := resetCachesWith(t.Context(), Docker, []CachePlan{plan}, nil, run); err != nil {
		t.Fatal(err)
	}
	if removals != 1 {
		t.Fatalf("removals = %d, want 1", removals)
	}
}

func TestWaitForCacheAvailabilityReportsAndCancels(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\nprintf '%s\\n' active-container\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	plan := testCachePlan(t)
	plan.Concurrency = "exclusive"
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	var progress bytes.Buffer
	err := WaitForCacheAvailability(ctx, Docker, []CachePlan{plan}, &progress)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitForCacheAvailability error = %v, want deadline", err)
	}
	if progress.String() != "shadowtree: waiting for runtime cache go-build\n" {
		t.Fatalf("progress = %q", progress.String())
	}
}

func testCachePlan(t *testing.T) CachePlan {
	t.Helper()
	descriptor := cacheDescriptor{provider: "go-build", format: "go-build-v1", mountPath: "/opt/shadowtree/cache/go-build", toolchain: "1.26.4", concurrency: "shared"}
	plans := planCaches([]cacheDescriptor{descriptor}, "/project", CanonicalProjectKey("/project"), "linux/amd64", []ImageStage{{Key: "base"}})
	if len(plans) != 1 || plans[0].Provider != "go-build" {
		t.Fatalf("test cache plan = %#v", plans)
	}
	return plans[0]
}
