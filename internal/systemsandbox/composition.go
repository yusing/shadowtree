package systemsandbox

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/shadowtree/internal/recipe"
)

const (
	managedFoundation        = "debian:trixie-slim"
	toolchainContractVersion = "toolchain-provider-v1"
)

// ImageRequest preserves every recipe contribution used to plan one system
// image. Root remains the recipe whose lifecycle is executed.
type ImageRequest struct {
	Root          recipe.Resolved
	Contributions []ImageContribution
}

// ImageContribution is one resolved recipe's system-image input and origin.
type ImageContribution struct {
	Resolved       recipe.Resolved
	ConfigIdentity string
	Workdir        string
	ReferenceRoute []ReferenceRouteStep
}

// ReferenceRouteStep identifies one recipe-reference edge leading to a
// contribution.
type ReferenceRouteStep struct {
	ConfigIdentity string
	Recipe         string
	Stage          string
	Reference      string
}

// ToolchainOrigin preserves the recipe input that selected a toolchain.
type ToolchainOrigin struct {
	ConfigIdentity string
	Recipe         string
	Workdir        string
	Provenance     string
	ReferenceRoute []ReferenceRouteStep
}

// ResolvedToolchain is one exact, reusable provider contract.
type ResolvedToolchain struct {
	Kind            string
	Identity        string
	Variant         string
	Platform        string
	ContractVersion string
	Setup           []string
	Verification    []string
	Environment     map[string]string
	Origins         []ToolchainOrigin
}

// DependencyPlan is one provider-owned locked preparation contribution.
type DependencyPlan struct {
	Provider       string
	Identity       string
	ConfigIdentity string
	Recipe         string
	Workdir        string
	Commands       []string
	ContextHashes  map[string]string
	Metadata       map[string]string
}

// PlanComposition resolves a canonical provider set and renders it on the
// managed Trixie foundation without selecting a dominant profile.
func PlanComposition(request ImageRequest, sourceDir string) (ImagePlan, error) {
	if err := validateToolchainRegistry(); err != nil {
		return ImagePlan{}, err
	}
	if request.Root.SandboxMode != recipe.SandboxModeSystem {
		return ImagePlan{}, fmt.Errorf("recipe %q image planning requires sandboxed = %q", request.Root.Name, recipe.SandboxModeSystem)
	}
	if len(request.Contributions) == 0 {
		return ImagePlan{}, fmt.Errorf("recipe %q image planning has no contributions", request.Root.Name)
	}
	source, err := filepath.Abs(sourceDir)
	if err != nil {
		return ImagePlan{}, err
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return ImagePlan{}, fmt.Errorf("canonical project root: %w", err)
	}
	toolchains, platform, err := resolveToolchains(request, source)
	if err != nil {
		return ImagePlan{}, err
	}
	composed := len(toolchains) > 0 || len(request.Root.Recipe.Requires.SystemPackages) > 0
	foundation, err := compositionFoundation(request.Contributions, composed)
	if err != nil {
		return ImagePlan{}, err
	}
	tooling, toolchainIdentity, err := toolchainStageCommands(toolchains)
	if err != nil {
		return ImagePlan{}, err
	}
	dependency, dependencies, seeds, cacheDescriptors, err := contributionPlans(request.Contributions, source)
	if err != nil {
		return ImagePlan{}, err
	}
	recipePackages, err := recipePackageCommands(request.Root.Recipe.Requires)
	if err != nil {
		return ImagePlan{}, fmt.Errorf("recipe %q system recipe packages: %w", request.Root.Name, err)
	}
	projectKey := CanonicalProjectKey(source)
	recipeKey := digestKey(map[string]any{"config": rootConfigIdentity(request), "recipe": request.Root.Name})
	baseCommands := []string{"LABEL shadowtree.plan=" + shellQuote("system-image-v2")}
	if composed {
		baseCommands = append(baseCommands, "RUN test -r /etc/os-release && . /etc/os-release && case \"$ID\" in debian|ubuntu) ;; *) echo unsupported foundation: \"$ID\" >&2; exit 1;; esac")
	}
	inputs := []stageInput{
		{name: "base", commands: baseCommands},
		{name: "toolchains", commands: tooling, metadata: map[string]string{"contract": toolchainContractVersion}},
		{name: "system-packages", commands: systemPackageCommands(request.Root.Recipe.Requires.SystemPackages)},
		{name: "recipe-packages", commands: recipePackages},
		{name: "dependencies", commands: dependency.commands, context: dependency.context, metadata: dependency.metadata},
	}
	stages := make([]ImageStage, 0, len(inputs))
	parentTag := foundation
	parentKey := digestKey(map[string]any{"external": foundation, "platform": platform})
	for _, input := range inputs {
		contextHashes := digestContext(input.context)
		identity := map[string]any{
			"version": "system-image-v2", "stage": input.name, "parent": parentKey, "platform": platform,
			"commands": input.commands, "context": contextHashes, "metadata": input.metadata,
		}
		if input.name == "toolchains" {
			identity["providers"] = toolchainIdentity
		}
		key := digestKey(identity)
		tag := "shadowtree.local/stage/" + input.name + ":" + key
		labels := map[string]string{
			"shadowtree.owner": "github.com/yusing/shadowtree", "shadowtree.stage": input.name,
			"shadowtree.key": key, "shadowtree.parent-key": parentKey, "shadowtree.platform": platform,
		}
		stages = append(stages, ImageStage{
			Name: input.name, Platform: platform, Key: key, Tag: tag, Labels: labels,
			Containerfile: renderContainerfile(parentTag, labels, input.commands), Context: input.context,
			ContextHashes: contextHashes, Metadata: maps.Clone(input.metadata),
		})
		parentTag, parentKey = tag, key
	}
	plan := ImagePlan{
		BaseImage: foundation, Platform: platform, Stages: stages,
		FinalTag:   "shadowtree.local/" + projectKey + "/" + recipeKey + ":" + parentKey,
		Toolchains: toolchains, Dependencies: dependencies, DependencySeeds: seeds,
		Caches: planCaches(cacheDescriptors, source, projectKey, platform, stages),
	}
	return plan, nil
}

func toolchainStageCommands(toolchains []ResolvedToolchain) ([]string, []map[string]any, error) {
	commands := []string{
		"RUN install -d -m 0755 /opt/shadowtree/bin /opt/shadowtree/toolchains",
		"ENV PATH=/opt/shadowtree/bin:$PATH",
	}
	identity := make([]map[string]any, 0, len(toolchains))
	for _, toolchain := range toolchains {
		if toolchain.ContractVersion != toolchainContractVersion {
			return nil, nil, fmt.Errorf("toolchain provider %q uses unsupported contract %q", toolchain.Kind, toolchain.ContractVersion)
		}
		commands = append(commands, toolchain.Setup...)
		for _, key := range slices.Sorted(maps.Keys(toolchain.Environment)) {
			if key == "PATH" {
				return nil, nil, fmt.Errorf("toolchain provider %q cannot override managed PATH", toolchain.Kind)
			}
			if !validEnvironmentName(key) {
				return nil, nil, fmt.Errorf("toolchain provider %q has invalid environment name %q", toolchain.Kind, key)
			}
			commands = append(commands, "ENV "+key+"="+toolchain.Environment[key])
		}
		commands = append(commands, toolchain.Verification...)
		identity = append(identity, map[string]any{
			"kind": toolchain.Kind, "identity": toolchain.Identity, "contract": toolchain.ContractVersion,
			"setup": toolchain.Setup, "verification": toolchain.Verification, "environment": toolchain.Environment,
		})
	}
	return commands, identity, nil
}

func validEnvironmentName(name string) bool {
	for index := range len(name) {
		character := name[index]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || character == '_' || index > 0 && character >= '0' && character <= '9' {
			continue
		}
		return false
	}
	return name != ""
}

func validateToolchainRegistry() error {
	for _, profile := range recipe.SupportedProfiles() {
		provider, ok := recipe.ToolchainProvider(profile)
		if !ok {
			return fmt.Errorf("supported profile %q has no toolchain provider", profile)
		}
		switch provider {
		case "go", "node", "rust":
		default:
			return fmt.Errorf("supported profile %q has unknown toolchain provider %q", profile, provider)
		}
	}
	return nil
}

func rootConfigIdentity(request ImageRequest) string {
	for _, contribution := range request.Contributions {
		if contribution.Resolved.Name == request.Root.Name && contribution.Resolved.ConfigPath == request.Root.ConfigPath {
			return contribution.ConfigIdentity
		}
	}
	return ""
}

func contributionPlans(contributions []ImageContribution, source string) (dependencyInput, []DependencyPlan, []DependencySeed, []cacheDescriptor, error) {
	combined := dependencyInput{context: map[string][]byte{}, metadata: map[string]string{}}
	var plans []DependencyPlan
	var seeds []DependencySeed
	var descriptors []cacheDescriptor
	for _, contribution := range contributions {
		dir := filepath.Join(source, filepath.FromSlash(contribution.Workdir))
		input, err := contributionInputs(contribution.Resolved, source, dir)
		if err != nil {
			return dependencyInput{}, nil, nil, nil, fmt.Errorf("%s %q dependency plan: %w", contribution.ConfigIdentity, contribution.Resolved.Name, err)
		}
		for name, data := range input.dependency.context {
			if existing, ok := combined.context[name]; ok && !slices.Equal(existing, data) {
				return dependencyInput{}, nil, nil, nil, fmt.Errorf("dependency context ownership conflict at %s", name)
			}
			combined.context[name] = data
		}
		if len(input.dependency.commands) > 0 {
			provider, identity := dependencyProvider(contribution.Resolved.Profile, input.dependency.metadata)
			plans = append(plans, DependencyPlan{
				Provider: provider, Identity: identity, ConfigIdentity: contribution.ConfigIdentity,
				Recipe: contribution.Resolved.Name, Workdir: contribution.Workdir,
				Commands: slices.Clone(input.dependency.commands), ContextHashes: digestContext(input.dependency.context),
				Metadata: maps.Clone(input.dependency.metadata),
			})
		}
		if input.dependency.seedProvider != "" {
			managerDir := input.dependency.metadata["workdir"]
			target := slashJoin(managerDir, "node_modules")
			sourcePath := "/opt/shadowtree/dependencies/" + target
			seeds = append(seeds, DependencySeed{
				Provider: input.dependency.seedProvider, SourcePath: sourcePath, TargetPath: target,
				Origin: contribution.ConfigIdentity + ":" + contribution.Resolved.Name,
			})
		}
		for _, descriptor := range input.caches {
			merged := false
			for existing := range descriptors {
				if descriptors[existing].mountPath != descriptor.mountPath {
					continue
				}
				if !compatibleCacheDescriptors(descriptors[existing], descriptor) {
					return dependencyInput{}, nil, nil, nil, fmt.Errorf("incompatible cache providers share destination %s", descriptor.mountPath)
				}
				merged = true
				break
			}
			if !merged {
				descriptors = append(descriptors, descriptor)
			}
		}
	}
	slices.SortFunc(plans, func(a, b DependencyPlan) int {
		return strings.Compare(a.Provider+"\x00"+a.Identity+"\x00"+a.ConfigIdentity+"\x00"+a.Recipe+"\x00"+a.Workdir, b.Provider+"\x00"+b.Identity+"\x00"+b.ConfigIdentity+"\x00"+b.Recipe+"\x00"+b.Workdir)
	})
	for index, plan := range plans {
		combined.commands = append(combined.commands, plan.Commands...)
		prefix := fmt.Sprintf("dependency.%d.", index)
		combined.metadata[prefix+"provider"] = plan.Provider
		combined.metadata[prefix+"identity"] = plan.Identity
		combined.metadata[prefix+"config"] = plan.ConfigIdentity
		combined.metadata[prefix+"recipe"] = plan.Recipe
		combined.metadata[prefix+"workdir"] = plan.Workdir
		for key, value := range plan.Metadata {
			combined.metadata[prefix+key] = value
		}
	}
	seeds, err := canonicalSeeds(seeds)
	if err != nil {
		return dependencyInput{}, nil, nil, nil, err
	}
	slices.SortFunc(descriptors, func(a, b cacheDescriptor) int { return strings.Compare(a.mountPath, b.mountPath) })
	if len(combined.commands) == 0 {
		combined.context = nil
		combined.metadata = nil
	}
	return combined, plans, seeds, descriptors, nil
}

func dependencyProvider(profile string, metadata map[string]string) (string, string) {
	provider, _ := recipe.ToolchainProvider(profile)
	switch provider {
	case "go":
		return "go", metadata["toolchain"]
	case "node":
		return metadata["manager"], metadata["manager_identity"]
	case "rust":
		return "rust", metadata["toolchain"]
	default:
		return provider, ""
	}
}

func canonicalSeeds(seeds []DependencySeed) ([]DependencySeed, error) {
	slices.SortFunc(seeds, func(a, b DependencySeed) int {
		return strings.Compare(a.TargetPath+"\x00"+a.Provider+"\x00"+a.SourcePath, b.TargetPath+"\x00"+b.Provider+"\x00"+b.SourcePath)
	})
	out := seeds[:0]
	for _, seed := range seeds {
		if len(out) > 0 && out[len(out)-1].TargetPath == seed.TargetPath {
			if out[len(out)-1].Provider == seed.Provider && out[len(out)-1].SourcePath == seed.SourcePath {
				continue
			}
			return nil, fmt.Errorf("dependency seed ownership conflict at %s between %s and %s", seed.TargetPath, out[len(out)-1].Origin, seed.Origin)
		}
		for _, existing := range out {
			if slashAncestor(existing.TargetPath, seed.TargetPath) || slashAncestor(seed.TargetPath, existing.TargetPath) {
				return nil, fmt.Errorf("dependency seed targets overlap: %s and %s", existing.TargetPath, seed.TargetPath)
			}
		}
		out = append(out, seed)
	}
	return out, nil
}

func compatibleCacheDescriptors(left, right cacheDescriptor) bool {
	return left.provider == right.provider && left.format == right.format && left.toolchain == right.toolchain &&
		left.concurrency == right.concurrency && left.workspace == right.workspace && left.outputPath == right.outputPath &&
		maps.Equal(left.environment, right.environment) && maps.Equal(left.inputs, right.inputs)
}

func resolveToolchains(request ImageRequest, source string) ([]ResolvedToolchain, string, error) {
	selected := map[string]ResolvedToolchain{}
	platform := defaultImagePlatform()
	for _, contribution := range request.Contributions {
		toolchain, ok, err := resolveContributionToolchain(contribution, source)
		if err != nil {
			return nil, "", fmt.Errorf("%s %q toolchain: %w", contribution.ConfigIdentity, contribution.Resolved.Name, err)
		}
		if !ok {
			continue
		}
		if toolchain.Platform != platform {
			return nil, "", fmt.Errorf("toolchain platform conflict: %s requires %s while the composition requires %s", originLabel(toolchain.Origins[0]), toolchain.Platform, platform)
		}
		if existing, exists := selected[toolchain.Kind]; exists {
			if existing.Identity != toolchain.Identity {
				if existing.Variant == toolchain.Variant && defaultToolchain(existing) != defaultToolchain(toolchain) {
					if defaultToolchain(existing) {
						toolchain.Origins = append(toolchain.Origins, existing.Origins...)
						selected[toolchain.Kind] = toolchain
					} else {
						existing.Origins = append(existing.Origins, toolchain.Origins...)
						selected[toolchain.Kind] = existing
					}
					continue
				}
				return nil, "", fmt.Errorf("conflicting %s toolchains: %s requires %s and %s requires %s", toolchain.Kind, originLabel(existing.Origins[0]), existing.Identity, originLabel(toolchain.Origins[0]), toolchain.Identity)
			}
			existing.Origins = append(existing.Origins, toolchain.Origins...)
			selected[toolchain.Kind] = existing
			continue
		}
		selected[toolchain.Kind] = toolchain
	}
	implicit := []struct {
		kind     string
		required bool
		profile  string
	}{
		{kind: "go", required: len(request.Root.Recipe.Requires.GoCommands) > 0, profile: recipe.GoProfile},
		{kind: "node", required: len(request.Root.Recipe.Requires.NodeCommands) > 0, profile: recipe.NodeProfile},
	}
	for _, requirement := range implicit {
		if !requirement.required {
			continue
		}
		if _, ok := selected[requirement.kind]; ok {
			continue
		}
		resolved := request.Root
		resolved.Profile = requirement.profile
		contribution := ImageContribution{Resolved: resolved, ConfigIdentity: rootConfigIdentity(request)}
		toolchain, ok, err := resolveContributionToolchain(contribution, source)
		if err != nil {
			return nil, "", fmt.Errorf("installable %s command provider: %w", requirement.kind, err)
		}
		if !ok {
			return nil, "", fmt.Errorf("installable %s commands have no toolchain provider", requirement.kind)
		}
		selected[toolchain.Kind] = toolchain
	}
	kinds := slices.Sorted(maps.Keys(selected))
	toolchains := make([]ResolvedToolchain, 0, len(kinds))
	for _, kind := range kinds {
		toolchain := selected[kind]
		slices.SortFunc(toolchain.Origins, func(a, b ToolchainOrigin) int { return strings.Compare(originLabel(a), originLabel(b)) })
		toolchains = append(toolchains, toolchain)
	}
	return toolchains, platform, nil
}

func defaultToolchain(toolchain ResolvedToolchain) bool {
	return len(toolchain.Origins) > 0 && toolchain.Origins[0].Provenance == "shadowtree-default"
}

func resolveContributionToolchain(contribution ImageContribution, source string) (ResolvedToolchain, bool, error) {
	dir := filepath.Join(source, filepath.FromSlash(contribution.Workdir))
	origin := ToolchainOrigin{
		ConfigIdentity: contribution.ConfigIdentity, Recipe: contribution.Resolved.Name,
		Workdir: contribution.Workdir, ReferenceRoute: slices.Clone(contribution.ReferenceRoute),
	}
	platform := defaultImagePlatform()
	providerKind, supported := recipe.ToolchainProvider(contribution.Resolved.Profile)
	if !supported {
		return ResolvedToolchain{}, false, nil
	}
	switch providerKind {
	case "go":
		info, err := recipe.ResolveGoToolchain(dir, source)
		if err != nil {
			return ResolvedToolchain{}, false, err
		}
		origin.Provenance = relativeProvenance(source, info.Provenance)
		prefix := "/opt/shadowtree/toolchains/go/" + info.Version
		return provider("go", info.Version, "", platform, origin,
			[]string{"COPY --from=golang:" + info.Version + "-trixie /usr/local/go/ " + prefix + "/", "RUN ln -s " + prefix + "/bin/go /opt/shadowtree/bin/go && ln -s " + prefix + "/bin/gofmt /opt/shadowtree/bin/gofmt"},
			[]string{"RUN test \"$(/opt/shadowtree/bin/go version | awk '{print $3}')\" = go" + info.Version}, nil), true, nil
	case "node":
		manager, err := recipe.ResolveNodePackageManagerWithin(dir, source)
		if err != nil {
			return ResolvedToolchain{}, false, err
		}
		origin.Provenance = relativeProvenance(source, manager.Provenance)
		if manager.Name == "bun" {
			prefix := "/opt/shadowtree/toolchains/bun/" + manager.Version
			return provider("bun", manager.Identity, "bun", platform, origin,
				[]string{"COPY --from=oven/bun:" + manager.Version + "-slim /usr/local/bin/bun " + prefix + "/bin/bun", "RUN ln -s " + prefix + "/bin/bun /opt/shadowtree/bin/bun && ln -s " + prefix + "/bin/bun /opt/shadowtree/bin/bunx"},
				[]string{"RUN test \"$(/opt/shadowtree/bin/bun --version)\" = " + shellQuote(manager.Version)}, nil), true, nil
		}
		identity := recipe.DefaultNodeRelease + "+" + manager.Identity
		prefix := "/opt/shadowtree/toolchains/node/" + identity
		setup := []string{"COPY --from=" + recipe.DefaultNodeImage + " /usr/local/ " + prefix + "/", "RUN ln -s " + prefix + "/bin/node /opt/shadowtree/bin/node && ln -s " + prefix + "/bin/npm /opt/shadowtree/bin/npm && ln -s " + prefix + "/bin/npx /opt/shadowtree/bin/npx && ln -s " + prefix + "/bin/corepack /opt/shadowtree/bin/corepack"}
		verify := []string{"RUN test \"$(/opt/shadowtree/bin/node --version)\" = v" + recipe.DefaultNodeRelease}
		environment := map[string]string{}
		if manager.Name == "pnpm" || manager.Name == "yarn" {
			corepackHome := prefix + "/corepack"
			environment["COREPACK_HOME"] = corepackHome
			setup = append(setup, "RUN COREPACK_HOME="+corepackHome+" /opt/shadowtree/bin/corepack enable --install-directory /opt/shadowtree/bin && COREPACK_HOME="+corepackHome+" /opt/shadowtree/bin/corepack prepare "+shellQuote(manager.Identity)+" --activate")
			verify = append(verify, "RUN test \"$(/opt/shadowtree/bin/"+manager.Name+" --version)\" = "+shellQuote(manager.Version))
		} else {
			if manager.Declared {
				setup = append(setup, "RUN "+prefix+"/bin/npm install --global --prefix "+prefix+" --ignore-scripts "+shellQuote(manager.Identity))
			}
			verify = append(verify, "RUN test \"$(/opt/shadowtree/bin/npm --version)\" = "+shellQuote(manager.Version))
		}
		return provider("node", identity, manager.Name, platform, origin, setup, verify, environment), true, nil
	case "rust":
		identity, err := recipe.RustToolchainWithin(dir, source)
		if err != nil {
			return ResolvedToolchain{}, false, err
		}
		release, host, _ := strings.Cut(identity, "-")
		if host != "" {
			platform, err = rustHostPlatform(host)
			if err != nil {
				return ResolvedToolchain{}, false, err
			}
		}
		origin.Provenance = identity
		prefix := "/opt/shadowtree/toolchains/rust/" + identity
		env := map[string]string{"CARGO_HOME": prefix + "/cargo", "RUSTUP_HOME": prefix + "/rustup"}
		verification := []string{"RUN /opt/shadowtree/bin/rustc --version --verbose | grep -F " + shellQuote("release: "+release)}
		if host != "" {
			verification = append(verification, "RUN /opt/shadowtree/bin/rustc --version --verbose | grep -F "+shellQuote("host: "+host))
		}
		return provider("rust", identity, "", platform, origin,
			[]string{"COPY --from=rust:" + release + "-trixie /usr/local/cargo/ " + prefix + "/cargo/", "COPY --from=rust:" + release + "-trixie /usr/local/rustup/ " + prefix + "/rustup/", "RUN ln -s " + prefix + "/cargo/bin/cargo /opt/shadowtree/bin/cargo && ln -s " + prefix + "/cargo/bin/rustc /opt/shadowtree/bin/rustc && ln -s " + prefix + "/cargo/bin/rustup /opt/shadowtree/bin/rustup"},
			verification, env), true, nil
	}
	return ResolvedToolchain{}, false, fmt.Errorf("supported profile %q has unknown provider %q", contribution.Resolved.Profile, providerKind)
}

func provider(kind, identity, variant, platform string, origin ToolchainOrigin, setup, verification []string, environment map[string]string) ResolvedToolchain {
	return ResolvedToolchain{Kind: kind, Identity: identity, Variant: variant, Platform: platform, ContractVersion: toolchainContractVersion, Setup: setup, Verification: verification, Environment: environment, Origins: []ToolchainOrigin{origin}}
}

func compositionFoundation(contributions []ImageContribution, composed bool) (string, error) {
	base := ""
	for _, contribution := range contributions {
		if contribution.Resolved.Recipe.System == nil || contribution.Resolved.Recipe.System.BaseImage == "" {
			continue
		}
		candidate := contribution.Resolved.Recipe.System.BaseImage
		if base != "" && base != candidate {
			return "", fmt.Errorf("conflicting explicit system foundations %q and %q", base, candidate)
		}
		base = candidate
	}
	if base == "" {
		return managedFoundation, nil
	}
	if err := validateBaseImage(base); err != nil {
		return "", fmt.Errorf("system.base_image: %w", err)
	}
	if composed && !supportedCompositionFoundation(base) {
		return "", fmt.Errorf("system.base_image %q cannot host composed toolchains or system packages; use pinned Debian or Ubuntu", base)
	}
	return base, nil
}

func supportedCompositionFoundation(image string) bool {
	repository := strings.ToLower(image)
	if name, _, ok := strings.Cut(repository, "@"); ok {
		repository = name
	}
	if slash, colon := strings.LastIndexByte(repository, '/'), strings.LastIndexByte(repository, ':'); colon > slash {
		repository = repository[:colon]
	}
	return repository == "debian" || repository == "ubuntu" || strings.HasSuffix(repository, "/library/debian") || strings.HasSuffix(repository, "/library/ubuntu")
}

func originLabel(origin ToolchainOrigin) string {
	return origin.ConfigIdentity + ":" + origin.Recipe + " (workdir " + origin.Workdir + ")"
}
