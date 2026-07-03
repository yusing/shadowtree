package runner

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == OverlayHelperCommand {
		os.Exit(OverlayHelperMain(os.Args[2:]))
	}
	os.Exit(m.Run())
}
