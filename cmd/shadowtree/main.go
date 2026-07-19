package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"maps"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/yusing/shadowtree/internal/completion"
	"github.com/yusing/shadowtree/internal/configfile"
	"github.com/yusing/shadowtree/internal/globalflag"
	"github.com/yusing/shadowtree/internal/recipe"
	"github.com/yusing/shadowtree/internal/runner"
)

const version = "0.1.0"

type options struct {
	configPath string
	profile    string
	all        bool
	syncOut    multiFlag
	syncOutAll bool
	printOnly  bool
	expanded   bool
	checkOnly  bool
	checkShell bool
	verbose    bool
	help       bool
	showVer    bool
}

type multiFlag []string

type recipeHelpOptions struct {
	Dir        string
	ConfigPath string
	Env        map[string]string
	Recipes    map[string]recipe.Recipe
	EnumSets   map[string]recipe.Command
	Color      bool
}

type helpColors struct {
	enabled bool
}

func (flag *multiFlag) String() string {
	return strings.Join(*flag, ",")
}

func (flag *multiFlag) Set(value string) error {
	for part := range strings.SplitSeq(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			*flag = append(*flag, part)
		}
	}
	return nil
}

func main() {
	log.SetFlags(0)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	stopAfterCancel := context.AfterFunc(ctx, stop)
	defer func() {
		stopAfterCancel()
		stop()
	}()
	if len(os.Args) > 1 && os.Args[1] == runner.OverlayHelperCommand {
		os.Exit(runner.OverlayHelperMain(ctx, os.Args[2:]))
	}
	if err := run(ctx, os.Args[1:]); err != nil {
		if exitErr, ok := errors.AsType[runner.ExitError](err); ok {
			os.Exit(exitErr.Code)
		}
		log.Fatal(err)
	}
}

func run(ctx context.Context, args []string) error {
	opts, rest, err := parseGlobal(args)
	if err != nil {
		return err
	}
	if opts.showVer {
		fmt.Fprintln(os.Stdout, version)
		return nil
	}
	if opts.help || len(rest) == 0 {
		if len(rest) == 0 && opts.all && !opts.help {
			return errors.New("--all requires a recipe")
		}
		printBasicHelp(os.Stdout)
		return nil
	}
	if err := validateGlobalMode(opts); err != nil {
		return err
	}
	switch rest[0] {
	case "completion":
		if opts.all {
			return errors.New("--all requires a recipe")
		}
		if len(rest) != 2 {
			return errors.New("usage: shadowtree completion <shell>")
		}
		return completion.Script(os.Stdout, rest[1])
	case "__complete":
		return runComplete(ctx, rest[1:])
	case "init":
		if opts.all {
			return errors.New("--all requires a recipe")
		}
		if len(rest) > 2 {
			return errors.New("usage: shadowtree init [path]")
		}
		path := ".shadowtree.toml"
		if len(rest) > 1 {
			path = rest[1]
		}
		return configfile.Init(path)
	}
	recipeHelpColor := true
	if len(rest) > 2 && rest[0] == "help" {
		color, err := parseRecipeHelpOptions(rest[2:])
		if err != nil {
			return err
		}
		recipeHelpColor = color
	}
	resolvedSet, loaded, profile, err := resolveSet(ctx, opts, true)
	if err != nil {
		return err
	}
	if rest[0] == "exec" {
		if opts.all {
			return errors.New("--all is not supported by exec")
		}
		if len(rest) < 2 || rest[1] != "--" {
			return errors.New("usage: shadowtree exec -- <cmd> [args...]")
		}
		command := rest[2:]
		if len(command) == 0 {
			return errors.New("exec requires a command")
		}
		rec := recipe.Recipe{Cmd: recipe.Command(command)}
		resolved, err := recipe.Resolve("exec", rec, nil, opts.syncOut, loaded.Config.Env, loaded.Path, profile)
		if err != nil {
			return err
		}
		return runner.Run(ctx, runner.Options{
			Resolved:      resolved,
			ConfigEnv:     loaded.Config.Env,
			SourceDir:     mustGetwd(),
			PrintOnly:     opts.printOnly,
			PrintExpanded: opts.expanded,
			CheckOnly:     opts.checkOnly,
			CheckShell:    opts.checkShell,
			Verbose:       opts.verbose,
			SyncOutAll:    opts.syncOutAll,
			Stdin:         os.Stdin,
			Stdout:        os.Stdout,
			Stderr:        os.Stderr,
		})
	}
	switch rest[0] {
	case "help":
		if opts.all {
			return errors.New("--all requires a recipe invocation; use help <recipe> to inspect support")
		}
		if len(rest) > 1 {
			rec, ok := resolvedSet[rest[1]]
			if !ok {
				return fmt.Errorf("unknown recipe: %s", rest[1])
			}
			return printRecipeHelp(ctx, os.Stdout, rest[1], rec, recipeHelpOptions{
				Dir:        mustGetwd(),
				ConfigPath: loaded.Path,
				Env:        loaded.Config.Env,
				Recipes:    resolvedSet,
				EnumSets:   loaded.Config.EnumSets,
				Color:      recipeHelpColor,
			})
		}
		return printHelp(os.Stdout, loaded, profile, resolvedSet)
	case "recipes":
		if opts.all {
			return errors.New("--all requires a recipe")
		}
		return printRecipes(os.Stdout, resolvedSet)
	case "config":
		if opts.all {
			return errors.New("--all requires a recipe")
		}
		return printConfig(os.Stdout, loaded, profile, resolvedSet)
	default:
		name, recipeArgs := recipe.Invocation(rest)
		rec, ok := resolvedSet[name]
		if !ok {
			return fmt.Errorf("unknown recipe: %s", name)
		}
		resolveOpts := recipe.ResolveOptions{Recipes: resolvedSet, EnumSets: loaded.Config.EnumSets}
		if opts.all {
			var domain string
			var source recipe.TargetSource
			rec, domain, source, err = recipe.SelectAll(name, rec)
			if err != nil {
				return err
			}
			resolveOpts.Scope = recipe.ScopeAll
			resolveOpts.TargetDomain = domain
			resolveOpts.TargetSource = source
		}
		resolved, err := recipe.ResolveWithOptions(name, rec, recipeArgs, opts.syncOut, loaded.Config.Env, loaded.Path, profile, resolveOpts)
		if err != nil {
			return err
		}
		return runner.Run(ctx, runner.Options{
			Resolved:      resolved,
			Recipes:       resolvedSet,
			EnumSets:      loaded.Config.EnumSets,
			ConfigEnv:     loaded.Config.Env,
			SourceDir:     mustGetwd(),
			PrintOnly:     opts.printOnly,
			PrintExpanded: opts.expanded,
			CheckOnly:     opts.checkOnly,
			CheckShell:    opts.checkShell,
			Verbose:       opts.verbose,
			SyncOutAll:    opts.syncOutAll,
			Stdin:         os.Stdin,
			Stdout:        os.Stdout,
			Stderr:        os.Stderr,
		})
	}
}

func validateGlobalMode(opts options) error {
	if opts.printOnly && opts.checkOnly {
		return errors.New("--print and --check cannot be used together")
	}
	if opts.expanded && !opts.printOnly {
		return errors.New("--expanded requires --print")
	}
	if opts.checkShell && !opts.checkOnly {
		return errors.New("--shell requires --check")
	}
	return nil
}

func parseGlobal(args []string) (options, []string, error) {
	var opts options
	flags := flag.NewFlagSet("shadowtree", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	registerGlobalFlags(flags, &opts)
	boundary := globalFlagBoundary(args)
	if boundary < len(args) {
		if err := flags.Parse(args[:boundary]); err != nil {
			return opts, nil, err
		}
		if args[boundary] == "--" {
			return opts, args[boundary+1:], nil
		}
		return opts, args[boundary:], nil
	}
	if err := flags.Parse(args); err != nil {
		return opts, nil, err
	}
	return opts, flags.Args(), nil
}

func globalFlagBoundary(args []string) int {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return i
		}
		if globalflag.TakesValue(arg) {
			if !strings.Contains(arg, "=") {
				i++
			}
			continue
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			return i
		}
	}
	return len(args)
}

func registerGlobalFlags(flags *flag.FlagSet, opts *options) {
	for _, spec := range globalflag.All() {
		switch spec.Name {
		case globalflag.Config:
			flags.StringVar(&opts.configPath, spec.Name, "", spec.Help)
		case globalflag.Profile:
			flags.StringVar(&opts.profile, spec.Name, "", spec.Help)
		case globalflag.AllTargets:
			flags.BoolVar(&opts.all, spec.Name, false, spec.Help)
		case globalflag.SyncOut:
			flags.Var(&opts.syncOut, spec.Name, spec.Help)
		case globalflag.SyncOutAll:
			flags.BoolVar(&opts.syncOutAll, spec.Name, false, spec.Help)
		case globalflag.Print:
			flags.BoolVar(&opts.printOnly, spec.Name, false, spec.Help)
		case globalflag.Expanded:
			flags.BoolVar(&opts.expanded, spec.Name, false, spec.Help)
		case globalflag.Check:
			flags.BoolVar(&opts.checkOnly, spec.Name, false, spec.Help)
		case globalflag.Shell:
			flags.BoolVar(&opts.checkShell, spec.Name, false, spec.Help)
		case globalflag.Verbose:
			flags.BoolVar(&opts.verbose, spec.Name, false, spec.Help)
		case globalflag.Help:
			flags.BoolVar(&opts.help, spec.Name, false, spec.Help)
		case globalflag.Version:
			flags.BoolVar(&opts.showVer, spec.Name, false, spec.Help)
		}
	}
}

func parseRecipeHelpOptions(args []string) (bool, error) {
	color := true
	for _, arg := range args {
		key, value, ok := strings.Cut(arg, "=")
		if !ok {
			return color, fmt.Errorf("unknown help argument %q", arg)
		}
		switch key {
		case "color":
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return color, fmt.Errorf("color: want bool, got %q", value)
			}
			color = parsed
		default:
			return color, fmt.Errorf("unknown help argument %q", key)
		}
	}
	return color, nil
}

func resolveSet(ctx context.Context, opts options, evalDynamicVars bool) (map[string]recipe.Recipe, configfile.Loaded, string, error) {
	cwd := mustGetwd()
	loaded := configfile.Loaded{}
	if opts.configPath != "" {
		cfg, err := configfile.Load(opts.configPath)
		if err != nil {
			return nil, loaded, "", err
		}
		loaded = cfg
	} else if cfg, ok, err := configfile.Find(cwd); err != nil {
		return nil, loaded, "", err
	} else if ok {
		loaded = cfg
	}
	recipes, profile, err := configfile.ResolveRecipes(ctx, loaded, cwd, configfile.ResolveOptions{
		Profile:         opts.profile,
		EvalDynamicVars: evalDynamicVars,
	})
	if err != nil {
		return nil, loaded, "", err
	}
	return recipes, loaded, profile, nil
}

func runComplete(ctx context.Context, args []string) error {
	request, err := completion.ParseRequest(args)
	if err != nil {
		return err
	}
	if candidates, ok, err := request.StaticCandidates(); err != nil {
		return err
	} else if ok {
		return completion.WriteCandidates(os.Stdout, request.Shell, candidates)
	}
	opts := completionOptions(request.Words)
	recipes, loaded, _, err := resolveSet(ctx, opts, false)
	if err != nil {
		return nil
	}
	if opts.all {
		recipes = allScopeRecipes(recipes)
	}
	candidates, err := completion.Candidates(ctx, request.Shell, request.Words, recipes, completion.Options{
		Dir:        mustGetwd(),
		ConfigPath: loaded.Path,
		Env:        loaded.Config.Env,
		EnumSets:   loaded.Config.EnumSets,
	})
	if err != nil {
		return err
	}
	candidates = request.AdjustCandidates(candidates)
	return completion.WriteCandidates(os.Stdout, request.Shell, candidates)
}

func completionOptions(words []string) options {
	var opts options
	if len(words) <= 1 {
		return opts
	}
	args := words[1:]
	for i, boundary := 0, globalFlagBoundary(args); i < boundary; i++ {
		word := args[i]
		name, value, hasValue := strings.Cut(word, "=")
		switch name {
		case "--" + globalflag.Config:
			if hasValue {
				opts.configPath = value
				continue
			}
			if i+1 < len(args) {
				opts.configPath = args[i+1]
				i++
			}
		case "--" + globalflag.Profile:
			if hasValue {
				opts.profile = value
				continue
			}
			if i+1 < len(args) {
				opts.profile = args[i+1]
				i++
			}
		case "--" + globalflag.AllTargets:
			if !hasValue {
				opts.all = true
				continue
			}
			if enabled, err := strconv.ParseBool(value); err == nil {
				opts.all = enabled
			}
		}
	}
	return opts
}

func allScopeRecipes(recipes map[string]recipe.Recipe) map[string]recipe.Recipe {
	out := make(map[string]recipe.Recipe, len(recipes))
	for name, rec := range recipes {
		all, _, _, err := recipe.SelectAll(name, rec)
		if err == nil {
			out[name] = all
		}
	}
	return out
}

func printRecipes(w io.Writer, recipes map[string]recipe.Recipe) error {
	return printRecipeList(w, recipes, "")
}

func printRecipeList(w io.Writer, recipes map[string]recipe.Recipe, indent string) error {
	names := slices.Sorted(maps.Keys(recipes))
	nameColumn := recipeNameColumn(names)
	for _, name := range names {
		fmt.Fprintf(w, "%s%-*s%s\n", indent, nameColumn, name, recipe.Help(recipes[name]))
	}
	return nil
}

func recipeNameColumn(names []string) int {
	const minColumn = 13
	column := minColumn
	for _, name := range names {
		column = max(column, len(name)+2)
	}
	return column
}

func printConfig(w io.Writer, loaded configfile.Loaded, profile string, recipes map[string]recipe.Recipe) error {
	if loaded.Path != "" {
		fmt.Fprintf(w, "config: %s\n", loaded.Path)
	} else {
		fmt.Fprintln(w, "config: <none>")
	}
	if profile != "" {
		fmt.Fprintf(w, "profile: %s\n", profile)
	} else {
		fmt.Fprintln(w, "profile: <none>")
	}
	fmt.Fprintln(w, "recipes:")
	return printRecipeList(w, recipes, "  ")
}

func printHelp(w io.Writer, loaded configfile.Loaded, profile string, recipes map[string]recipe.Recipe) error {
	printBasicHelp(w)
	if loaded.Path != "" {
		fmt.Fprintf(w, "\nconfig: %s\n", loaded.Path)
	}
	if profile != "" {
		fmt.Fprintf(w, "profile: %s\n", profile)
	}
	fmt.Fprintln(w, "\nrecipes:")
	return printRecipes(w, recipes)
}

func printRecipeHelp(ctx context.Context, w io.Writer, name string, rec recipe.Recipe, opts recipeHelpOptions) error {
	colors := helpColors{enabled: opts.Color}
	fmt.Fprintf(w, "%s\n\n", colors.title(name))
	fmt.Fprintf(w, "  %s\n", recipeHelpText(rec))
	fmt.Fprintf(w, "\n%s\n\n", colors.section("- Command:"))
	fmt.Fprintf(w, "    %s\n", colors.literal(recipe.CommandSummary(rec)))
	domain, unsupported, supported := recipe.AllSupport(rec)
	fmt.Fprintf(w, "\n%s\n\n", colors.section("- All scope:"))
	if supported {
		fmt.Fprintf(w, "    %s\n", colors.literal(domain))
	} else {
		fmt.Fprintf(w, "    %s\n", colors.literal("unsupported: "+unsupported))
	}
	if mode := recipe.RecipeSandboxMode(rec); mode != recipe.SandboxModeWorkspace {
		fmt.Fprintf(w, "\n%s\n\n", colors.section("- Sandboxed:"))
		fmt.Fprintf(w, "    %s\n", colors.literal(string(mode)))
	}
	if rec.System != nil && rec.System.BaseImage != "" {
		fmt.Fprintf(w, "\n%s\n\n", colors.section("- System base image:"))
		fmt.Fprintf(w, "    %s\n", colors.literal(rec.System.BaseImage))
	}
	if !rec.Requires.Empty() {
		printRecipeRequirements(w, rec.Requires, colors)
	}
	if len(rec.Pre) > 0 {
		fmt.Fprintf(w, "\n%s\n\n", colors.section("- Pre commands:"))
		for i, command := range rec.Pre {
			fmt.Fprintf(w, "    %s %s\n", colors.index(i), colors.literal(recipe.StageCommandHelpText(command)))
		}
	}
	if len(rec.Post) > 0 {
		fmt.Fprintf(w, "\n%s\n\n", colors.section("- Post commands:"))
		for i, command := range rec.Post {
			fmt.Fprintf(w, "    %s %s\n", colors.index(i), colors.literal(recipe.StageCommandHelpText(command)))
		}
	}
	if len(rec.ForEach) > 0 {
		fmt.Fprintf(w, "\n%s\n\n", colors.section("- For each:"))
		fmt.Fprintf(w, "    %s\n", colors.literal(recipe.CommandHelpText(rec.ForEach)))
	}
	if rec.Workdir != "" {
		fmt.Fprintf(w, "\n%s\n\n", colors.section("- Workdir:"))
		fmt.Fprintf(w, "    %s\n", colors.literal(rec.Workdir))
	}
	if rec.Log != "" {
		fmt.Fprintf(w, "\n%s\n\n", colors.section("- Log:"))
		fmt.Fprintf(w, "    %s\n", colors.literal(rec.Log))
		if len(rec.LogStages) > 0 {
			fmt.Fprintf(w, "    %s %s\n", colors.label("stages:"), colors.literal(strings.Join(rec.LogStages, ",")))
		}
		if rec.LogTee != nil {
			fmt.Fprintf(w, "    %s %s\n", colors.label("tee:"), colors.literal(strconv.FormatBool(*rec.LogTee)))
		}
	}
	argNames := slices.Sorted(maps.Keys(rec.Arguments))
	if len(argNames) > 0 {
		fmt.Fprintf(w, "\n%s\n\n", colors.section("- Arguments:"))
	}
	for i, argName := range argNames {
		if i > 0 {
			fmt.Fprintln(w)
		}
		arg := rec.Arguments[argName]
		if arg.Help != "" {
			fmt.Fprintf(w, "    %s - %s\n", colors.argument(argName), strings.TrimSuffix(recipe.SingleLine(arg.Help), "."))
		} else {
			fmt.Fprintf(w, "    %s\n", colors.argument(argName))
		}
		fmt.Fprintln(w)
		fmt.Fprintf(w, "      %s ", colors.label("info:"))
		printArgumentInfo(w, arg, colors)
		fmt.Fprintln(w)
		if err := printArgumentValues(ctx, w, arg, rec, opts); err != nil {
			return fmt.Errorf("arg %s values: %w", argName, err)
		}
	}
	presetNames := slices.Sorted(maps.Keys(rec.Presets))
	if len(presetNames) > 0 {
		fmt.Fprintf(w, "\n%s\n\n", colors.section("- Presets:"))
	}
	presetNameWidth := 0
	for _, presetName := range presetNames {
		presetNameWidth = max(presetNameWidth, len(presetName))
	}
	for _, presetName := range presetNames {
		fmt.Fprintf(w, "    %s%s", colors.argument(presetName), strings.Repeat(" ", presetNameWidth-len(presetName)))
		argNames := slices.Sorted(maps.Keys(rec.Presets[presetName].Arguments))
		for _, argName := range argNames {
			fmt.Fprintf(w, " %s%s", colors.label(argName+"="), colors.literal(argumentScalarDisplayValue(rec.Presets[presetName].Arguments[argName])))
		}
		fmt.Fprintln(w)
	}
	if recipe.RecipeSandboxMode(rec) != recipe.SandboxModeHost {
		if len(rec.SyncOut) > 0 {
			fmt.Fprintf(w, "\n%s\n\n", colors.section("- Sync out:"))
			for _, path := range rec.SyncOut {
				fmt.Fprintf(w, "    %s\n", colors.literal(path))
			}
		}
	}
	return nil
}

func printRecipeRequirements(w io.Writer, req recipe.Requirements, colors helpColors) {
	fmt.Fprintf(w, "\n%s\n\n", colors.section("- Requires:"))
	if len(req.Commands) > 0 {
		fmt.Fprintf(w, "    %s %s\n", colors.label("commands:"), colors.literal(strings.Join(req.Commands, ", ")))
	}
	if len(req.OptionalCommands) > 0 {
		fmt.Fprintf(w, "    %s %s\n", colors.label("optional:"), colors.literal(strings.Join(req.OptionalCommands, ", ")))
	}
	if len(req.SystemPackages) > 0 {
		fmt.Fprintf(w, "    %s %s\n", colors.label("system:"), colors.literal(strings.Join(slices.Sorted(slices.Values(req.SystemPackages)), ", ")))
	}
	if len(req.GoCommands) > 0 {
		fmt.Fprintf(w, "    %s %s\n", colors.label("go:"), colors.literal(requirementMapHelpText(req.GoCommands)))
	}
	if len(req.NodeCommands) > 0 {
		fmt.Fprintf(w, "    %s %s\n", colors.label("node:"), colors.literal(requirementMapHelpText(req.NodeCommands)))
	}
}

func requirementMapHelpText(values map[string]string) string {
	parts := make([]string, 0, len(values))
	for _, name := range slices.Sorted(maps.Keys(values)) {
		parts = append(parts, fmt.Sprintf("%s (%s)", name, values[name]))
	}
	return strings.Join(parts, ", ")
}

func recipeHelpText(rec recipe.Recipe) string {
	if rec.Help == "" {
		return recipe.CommandSummary(rec)
	}
	return strings.TrimSuffix(recipe.SingleLine(rec.Help), ".")
}

func printArgumentInfo(w io.Writer, arg recipe.Argument, colors helpColors) {
	typeName := arg.Type
	if typeName == "" {
		typeName = "string"
	}
	fmt.Fprintf(w, "type=%s", colors.literal(typeName))
	if arg.PathKind != "" {
		fmt.Fprintf(w, " path_kind=%s", colors.literal(arg.PathKind))
	}
	if arg.Position > 0 {
		fmt.Fprintf(w, " position=%s", colors.literal(strconv.Itoa(arg.Position)))
	}
	if arg.Required {
		fmt.Fprintf(w, " %s", colors.literal("required"))
	}
	if arg.Default != nil {
		fmt.Fprintf(w, " default=%s", colors.literal(argumentScalarDisplayValue(arg.Default)))
	}
	if arg.Min != nil {
		fmt.Fprintf(w, " min=%s", colors.literal(argumentScalarDisplayValue(arg.Min)))
	}
	if arg.Max != nil {
		fmt.Fprintf(w, " max=%s", colors.literal(argumentScalarDisplayValue(arg.Max)))
	}
}

func argumentScalarDisplayValue(value any) string {
	if text, err := recipe.ScalarValueString(value); err == nil {
		if _, ok := value.(string); ok {
			return strconv.Quote(text)
		}
		return text
	}
	return fmt.Sprint(value)
}

func printArgumentValues(ctx context.Context, w io.Writer, arg recipe.Argument, rec recipe.Recipe, opts recipeHelpOptions) error {
	colors := helpColors{enabled: opts.Color}
	values, err := argumentValues(ctx, arg, rec, opts)
	if err != nil {
		fmt.Fprintf(w, "      %s <unavailable: %v>\n", colors.label("values:"), err)
		return nil
	}
	if len(values) == 0 {
		return nil
	}
	fmt.Fprintf(w, "      %s\n", colors.label("values:"))
	valueColumn := argumentValueColumn(values)
	for _, value := range values {
		if value.Help == "" {
			fmt.Fprintf(w, "        %s\n", colors.literal(value.Value))
			continue
		}
		fmt.Fprintf(w, "        %s%s\n", colors.literal(fmt.Sprintf("%-*s", valueColumn, value.Value)), value.Help)
	}
	return nil
}

func (colors helpColors) title(text string) string {
	return colors.color("\x1b[1;36m", text)
}

func (colors helpColors) section(text string) string {
	return colors.color("\x1b[1;33m", text)
}

func (colors helpColors) argument(text string) string {
	return colors.color("\x1b[1;32m", text)
}

func (colors helpColors) label(text string) string {
	return colors.color("\x1b[36m", text)
}

func (colors helpColors) literal(text string) string {
	return colors.color("\x1b[32m", text)
}

func (colors helpColors) index(index int) string {
	return colors.color("\x1b[2m", fmt.Sprintf("[%d]", index))
}

func (colors helpColors) color(code, text string) string {
	if !colors.enabled {
		return text
	}
	return code + text + "\x1b[0m"
}

func argumentValues(ctx context.Context, arg recipe.Argument, rec recipe.Recipe, opts recipeHelpOptions) ([]completion.Candidate, error) {
	if len(arg.Values) > 0 {
		if values, ok, err := recipe.BuiltinValues(arg.Values, recipe.ValueBuiltinOptions{
			Context:    ctx,
			Dir:        opts.Dir,
			ConfigPath: opts.ConfigPath,
			Recipe:     rec,
			Recipes:    opts.Recipes,
			EnumSets:   opts.EnumSets,
		}); ok {
			if err != nil {
				return nil, err
			}
			candidates := make([]completion.Candidate, 0, len(values))
			for _, value := range values {
				candidates = append(candidates, completion.Candidate{Value: value.Value, Help: value.Help})
			}
			return candidates, nil
		}
		command, err := recipe.CommandWithRecipeReferenceExpandedPrelude(arg.Values, rec.Shell, rec.ShellPrelude, rec.Vars)
		if err != nil {
			return nil, err
		}
		env := maps.Clone(opts.Env)
		if env == nil {
			env = map[string]string{}
		}
		maps.Copy(env, rec.Env)
		valueCtx, cancel := context.WithTimeout(ctx, completion.DefaultCommandBackedValueTimeout)
		defer cancel()
		output, err := runner.CommandOutput(valueCtx, opts.Dir, env, command, runner.CommandOutputOptions{
			Recipes:    opts.Recipes,
			EnumSets:   opts.EnumSets,
			ConfigPath: opts.ConfigPath,
			SourceDir:  opts.Dir,
		})
		if err != nil {
			return nil, err
		}
		return parseArgumentValues(output), nil
	}
	return nil, nil
}

func parseArgumentValues(output string) []completion.Candidate {
	parsed := recipe.ParseValueCandidates(output)
	values := make([]completion.Candidate, 0, len(parsed))
	for _, value := range parsed {
		values = append(values, completion.Candidate{Value: value.Value, Help: value.Help})
	}
	return values
}

func argumentValueColumn(values []completion.Candidate) int {
	column := 0
	for _, value := range values {
		column = max(column, len(value.Value)+2)
	}
	return column
}

func printBasicHelp(w io.Writer) {
	fmt.Fprint(w, `usage: shadowtree [flags] <recipe> [args...]
       shadowtree [flags] exec -- <cmd> [args...]
       shadowtree help [recipe [color=false]]
       shadowtree recipes
       shadowtree config
       shadowtree init [path]
       shadowtree completion bash|fish|zsh

flags:
`)
	for _, spec := range globalflag.All() {
		if spec.UsageHelp == "" {
			continue
		}
		fmt.Fprintf(w, "  --%-18s%s\n", spec.Usage(), spec.UsageHelp)
	}
}

func mustGetwd() string {
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return cwd
}
