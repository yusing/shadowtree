package systemsandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
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

func TestDirectStreamingCommandPreservesCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX shell")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh is not installed")
	}
	ctx, cancel := context.WithCancel(t.Context())
	progress := callbackWriter(func(p []byte) (int, error) {
		cancel()
		return len(p), nil
	})
	_, err = directStreamingCommand(ctx, progress, sh, "-c", "printf ready; exec sleep 30")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("directStreamingCommand error = %v, want context cancellation", err)
	}
}

type callbackWriter func([]byte) (int, error)

func (write callbackWriter) Write(p []byte) (int, error) {
	return write(p)
}

func TestDetectUsesStableOrderAndContinuesAfterUnusableRuntime(t *testing.T) {
	var calls []string
	var progress bytes.Buffer
	selected, err := detect(t.Context(), &progress, RuntimeCandidates(), func(_ context.Context, executable string, args ...string) ([]byte, error) {
		calls = append(calls, executable+" "+strings.Join(args, " "))
		if executable == string(Docker) {
			return []byte("daemon unavailable"), errors.New("exit status 1")
		}
		return successfulProbeOutput(args), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if selected.Name != Podman {
		t.Fatalf("runtime = %q, want %q", selected.Name, Podman)
	}
	if len(calls) < 2 || calls[0] != "docker info" || calls[1] != "podman info" {
		t.Fatalf("calls = %#v, want Docker then Podman", calls)
	}
	if strings.Contains(strings.Join(calls, "\n"), "nerdctl") {
		t.Fatalf("calls after usable Podman = %#v", calls)
	}
	for _, want := range []string{"detecting system runtime docker", "system runtime rejected: docker: engine reachability", "detecting system runtime podman", "selected system runtime podman"} {
		if !strings.Contains(progress.String(), want) {
			t.Fatalf("progress missing %q:\n%s", want, progress.String())
		}
	}
}

func TestDetectReportsEveryCandidateFailure(t *testing.T) {
	_, err := detect(t.Context(), io.Discard, RuntimeCandidates(), func(_ context.Context, executable string, _ ...string) ([]byte, error) {
		return []byte("cannot reach engine"), fmt.Errorf("%s failed", executable)
	})
	if err == nil {
		t.Fatal("detect succeeded, want aggregate failure")
	}
	for _, name := range RuntimeCandidates() {
		if !strings.Contains(err.Error(), string(name)+": engine reachability") {
			t.Fatalf("error missing %s diagnostic: %v", name, err)
		}
	}
}

func TestResolveConfinementPolicyHandlesRootlessMappingsAndSELinux(t *testing.T) {
	for _, test := range []struct {
		name     string
		runtime  RuntimeName
		security runtimeSecurity
		want     ConfinementPolicy
	}{
		{
			name: "rootless Podman keeps host identity", runtime: Podman,
			security: runtimeSecurity{rootless: true},
			want:     ConfinementPolicy{User: "1000:998", UserNamespace: "keep-id"},
		},
		{
			name: "rootless Docker uses mapped root and private relabelling", runtime: Docker,
			security: runtimeSecurity{rootless: true, selinux: true},
			want:     ConfinementPolicy{User: "0:0", SELinux: true},
		},
		{
			name: "rootful Docker preserves host identity", runtime: Docker,
			want: ConfinementPolicy{User: "1000:998"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy := confinementPolicy(test.runtime, test.security, 1000, 998)
			if policy != test.want {
				t.Fatalf("policy = %#v, want %#v", policy, test.want)
			}
		})
	}
}

func TestRuntimeProbeDoesNotRequireInapplicableConfinementFlags(t *testing.T) {
	_, err := probe(t.Context(), Docker, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		output := successfulProbeOutput(args)
		output = bytes.ReplaceAll(output, []byte("--userns"), nil)
		return bytes.ReplaceAll(output, []byte("--volume"), nil), nil
	})
	if err != nil {
		t.Fatalf("rootful non-SELinux probe rejected inapplicable flags: %v", err)
	}
}

func TestRuntimeSecurityInspectionRejectsIncompletePodmanState(t *testing.T) {
	_, err := inspectRuntimeSecurity(t.Context(), Podman, func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`{"host":{"security":{"rootless":true}}}`), nil
	})
	if err == nil || !strings.Contains(err.Error(), "omitted rootless or SELinux state") {
		t.Fatalf("inspectRuntimeSecurity error = %v, want incomplete-state rejection", err)
	}
}

func TestRuntimeSecurityInspectionRejectsMissingDockerState(t *testing.T) {
	_, err := inspectRuntimeSecurity(t.Context(), Docker, func(context.Context, string, ...string) ([]byte, error) {
		return []byte("null"), nil
	})
	if err == nil || !strings.Contains(err.Error(), "omitted security options") {
		t.Fatalf("inspectRuntimeSecurity error = %v, want missing-state rejection", err)
	}
}

func TestRuntimeProbeRequiresNeededLifecycleAndVolumeFlags(t *testing.T) {
	for _, missing := range []string{"--file", "--tag", "--label", "--platform", "--secret", "--build-arg", "--mount", "--read-only", "--user", "--name", "--interactive", "--attach", "--signal", "--force", "--filter", "--format"} {
		t.Run(missing, func(t *testing.T) {
			_, err := probe(t.Context(), Docker, func(_ context.Context, _ string, args ...string) ([]byte, error) {
				output := successfulProbeOutput(args)
				return bytes.ReplaceAll(output, []byte(missing), nil), nil
			})
			if err == nil || !strings.Contains(err.Error(), "lacks exact option "+missing) {
				t.Fatalf("probe error = %v, want missing flag", err)
			}
		})
	}
}

func TestRuntimeProbeRequiresOnlyApplicableConfinementFlags(t *testing.T) {
	for _, test := range []struct {
		name, missing string
		runtime       RuntimeName
		security      []byte
	}{
		{name: "rootless Podman user namespace", missing: "--userns", runtime: Podman, security: []byte(`{"host":{"security":{"rootless":true,"selinuxEnabled":false}}}`)},
		{name: "SELinux private volume relabelling", missing: "--volume", runtime: Docker, security: []byte(`["name=selinux"]`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := probe(t.Context(), test.runtime, func(_ context.Context, _ string, args ...string) ([]byte, error) {
				if slices.Equal(args, []string{"info", "--format", "{{json .SecurityOptions}}"}) || slices.Equal(args, []string{"info", "--format", "json"}) {
					return test.security, nil
				}
				output := successfulProbeOutput(args)
				return bytes.ReplaceAll(output, []byte(test.missing), nil), nil
			})
			if err == nil || !strings.Contains(err.Error(), "lacks exact option "+test.missing) {
				t.Fatalf("probe error = %v, want missing applicable flag", err)
			}
		})
	}
}

func TestRuntimeProbeRejectsPrefixCollisionOptions(t *testing.T) {
	for required, collision := range map[string]string{"--label": "--label-file", "--user": "--userns"} {
		t.Run(required, func(t *testing.T) {
			_, err := probe(t.Context(), Docker, func(_ context.Context, _ string, args ...string) ([]byte, error) {
				return bytes.ReplaceAll(successfulProbeOutput(args), []byte(required), []byte(collision)), nil
			})
			if err == nil || !strings.Contains(err.Error(), "lacks exact option "+required) {
				t.Fatalf("probe error = %v, want exact-option rejection", err)
			}
		})
	}
}

func TestDetectPropagatesCancellationWithoutTryingLaterCandidates(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	want := errors.New("stop detection")
	var calls int
	_, err := detect(ctx, io.Discard, []RuntimeName{Docker, Podman}, func(context.Context, string, ...string) ([]byte, error) {
		calls++
		cancel(want)
		return nil, context.Canceled
	})
	if !errors.Is(err, want) {
		t.Fatalf("detect error = %v, want %v", err, want)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestDirectCommandBoundsCapturedOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires sh")
	}
	output, err := directCommand(t.Context(), "sh", "-c", "i=0; while [ $i -lt 10000 ]; do printf 1234567890; i=$((i+1)); done")
	if err != nil {
		t.Fatal(err)
	}
	if len(output) != probeOutputLimit {
		t.Fatalf("output length = %d, want %d", len(output), probeOutputLimit)
	}
}

func TestDirectCommandBoundsInheritedPipeWait(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires sh")
	}
	started := time.Now()
	_, err := directCommand(t.Context(), "sh", "-c", "(sleep 2) &")
	if !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("directCommand error = %v, want %v", err, exec.ErrWaitDelay)
	}
	if elapsed := time.Since(started); elapsed > 1500*time.Millisecond {
		t.Fatalf("directCommand waited %s, want bounded wait", elapsed)
	}
}

func successfulProbeOutput(args []string) []byte {
	if slices.Equal(args, []string{"build", "--help"}) {
		return []byte("--file --tag --label --platform --secret --build-arg")
	}
	if slices.Equal(args, []string{"volume", "create", "--help"}) {
		return []byte("--label")
	}
	if slices.Equal(args, []string{"volume", "inspect", "--help"}) {
		return []byte("--format")
	}
	if slices.Equal(args, []string{"volume", "ls", "--help"}) || slices.Equal(args, []string{"ps", "--help"}) {
		return []byte("--filter --format")
	}
	if slices.Equal(args, []string{"create", "--help"}) {
		return []byte("--mount --volume --read-only --user --userns --platform --name --interactive")
	}
	if slices.Equal(args, []string{"info", "--format", "{{json .SecurityOptions}}"}) {
		return []byte("[]")
	}
	if slices.Equal(args, []string{"info", "--format", "json"}) {
		return []byte(`{"host":{"security":{"rootless":false,"selinuxEnabled":false}}}`)
	}
	if slices.Equal(args, []string{"start", "--help"}) {
		return []byte("--attach --interactive")
	}
	if slices.Equal(args, []string{"kill", "--help"}) {
		return []byte("--signal")
	}
	if slices.Equal(args, []string{"rm", "--help"}) {
		return []byte("--force")
	}
	return []byte("ok")
}
