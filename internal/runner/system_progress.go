package runner

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/yusing/shadowtree/internal/systemsandbox"
	"golang.org/x/term"
)

const systemProgressTick = 100 * time.Millisecond

var systemProgressFrames = [...]string{"⠋ ", "⠙ ", "⠹ ", "⠸ ", "⠼ ", "⠴ ", "⠦ ", "⠧ ", "⠇ ", "⠏ "}

type systemProgress struct {
	mu          sync.Mutex
	output      io.Writer
	interactive bool
	current     string
	started     time.Time
	frame       int
	err         error
	done        chan struct{}
	stopOnce    sync.Once
	wait        sync.WaitGroup
}

func newSystemProgress(output io.Writer) *systemProgress {
	progress := &systemProgress{
		output:      output,
		interactive: isTerminalWriter(output),
		done:        make(chan struct{}),
	}
	if progress.interactive {
		progress.wait.Go(progress.animate)
	}
	return progress
}

func isTerminalWriter(output io.Writer) bool {
	file, ok := output.(interface{ Fd() uintptr })
	return ok && term.IsTerminal(int(file.Fd()))
}

func (progress *systemProgress) animate() {
	ticker := time.NewTicker(systemProgressTick)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			progress.mu.Lock()
			if progress.err == nil && progress.current != "" {
				progress.frame++
				progress.renderLocked(systemProgressFrames[progress.frame%len(systemProgressFrames)], "")
			}
			progress.mu.Unlock()
		case <-progress.done:
			return
		}
	}
}

func (progress *systemProgress) Start(label string) error {
	label = cleanProgressLabel(label)
	progress.mu.Lock()
	defer progress.mu.Unlock()
	if progress.err != nil {
		return progress.err
	}
	if progress.current == label {
		return nil
	}
	progress.current = label
	progress.started = time.Now()
	progress.frame = 0
	if progress.interactive {
		progress.renderLocked(systemProgressFrames[0], "")
	} else {
		_, progress.err = fmt.Fprintln(progress.output, label)
	}
	return progress.err
}

func (progress *systemProgress) Stage(baseImage string, stage systemsandbox.ImageStage) error {
	return progress.Start(imageStageProgressLabel(baseImage, stage.Name))
}

func (progress *systemProgress) Succeed() error {
	return progress.finish("✔ ", "")
}

func (progress *systemProgress) Fail() error {
	return progress.finish("! ", "Failed")
}

func (progress *systemProgress) finish(icon, status string) error {
	progress.stopOnce.Do(func() { close(progress.done) })
	progress.wait.Wait()
	progress.mu.Lock()
	defer progress.mu.Unlock()
	if progress.err != nil {
		return progress.err
	}
	if progress.current == "" {
		return nil
	}
	if progress.interactive {
		progress.renderLocked(icon, status)
		if progress.err == nil {
			_, progress.err = fmt.Fprintln(progress.output)
		}
	} else if status != "" {
		_, progress.err = fmt.Fprintf(progress.output, "%s%s: %s\n", icon, progress.current, status)
	}
	return progress.err
}

func (progress *systemProgress) renderLocked(icon, status string) {
	elapsed := time.Since(progress.started).Round(100 * time.Millisecond)
	if elapsed < 0 {
		elapsed = 0
	}
	if status == "" {
		_, progress.err = fmt.Fprintf(progress.output, "\r\x1b[2K %s%s  %s", icon, progress.current, elapsed)
		return
	}
	_, progress.err = fmt.Fprintf(progress.output, "\r\x1b[2K %s%s  %s  %s", icon, progress.current, status, elapsed)
}

func imageStageProgressLabel(baseImage, stage string) string {
	switch stage {
	case "base":
		return "Image " + baseImage
	case "toolchains":
		return "Setup toolchains"
	case "system-packages":
		return "Setup system packages"
	case "recipe-packages":
		return "Setup recipe packages"
	case "dependencies":
		return "Setup dependencies"
	default:
		return "Setup image stage " + stage
	}
}

func cleanProgressLabel(label string) string {
	label = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, label)
	label = strings.Join(strings.Fields(label), " ")
	if label == "" {
		return "Container setup"
	}
	runes := []rune(label)
	if len(runes) > 120 {
		return string(runes[:119]) + "…"
	}
	return label
}
