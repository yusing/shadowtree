package runner

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

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
	if err := collector.collectResolved(options.Resolved, options.Recipes, options.EnumSets, options.ConfigEnv, nil); err != nil {
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
	edges        []systemImageReferenceEdge
	requirements recipe.Requirements
}

type imageRecipeReference struct {
	target  recipe.RecipeReferenceTarget
	stage   string
	origin  imageReferenceOrigin
	written string
}

type imageReferenceOrigin struct {
	kind         string
	line         int
	column       int
	sourceLine   string
	throughRetry bool
}

type systemImageCommand struct {
	stage   string
	command recipe.Command
}

type systemImageReferenceEdge struct {
	source systemImageRecipe
	target systemImageRecipe
	ref    imageRecipeReference
}

type systemImageRecipe struct {
	name       string
	configPath string
}

type systemImageCycleError struct {
	message string
}

func (err *systemImageCycleError) Error() string {
	return err.message
}

func (collector *systemRequirementCollector) collectResolved(resolved recipe.Resolved, recipes map[string]recipe.Recipe, enumSets map[string]recipe.Command, configEnv map[string]string, incoming *systemImageReferenceEdge) error {
	key := recipeReferenceStackKey(resolved.ConfigPath, resolved.Name)
	if collector.visiting[key] {
		return collector.referenceCycle(*incoming)
	}
	collector.visiting[key] = true
	if incoming != nil {
		collector.edges = append(collector.edges, *incoming)
	}
	defer func() {
		delete(collector.visiting, key)
		if incoming != nil {
			collector.edges = collector.edges[:len(collector.edges)-1]
		}
	}()
	if resolved.Name != collector.root.Name || resolved.ConfigPath != collector.root.ConfigPath {
		if err := collector.validateNestedContract(resolved); err != nil {
			return err
		}
	}
	if err := mergeImageRequirements(&collector.requirements, resolved.Recipe.Requires); err != nil {
		return fmt.Errorf("recipe %q system image requirements: %w", resolved.Name, err)
	}
	commands := make([]systemImageCommand, 0, len(resolved.Recipe.Pre)+len(resolved.Recipe.Post)+2)
	forEach := recipe.CommandWithRecipeReference(resolved.Recipe.ForEach, resolved.Recipe.Shell, resolved.Recipe.ShellPrelude)
	commands = append(commands, systemImageCommand{stage: "for_each", command: forEach})
	for i, command := range resolved.Recipe.Pre {
		commands = append(commands, systemImageCommand{stage: fmt.Sprintf("pre[%d]", i), command: command.Cmd})
	}
	commands = append(commands, systemImageCommand{stage: "cmd", command: resolved.Main})
	for i, command := range resolved.Recipe.Post {
		commands = append(commands, systemImageCommand{stage: fmt.Sprintf("post[%d]", i), command: command.Cmd})
	}
	for _, item := range commands {
		refs, err := imageRecipeReferences(item.command, resolved.Recipe.ShellPrelude)
		if err != nil {
			return fmt.Errorf("%s %s system image references: %w", systemImageRecipeLabel(resolved.ConfigPath, resolved.Name), item.stage, err)
		}
		for _, ref := range refs {
			ref.stage = item.stage
			if err := collector.collectReference(resolved, recipes, enumSets, configEnv, ref); err != nil {
				return err
			}
		}
	}
	return nil
}

func (collector *systemRequirementCollector) collectReference(parent recipe.Resolved, recipes map[string]recipe.Recipe, enumSets map[string]recipe.Command, configEnv map[string]string, reference imageRecipeReference) error {
	ref := reference.target
	if ref.Path == "" {
		rec, ok := recipes[ref.Name]
		if !ok {
			return fmt.Errorf("%s %s references unknown recipe @%s", systemImageRecipeLabel(parent.ConfigPath, parent.Name), reference.description(), ref.Name)
		}
		resolved, err := recipe.ResolveWithOptions(ref.Name, rec, ref.Args, nil, configEnv, parent.ConfigPath, parent.Profile, recipe.ResolveOptions{RunID: parent.RunID, Recipes: recipes, EnumSets: enumSets})
		if err != nil {
			return fmt.Errorf("%s %s references @%s: %w", systemImageRecipeLabel(parent.ConfigPath, parent.Name), reference.description(), ref.Target(), err)
		}
		edge := collector.referenceEdge(parent, resolved, reference)
		return collector.collectResolved(resolved, recipes, enumSets, configEnv, &edge)
	}
	target, err := configfile.ResolveCrossConfigReference(collector.ctx, ref.Path, parent.ConfigPath, collector.source, configfile.ResolveOptions{EvalDynamicVars: true})
	if err != nil {
		return fmt.Errorf("%s %s references @%s: %w", systemImageRecipeLabel(parent.ConfigPath, parent.Name), reference.description(), ref.Target(), err)
	}
	rec, ok := target.Recipes[ref.Name]
	if !ok {
		return fmt.Errorf("%s %s references unknown recipe @%s", systemImageRecipeLabel(parent.ConfigPath, parent.Name), reference.description(), ref.Target())
	}
	resolved, err := recipe.ResolveWithOptions(ref.Name, rec, ref.Args, nil, target.Loaded.Config.Env, target.Loaded.Path, target.Profile, recipe.ResolveOptions{RunID: parent.RunID, Recipes: target.Recipes, EnumSets: target.Loaded.Config.EnumSets})
	if err != nil {
		return fmt.Errorf("%s %s references @%s: %w", systemImageRecipeLabel(parent.ConfigPath, parent.Name), reference.description(), ref.Target(), err)
	}
	edge := collector.referenceEdge(parent, resolved, reference)
	return collector.collectResolved(resolved, target.Recipes, target.Loaded.Config.EnumSets, target.Loaded.Config.Env, &edge)
}

func (collector *systemRequirementCollector) referenceEdge(parent, target recipe.Resolved, ref imageRecipeReference) systemImageReferenceEdge {
	return systemImageReferenceEdge{
		source: systemImageRecipe{name: parent.Name, configPath: parent.ConfigPath},
		target: systemImageRecipe{name: target.Name, configPath: target.ConfigPath},
		ref:    ref,
	}
}

func (collector *systemRequirementCollector) referenceCycle(closing systemImageReferenceEdge) error {
	edges := append(collector.edges, closing)
	var message strings.Builder
	message.WriteString("error: system image recipe reference cycle")
	for i, edge := range edges {
		anchor := "-->"
		if i > 0 {
			anchor = ":::"
		}
		fmt.Fprintf(&message, "\n\n  %s %s", anchor, filepath.ToSlash(edge.source.configPath))
		message.WriteString("\n   |")
		fmt.Fprintf(&message, "\n   | recipe %q · %s", edge.source.name, edge.ref.context())
		if edge.ref.origin.line > 0 {
			fmt.Fprintf(&message, "\n   | expanded %s:%d:%d", edge.ref.origin.kind, edge.ref.origin.line, edge.ref.origin.column)
		}
		fmt.Fprintf(&message, "\n   | %s", edge.ref.origin.sourceLine)
		indent := diagnosticIndent(edge.ref.origin.sourceLine, edge.ref.origin.column)
		fmt.Fprintf(&message, "\n   | %s%s references recipe %q", indent, strings.Repeat("^", len(edge.ref.written)), edge.target.name)
		if edge.target.configPath != edge.source.configPath {
			fmt.Fprintf(&message, " in %s", filepath.ToSlash(edge.target.configPath))
		}
		if i == len(edges)-1 {
			message.WriteString("\n   | ")
			message.WriteString(indent)
			message.WriteString(strings.Repeat(" ", len(edge.ref.written)+1))
			message.WriteString("cycle closes here")
		}
	}
	return &systemImageCycleError{message: message.String()}
}

func wrapSystemImageRequirements(name string, err error) error {
	if _, ok := errors.AsType[*systemImageCycleError](err); ok {
		return err
	}
	return fmt.Errorf("recipe %q system image requirements: %w", name, err)
}

func diagnosticIndent(line string, column int) string {
	end := min(max(column-1, 0), len(line))
	return strings.Map(func(r rune) rune {
		if r == '\t' {
			return r
		}
		return ' '
	}, line[:end])
}

func (ref imageRecipeReference) description() string {
	return ref.stage + " " + ref.origin.description()
}

func (ref imageRecipeReference) context() string {
	context := ref.stage + " "
	if ref.origin.kind == "shell_prelude" {
		context += "inherits shell_prelude"
	} else {
		context += "contains the recipe reference"
	}
	if ref.origin.throughRetry {
		context += " through @retry"
	}
	return context
}

func (origin imageReferenceOrigin) description() string {
	if origin.line == 0 {
		if origin.throughRetry {
			return "through @retry"
		}
		return "directly"
	}
	description := fmt.Sprintf("%s line %d:%d", origin.kind, origin.line, origin.column)
	if origin.throughRetry {
		description += " through @retry"
	}
	return description
}

func systemImageRecipeLabel(configPath, name string) string {
	if configPath == "" {
		return fmt.Sprintf("recipe %q", name)
	}
	return fmt.Sprintf("recipe %q in %s", name, filepath.ToSlash(configPath))
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

func imageRecipeReferences(command recipe.Command, shellPrelude string) ([]imageRecipeReference, error) {
	if len(command) == 0 {
		return nil, nil
	}
	origin := imageReferenceOrigin{kind: "command", column: 1}
	written := command[0]
	if target, ok, err := retryInvocation(command); ok {
		if err != nil {
			return nil, fmt.Errorf("direct @retry: %w", err)
		}
		command = target
		origin.throughRetry = true
		written = command[0]
	}
	if ref, ok := recipe.ParseRecipeReference(command); ok {
		if ref.Name == recipe.RetryCommandHelper {
			return nil, nil
		}
		origin.sourceLine = written
		return []imageRecipeReference{{target: ref, origin: origin, written: written}}, nil
	}
	if !recipe.IsScriptCommand(command) {
		return nil, nil
	}
	_, parsed, err := scriptref.Parse(recipe.ScriptShell(command), recipe.ScriptBody(command))
	if err != nil {
		return nil, fmt.Errorf("script: %w", err)
	}
	refs := make([]imageRecipeReference, 0, len(parsed))
	for _, item := range parsed {
		origin := imageScriptReferenceOrigin(command, shellPrelude, item.Start)
		written := item.Value
		candidate := recipe.Command{item.Value}
		for _, arg := range item.Args {
			candidate = append(candidate, arg.Value)
		}
		if target, ok, err := retryInvocation(candidate); ok {
			if err != nil {
				return nil, fmt.Errorf("%s @retry: %w", origin.description(), err)
			}
			candidate = target
			origin.throughRetry = true
			written = candidate[0]
			if len(item.Args) > 0 {
				origin.column = item.Args[0].Start.Col + 1
			}
		}
		if ref, ok := recipe.ParseRecipeReference(candidate); ok && ref.Name != recipe.RetryCommandHelper {
			refs = append(refs, imageRecipeReference{target: ref, origin: origin, written: written})
		}
	}
	return refs, nil
}

func imageScriptReferenceOrigin(command recipe.Command, shellPrelude string, position scriptref.Position) imageReferenceOrigin {
	prelude := strings.TrimRight(shellPrelude, "\r\n")
	body := recipe.ScriptBody(command)
	origin := imageReferenceOrigin{
		kind:       "script",
		line:       position.Line + 1,
		column:     position.Col + 1,
		sourceLine: sourceLine(body, position.Line),
	}
	if prelude != "" && (body == prelude || strings.HasPrefix(body, prelude+"\n")) {
		preludeLines := strings.Count(prelude, "\n") + 1
		if position.Line < preludeLines {
			origin.kind = "shell_prelude"
			return origin
		}
		origin.line = position.Line - preludeLines + 1
	}
	return origin
}

func sourceLine(body string, line int) string {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	if line < 0 || line >= len(lines) {
		return ""
	}
	return lines[line]
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
