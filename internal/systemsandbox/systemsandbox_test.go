package systemsandbox

import (
	"slices"
	"testing"
)

func TestRuntimeCandidatesHaveStableOrderAndIndependentStorage(t *testing.T) {
	want := []RuntimeName{Docker, Podman, Nerdctl}
	got := RuntimeCandidates()
	if !slices.Equal(got, want) {
		t.Fatalf("RuntimeCandidates() = %#v, want %#v", got, want)
	}
	got[0] = Nerdctl
	if next := RuntimeCandidates(); !slices.Equal(next, want) {
		t.Fatalf("RuntimeCandidates() after caller mutation = %#v, want %#v", next, want)
	}
}
