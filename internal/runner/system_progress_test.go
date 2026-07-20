package runner

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/yusing/shadowtree/internal/systemsandbox"
)

func TestSystemProgressNonInteractiveWritesPlainStageLines(t *testing.T) {
	var output bytes.Buffer
	progress := newSystemProgress(&output)
	base := "debian:13"
	for _, stage := range []string{"base", "toolchains", "system-packages", "recipe-packages", "dependencies"} {
		if err := progress.Stage(base, systemsandbox.ImageStage{Name: stage}); err != nil {
			t.Fatal(err)
		}
	}
	if err := progress.Start("Setup build cache"); err != nil {
		t.Fatal(err)
	}
	if err := progress.Start("Setup workspace"); err != nil {
		t.Fatal(err)
	}
	if err := progress.Succeed(); err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"Image debian:13",
		"Setup toolchains",
		"Setup system packages",
		"Setup recipe packages",
		"Setup dependencies",
		"Setup build cache",
		"Setup workspace",
		"",
	}, "\n")
	if output.String() != want {
		t.Fatalf("progress output = %q, want %q", output.String(), want)
	}
	if strings.Contains(output.String(), "\x1b") || strings.Contains(output.String(), "\r") {
		t.Fatalf("redirected progress contains terminal controls: %q", output.String())
	}
}

func TestSystemProgressInteractiveReplacesOneTransientLine(t *testing.T) {
	var output bytes.Buffer
	progress := newSystemProgress(&output)
	progress.interactive = true
	if err := progress.Start("Image alpine:3.23"); err != nil {
		t.Fatal(err)
	}
	if err := progress.Start("Setup dependencies"); err != nil {
		t.Fatal(err)
	}
	if err := progress.Succeed(); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	if strings.Count(got, "\n") != 1 {
		t.Fatalf("interactive progress emitted more than one persistent line: %q", got)
	}
	for _, want := range []string{"\r\x1b[2K", "Image alpine:3.23", "✔ Setup dependencies"} {
		if !strings.Contains(got, want) {
			t.Fatalf("interactive progress missing %q: %q", want, got)
		}
	}
}

func TestSystemProgressFailureIconsHaveTrailingSpace(t *testing.T) {
	var redirected bytes.Buffer
	progress := newSystemProgress(&redirected)
	if err := progress.Start("Image alpine:3.23"); err != nil {
		t.Fatal(err)
	}
	if err := progress.Fail(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(redirected.String(), "! Image alpine:3.23: Failed\n") {
		t.Fatalf("redirected failure lacks spaced icon: %q", redirected.String())
	}

	var interactive bytes.Buffer
	progress = newSystemProgress(&interactive)
	progress.interactive = true
	if err := progress.Start("Image alpine:3.23"); err != nil {
		t.Fatal(err)
	}
	if err := progress.Fail(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(interactive.String(), "! Image alpine:3.23") {
		t.Fatalf("interactive failure lacks spaced icon: %q", interactive.String())
	}
}

func TestImageStageProgressLabelHandlesUnknownMalformedAndUnrelatedNames(t *testing.T) {
	tests := []struct {
		name, stage, want string
	}{
		{name: "future stage", stage: "sbom", want: "Setup image stage sbom"},
		{name: "malformed stage", stage: "future\nstage\x1b[2J", want: "Setup image stage future stage [2J"},
		{name: "unrelated collision", stage: "dependencies-copy", want: "Setup image stage dependencies-copy"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := cleanProgressLabel(imageStageProgressLabel("base", test.stage)); got != test.want {
				t.Fatalf("progress label = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSystemProgressReportsRenderingFailure(t *testing.T) {
	want := errors.New("write failed")
	progress := newSystemProgress(errorWriter{err: want})
	if err := progress.Start("Image base"); !errors.Is(err, want) {
		t.Fatalf("Start() error = %v, want %v", err, want)
	}
	if err := progress.Fail(); !errors.Is(err, want) {
		t.Fatalf("Fail() error = %v, want %v", err, want)
	}
}

type errorWriter struct{ err error }

func (writer errorWriter) Write([]byte) (int, error) { return 0, writer.err }
