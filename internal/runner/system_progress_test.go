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
	if err := progress.Start("Detect container runtime"); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"base", "toolchains", "system-packages", "recipe-packages", "dependencies"} {
		for _, operation := range []systemsandbox.ImageBuildOperation{systemsandbox.ImageBuildStageLookup, systemsandbox.ImageBuildStageBuild, systemsandbox.ImageBuildStageVerify} {
			if err := progress.Image(base, systemsandbox.ImageBuildProgress{Operation: operation, StageName: stage}); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, operation := range []systemsandbox.ImageBuildOperation{systemsandbox.ImageBuildFinalLookup, systemsandbox.ImageBuildFinalTag, systemsandbox.ImageBuildFinalVerify} {
		if err := progress.Image(base, systemsandbox.ImageBuildProgress{Operation: operation}); err != nil {
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
		"Detect container runtime",
		"Check foundation image debian:13", "Build foundation image debian:13", "Verify foundation image debian:13",
		"Check toolchain image", "Build toolchain image", "Verify toolchain image",
		"Check system package image", "Build system package image", "Verify system package image",
		"Check recipe package image", "Build recipe package image", "Verify recipe package image",
		"Check dependency image", "Build dependency image", "Verify dependency image",
		"Check recipe image", "Publish recipe image", "Verify recipe image",
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
	if err := progress.Start("Check foundation image alpine:3.23"); err != nil {
		t.Fatal(err)
	}
	if err := progress.Start("Build dependency image"); err != nil {
		t.Fatal(err)
	}
	if err := progress.Succeed(); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	if strings.Count(got, "\n") != 1 {
		t.Fatalf("interactive progress emitted more than one persistent line: %q", got)
	}
	for _, want := range []string{"\r\x1b[2K", "Check foundation image alpine:3.23", "✔ Build dependency image"} {
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
		operation         systemsandbox.ImageBuildOperation
	}{
		{name: "future stage", stage: "sbom", operation: systemsandbox.ImageBuildStageBuild, want: "Build image stage sbom"},
		{name: "malformed stage", stage: "future\nstage\x1b[2J", operation: systemsandbox.ImageBuildStageBuild, want: "Build image stage future stage [2J"},
		{name: "unrelated collision", stage: "dependencies-copy", operation: systemsandbox.ImageBuildStageBuild, want: "Build image stage dependencies-copy"},
		{name: "future operation", stage: "sbom", operation: systemsandbox.ImageBuildOperation("future\noperation"), want: "Run image operation future operation for image stage sbom"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := systemsandbox.ImageBuildProgress{Operation: test.operation, StageName: test.stage}
			if got := cleanProgressLabel(imageBuildProgressLabel("base", event)); got != test.want {
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
