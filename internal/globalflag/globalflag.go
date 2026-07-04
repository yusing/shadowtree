package globalflag

import "strings"

const (
	Config     = "config"
	Profile    = "profile"
	SyncOut    = "sync-out"
	SyncOutAll = "sync-out-all"
	Print      = "print"
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
	{Name: Profile, ValueName: "PROFILE", Help: "Use profile", UsageHelp: "use detected/profile built-ins, initially: go", FishOptions: "-x -a 'go'"},
	{Name: SyncOut, ValueName: "PATH", Help: "Copy path back after success", UsageHelp: "copy path back after success; repeatable or comma-separated", FishOptions: "-r"},
	{Name: SyncOutAll, Help: "Copy entire workspace back after success", UsageHelp: "copy entire workspace back after success"},
	{Name: Print, Help: "Print resolved plan without running", UsageHelp: "print resolved plan without running"},
	{Name: Verbose, Help: "Show commands and workspace paths", UsageHelp: "show commands and workspace paths"},
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
