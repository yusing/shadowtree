package runner

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/yusing/shadowtree/internal/configfile"
	"github.com/yusing/shadowtree/internal/recipe"
	"github.com/yusing/shadowtree/internal/scriptref"
)

func resolvedSystemImageRecipe(ctx context.Context, options Options) (recipe.Resolved, error) {
	collector := systemRequirementCollector{
		ctx:      ctx,
		root:     options.Resolved,
		source:   options.SourceDir,
		visiting: map[string]bool{},
	}
	if err := collector.collectResolved(options.Resolved, options.Recipes, options.EnumSets, options.ConfigEnv); err != nil {
		return recipe.Resolved{}, err
	}
	resolved := options.Resolved
	resolved.Recipe.Requires = collector.requirements
	return resolved, nil
}

type systemRequirementCollector struct {
	ctx          context.Context
	root         recipe.Resolved
	source       string
	visiting     map[string]bool
	requirements recipe.Requirements
}

func (collector *systemRequirementCollector) collectResolved(resolved recipe.Resolved, recipes map[string]recipe.Recipe, enumSets map[string]recipe.Command, configEnv map[string]string) error {
	key := recipeReferenceStackKey(resolved.ConfigPath, resolved.Name)
	if collector.visiting[key] {
		return fmt.Errorf("system image recipe reference cycle at %s", key)
	}
	collector.visiting[key] = true
	defer delete(collector.visiting, key)
	if resolved.Name != collector.root.Name || resolved.ConfigPath != collector.root.ConfigPath {
		if err := collector.validateNestedContract(resolved); err != nil {
			return err
		}
	}
	if err := mergeImageRequirements(&collector.requirements, resolved.Recipe.Requires); err != nil {
		return fmt.Errorf("recipe %q system image requirements: %w", resolved.Name, err)
	}
	commands := make([]recipe.Command, 0, len(resolved.Recipe.Pre)+len(resolved.Recipe.Post)+2)
	commands = append(commands, resolved.Recipe.ForEach)
	for _, command := range resolved.Recipe.Pre {
		commands = append(commands, command.Cmd)
	}
	commands = append(commands, resolved.Main)
	for _, command := range resolved.Recipe.Post {
		commands = append(commands, command.Cmd)
	}
	for _, command := range commands {
		refs, err := imageRecipeReferences(command)
		if err != nil {
			return fmt.Errorf("recipe %q system image references: %w", resolved.Name, err)
		}
		for _, ref := range refs {
			if err := collector.collectReference(resolved, recipes, enumSets, configEnv, ref); err != nil {
				return err
			}
		}
	}
	return nil
}

func (collector *systemRequirementCollector) collectReference(parent recipe.Resolved, recipes map[string]recipe.Recipe, enumSets map[string]recipe.Command, configEnv map[string]string, ref recipe.RecipeReferenceTarget) error {
	if ref.Path == "" {
		rec, ok := recipes[ref.Name]
		if !ok {
			return fmt.Errorf("recipe %q system image: unknown recipe reference @%s", parent.Name, ref.Name)
		}
		resolved, err := recipe.ResolveWithOptions(ref.Name, rec, ref.Args, nil, configEnv, parent.ConfigPath, parent.Profile, recipe.ResolveOptions{RunID: parent.RunID, Recipes: recipes, EnumSets: enumSets})
		if err != nil {
			return fmt.Errorf("recipe %q system image @%s: %w", parent.Name, ref.Target(), err)
		}
		return collector.collectResolved(resolved, recipes, enumSets, configEnv)
	}
	target, err := configfile.ResolveCrossConfigReference(collector.ctx, ref.Path, parent.ConfigPath, collector.source, configfile.ResolveOptions{EvalDynamicVars: true})
	if err != nil {
		return fmt.Errorf("recipe %q system image @%s: %w", parent.Name, ref.Target(), err)
	}
	rec, ok := target.Recipes[ref.Name]
	if !ok {
		return fmt.Errorf("recipe %q system image: unknown recipe reference @%s", parent.Name, ref.Target())
	}
	resolved, err := recipe.ResolveWithOptions(ref.Name, rec, ref.Args, nil, target.Loaded.Config.Env, target.Loaded.Path, target.Profile, recipe.ResolveOptions{RunID: parent.RunID, Recipes: target.Recipes, EnumSets: target.Loaded.Config.EnumSets})
	if err != nil {
		return fmt.Errorf("recipe %q system image @%s: %w", parent.Name, ref.Target(), err)
	}
	return collector.collectResolved(resolved, target.Recipes, target.Loaded.Config.EnumSets, target.Loaded.Config.Env)
}

func (collector *systemRequirementCollector) validateNestedContract(resolved recipe.Resolved) error {
	mode := recipe.RecipeSandboxMode(resolved.Recipe)
	if mode == recipe.SandboxModeHost {
		return fmt.Errorf("recipe %q system image references host-mode recipe %q", collector.root.Name, resolved.Name)
	}
	if resolved.Profile != collector.root.Profile {
		return fmt.Errorf("recipe %q system image profile %q is incompatible with referenced recipe %q profile %q", collector.root.Name, collector.root.Profile, resolved.Name, resolved.Profile)
	}
	rootBase := ""
	if collector.root.Recipe.System != nil {
		rootBase = collector.root.Recipe.System.BaseImage
	}
	nestedBase := ""
	if resolved.Recipe.System != nil {
		nestedBase = resolved.Recipe.System.BaseImage
	}
	if nestedBase != "" && nestedBase != rootBase {
		return fmt.Errorf("recipe %q system image base %q is incompatible with referenced recipe %q base %q", collector.root.Name, rootBase, resolved.Name, nestedBase)
	}
	return nil
}

func imageRecipeReferences(command recipe.Command) ([]recipe.RecipeReferenceTarget, error) {
	if len(command) == 0 {
		return nil, nil
	}
	if target, ok, err := retryInvocation(command); ok {
		if err != nil {
			return nil, err
		}
		command = target
	}
	if ref, ok := recipe.ParseRecipeReference(command); ok {
		if ref.Name == recipe.RetryCommandHelper {
			return nil, nil
		}
		return []recipe.RecipeReferenceTarget{ref}, nil
	}
	if !recipe.IsScriptCommand(command) {
		return nil, nil
	}
	_, parsed, err := scriptref.Parse(recipe.ScriptShell(command), recipe.ScriptBody(command))
	if err != nil {
		return nil, err
	}
	refs := make([]recipe.RecipeReferenceTarget, 0, len(parsed))
	for _, item := range parsed {
		candidate := recipe.Command{item.Value}
		for _, arg := range item.Args {
			candidate = append(candidate, arg.Value)
		}
		if target, ok, err := retryInvocation(candidate); ok {
			if err != nil {
				return nil, err
			}
			candidate = target
		}
		if ref, ok := recipe.ParseRecipeReference(candidate); ok && ref.Name != recipe.RetryCommandHelper {
			refs = append(refs, ref)
		}
	}
	return refs, nil
}

func mergeImageRequirements(out *recipe.Requirements, incoming recipe.Requirements) error {
	out.Commands = appendUniqueStrings(out.Commands, incoming.Commands...)
	out.OptionalCommands = appendUniqueStrings(out.OptionalCommands, incoming.OptionalCommands...)
	out.SystemPackages = appendUniqueStrings(out.SystemPackages, incoming.SystemPackages...)
	if out.GoCommands == nil {
		out.GoCommands = map[string]string{}
	}
	if out.NodeCommands == nil {
		out.NodeCommands = map[string]string{}
	}
	for name, value := range incoming.GoCommands {
		if existing, ok := out.GoCommands[name]; ok && existing != value {
			return fmt.Errorf("conflicting go command %q: %q and %q", name, existing, value)
		}
		out.GoCommands[name] = value
	}
	for name, value := range incoming.NodeCommands {
		if existing, ok := out.NodeCommands[name]; ok && existing != value {
			return fmt.Errorf("conflicting node command %q: %q and %q", name, existing, value)
		}
		out.NodeCommands[name] = value
	}
	if overlap := slices.Sorted(maps.Keys(overlappingKeys(out.GoCommands, out.NodeCommands))); len(overlap) > 0 {
		return fmt.Errorf("command names use both Go and Node providers: %v", overlap)
	}
	return nil
}

func appendUniqueStrings(values []string, additions ...string) []string {
	seen := make(map[string]struct{}, len(values)+len(additions))
	for _, value := range values {
		seen[value] = struct{}{}
	}
	for _, value := range additions {
		if _, ok := seen[value]; !ok {
			values = append(values, value)
			seen[value] = struct{}{}
		}
	}
	return values
}

func overlappingKeys(left, right map[string]string) map[string]struct{} {
	out := map[string]struct{}{}
	for key := range left {
		if _, ok := right[key]; ok {
			out[key] = struct{}{}
		}
	}
	return out
}
