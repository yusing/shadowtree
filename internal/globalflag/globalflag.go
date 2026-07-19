package globalflag

import "strings"

const (
	Config     = "config"
	Profile    = "profile"
	AllTargets = "all"
	SyncOut    = "sync-out"
	SyncOutAll = "sync-out-all"
	Print      = "print"
	Expanded   = "expanded"
	Check      = "check"
	Shell      = "shell"
	Verbose    = "verbose"
	Help       = "help"
	Version    = "version"
)

type Spec struct {
	Name        string
	ValueName   string
	Help        string
	UsageHelp   string
	FishOptions string
}

func (spec Spec) TakesValue() bool {
	return spec.ValueName != ""
}

func (spec Spec) Usage() string {
	if spec.ValueName == "" {
		return spec.Name
	}
	return spec.Name + " " + spec.ValueName
}

var specs = []Spec{
	{Name: Config, ValueName: "PATH", Help: "Use config file", UsageHelp: "use config file", FishOptions: "-r"},
	{Name: Profile, ValueName: "PROFILE", Help: "Use profile", UsageHelp: "use profile built-ins, supported: go, node, rust", FishOptions: "-x -a 'go node rust'"},
	{Name: AllTargets, Help: "Run the recipe for all supported targets", UsageHelp: "run for every target supported by the recipe"},
	{Name: SyncOut, ValueName: "PATH", Help: "Copy path back after success", UsageHelp: "copy path back after success; repeatable or comma-separated", FishOptions: "-r"},
	{Name: SyncOutAll, Help: "Copy entire workspace back after success", UsageHelp: "copy entire workspace back after success"},
	{Name: Print, Help: "Print resolved plan without running", UsageHelp: "print resolved plan without running"},
	{Name: Expanded, Help: "Print expanded scripts and resolved values", UsageHelp: "with --print, include expanded scripts and resolved values"},
	{Name: Check, Help: "Validate resolved recipe without running", UsageHelp: "validate resolved recipe without running"},
	{Name: Shell, Help: "Check expanded shell script syntax", UsageHelp: "with --check, parse expanded shell scripts"},
	{Name: Verbose, Help: "Show workspace paths and command boundaries", UsageHelp: "show workspace paths and command boundaries"},
	{Name: Help, Help: "Show help"},
	{Name: Version, Help: "Show version"},
}

func All() []Spec {
	return specs
}

func Lookup(name string) (Spec, bool) {
	name, _, _ = strings.Cut(name, "=")
	name = strings.TrimLeft(name, "-")
	for _, spec := range specs {
		if spec.Name == name {
			return spec, true
		}
	}
	return Spec{}, false
}

func TakesValue(arg string) bool {
	spec, ok := Lookup(arg)
	return ok && spec.TakesValue()
}
