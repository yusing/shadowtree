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
	"strings"
	"syscall"
	"time"

	"github.com/yusing/shadowtree/internal/completion"
	"github.com/yusing/shadowtree/internal/configfile"
	"github.com/yusing/shadowtree/internal/recipe"
	"github.com/yusing/shadowtree/internal/runner"
)

const (
	version               = "0.1.0"
	argumentValuesTimeout = 5 * time.Second
)

type options struct {
	configPath string
	profile    string
	syncOut    multiFlag
	syncOutAll bool
	printOnly  bool
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
	if len(os.Args) > 1 && os.Args[1] == runner.OverlayHelperCommand {
		os.Exit(runner.OverlayHelperMain(os.Args[2:]))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
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
		printBasicHelp(os.Stdout)
		return nil
	}
	switch rest[0] {
	case "completion":
		if len(rest) != 2 {
			return errors.New("usage: shadowtree completion <shell>")
		}
		return completion.Script(os.Stdout, rest[1])
	case "__complete":
		return runComplete(rest[1:])
	case "init":
		path := ".shadowtree.toml"
		if len(rest) > 1 {
			path = rest[1]
		}
		return configfile.Init(path)
	}
	resolvedSet, loaded, profile, err := resolveSet(ctx, opts, true)
	if err != nil {
		return err
	}
	switch rest[0] {
	case "help":
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
			})
		}
		return printHelp(os.Stdout, loaded, profile, resolvedSet)
	case "recipes":
		return printRecipes(os.Stdout, resolvedSet)
	case "config":
		return printConfig(os.Stdout, loaded, profile, resolvedSet)
	case "run":
		command := rest[1:]
		if len(command) > 0 && command[0] == "--" {
			command = command[1:]
		}
		if len(command) == 0 {
			return errors.New("run requires a command")
		}
		rec := recipe.Recipe{Cmd: recipe.Command(command)}
		resolved, err := recipe.Resolve("run", rec, nil, opts.syncOut, loaded.Config.Env, loaded.Path, profile)
		if err != nil {
			return err
		}
		return runner.Run(ctx, runner.Options{
			Resolved:   resolved,
			SourceDir:  mustGetwd(),
			PrintOnly:  opts.printOnly,
			Verbose:    opts.verbose,
			SyncOutAll: opts.syncOutAll,
			Stdin:      os.Stdin,
			Stdout:     os.Stdout,
			Stderr:     os.Stderr,
		})
	default:
		name, recipeArgs := recipe.Invocation(rest)
		rec, ok := resolvedSet[name]
		if !ok {
			return fmt.Errorf("unknown recipe: %s", name)
		}
		resolved, err := recipe.Resolve(name, rec, recipeArgs, opts.syncOut, loaded.Config.Env, loaded.Path, profile)
		if err != nil {
			return err
		}
		return runner.Run(ctx, runner.Options{
			Resolved:   resolved,
			Recipes:    resolvedSet,
			SourceDir:  mustGetwd(),
			PrintOnly:  opts.printOnly,
			Verbose:    opts.verbose,
			SyncOutAll: opts.syncOutAll,
			Stdin:      os.Stdin,
			Stdout:     os.Stdout,
			Stderr:     os.Stderr,
		})
	}
}

func parseGlobal(args []string) (options, []string, error) {
	var opts options
	flags := flag.NewFlagSet("shadowtree", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.StringVar(&opts.configPath, "config", "", "use config file")
	flags.StringVar(&opts.profile, "profile", "", "use profile")
	flags.Var(&opts.syncOut, "sync-out", "copy path back after success")
	flags.BoolVar(&opts.syncOutAll, "sync-out-all", false, "copy entire workspace back after success")
	flags.BoolVar(&opts.printOnly, "print", false, "print resolved plan without running")
	flags.BoolVar(&opts.verbose, "verbose", false, "show commands and workspace paths")
	flags.BoolVar(&opts.help, "help", false, "show help")
	flags.BoolVar(&opts.showVer, "version", false, "show version")
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			if err := flags.Parse(args[:i]); err != nil {
				return opts, nil, err
			}
			return opts, args[i+1:], nil
		}
		if globalFlagTakesValue(arg) {
			if !strings.Contains(arg, "=") {
				i++
			}
			continue
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			if err := flags.Parse(args[:i]); err != nil {
				return opts, nil, err
			}
			return opts, args[i:], nil
		}
	}
	if err := flags.Parse(args); err != nil {
		return opts, nil, err
	}
	return opts, flags.Args(), nil
}

func globalFlagTakesValue(arg string) bool {
	name, _, _ := strings.Cut(arg, "=")
	switch name {
	case "-config", "--config", "-profile", "--profile", "-sync-out", "--sync-out":
		return true
	default:
		return false
	}
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

func runComplete(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: shadowtree __complete <shell> <words...>")
	}
	shell := args[0]
	words := args[1:]
	opts := completionOptions(words)
	recipes, loaded, _, err := resolveSet(context.Background(), opts, false)
	if err != nil {
		return nil
	}
	candidates, err := completion.Candidates(context.Background(), shell, words, recipes, completion.Options{
		Dir:        mustGetwd(),
		ConfigPath: loaded.Path,
		Env:        loaded.Config.Env,
	})
	if err != nil {
		return err
	}
	return completion.WriteCandidates(os.Stdout, shell, candidates)
}

func completionOptions(words []string) options {
	var opts options
	for i := 0; i < len(words); i++ {
		switch words[i] {
		case "--config":
			if i+1 < len(words) {
				opts.configPath = words[i+1]
				i++
			}
		case "--profile":
			if i+1 < len(words) {
				opts.profile = words[i+1]
				i++
			}
		}
	}
	return opts
}

func printRecipes(w io.Writer, recipes map[string]recipe.Recipe) error {
	return printRecipeList(w, recipes, "")
}

func printRecipeList(w io.Writer, recipes map[string]recipe.Recipe, indent string) error {
	names := mapsKeys(recipes)
	slices.Sort(names)
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
	fmt.Fprintf(w, "%s\n", name)
	fmt.Fprintf(w, "  %s\n", recipe.Help(rec))
	fmt.Fprintf(w, "\ncommand: %s\n", recipe.CommandSummary(rec))
	if !recipe.RecipeSandboxed(rec) {
		fmt.Fprintln(w, "sandboxed: false")
	}
	for i, command := range rec.Pre {
		fmt.Fprintf(w, "pre[%d]: %s\n", i, recipe.CommandHelpText(command))
	}
	for i, command := range rec.Post {
		fmt.Fprintf(w, "post[%d]: %s\n", i, recipe.CommandHelpText(command))
	}
	argNames := mapsKeys(rec.Arguments)
	slices.Sort(argNames)
	for _, argName := range argNames {
		arg := rec.Arguments[argName]
		fmt.Fprintf(w, "arg %s", argName)
		if arg.Type != "" {
			fmt.Fprintf(w, ":%s", arg.Type)
		} else {
			fmt.Fprint(w, ":string")
		}
		if arg.PathKind != "" {
			fmt.Fprintf(w, " path_kind=%s", arg.PathKind)
		}
		if arg.Position > 0 {
			fmt.Fprintf(w, " position=%d", arg.Position)
		}
		if arg.Required {
			fmt.Fprint(w, " required")
		}
		if arg.Default != nil {
			fmt.Fprintf(w, " default=%v", arg.Default)
		}
		fmt.Fprintf(w, "  %s\n", recipe.ArgumentHelp(arg))
		if err := printArgumentValues(ctx, w, arg, rec, opts); err != nil {
			return fmt.Errorf("arg %s values: %w", argName, err)
		}
	}
	if recipe.RecipeSandboxed(rec) {
		for _, path := range rec.SyncOut {
			fmt.Fprintf(w, "sync_out: %s\n", path)
		}
	}
	return nil
}

func printArgumentValues(ctx context.Context, w io.Writer, arg recipe.Argument, rec recipe.Recipe, opts recipeHelpOptions) error {
	values, err := argumentValues(ctx, arg, rec, opts)
	if err != nil {
		fmt.Fprintf(w, "  values: <unavailable: %v>\n", err)
		return nil
	}
	if len(values) == 0 {
		return nil
	}
	fmt.Fprintln(w, "  values:")
	valueColumn := argumentValueColumn(values)
	for _, value := range values {
		if value.Help == "" {
			fmt.Fprintf(w, "    %s\n", value.Value)
			continue
		}
		fmt.Fprintf(w, "    %-*s%s\n", valueColumn, value.Value, value.Help)
	}
	return nil
}

func argumentValues(ctx context.Context, arg recipe.Argument, rec recipe.Recipe, opts recipeHelpOptions) ([]completion.Candidate, error) {
	if len(arg.Values) > 0 {
		command := recipe.CommandWithRecipeReference(arg.Values, rec.Shell, rec.ShellPrelude)
		env := maps.Clone(opts.Env)
		if env == nil {
			env = map[string]string{}
		}
		maps.Copy(env, rec.Env)
		valueCtx, cancel := context.WithTimeout(ctx, argumentValuesTimeout)
		defer cancel()
		output, err := runner.CommandOutput(valueCtx, opts.Dir, env, command, runner.CommandOutputOptions{
			Recipes:    opts.Recipes,
			ConfigPath: opts.ConfigPath,
			SourceDir:  opts.Dir,
		})
		if err != nil {
			return nil, err
		}
		return parseArgumentValues(output), nil
	}
	if arg.Type != "bool" {
		return nil, nil
	}
	return []completion.Candidate{
		{Value: "true", Help: "bool"},
		{Value: "false", Help: "bool"},
	}, nil
}

func parseArgumentValues(output string) []completion.Candidate {
	var values []completion.Candidate
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		value, help, _ := strings.Cut(line, "\t")
		values = append(values, completion.Candidate{
			Value: value,
			Help:  help,
		})
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
       shadowtree [flags] run -- <cmd> [args...]
       shadowtree help [recipe]
       shadowtree recipes
       shadowtree init [path]
       shadowtree completion fish

flags:
  --config PATH       use config file
  --profile PROFILE   use detected/profile built-ins, initially: go
  --sync-out PATH     copy path back after success; repeatable or comma-separated
  --sync-out-all      copy entire workspace back after success
  --print             print resolved plan without running
  --verbose           show commands and workspace paths
`)
}

func mustGetwd() string {
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return cwd
}

func mapsKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}
