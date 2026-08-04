// Interactive prompting: the fallback the tools use when a value
// was not passed as a flag. Every prompt is optional: the tools are fully
// usable non-interactively (flags or environment), which is what CI and
// `docker run` without -it need.
package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// Prompt asks questions on out and reads answers from in.
type Prompt struct {
	in  *bufio.Reader
	out io.Writer
}

func NewPrompt(in io.Reader, out io.Writer) *Prompt {
	return &Prompt{in: bufio.NewReader(in), out: out}
}

// Line asks for a plain value, returning fallback when the answer is empty.
func (r *Prompt) Line(label, fallback string) (string, error) {
	if fallback != "" {
		fmt.Fprintf(r.out, "%s [%s]: ", label, fallback)
	} else {
		fmt.Fprintf(r.out, "%s: ", label)
	}
	text, err := r.in.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return fallback, nil
	}
	return text, nil
}

// Secret asks for a value without echoing it, when the terminal allows it.
// The answer is never logged by the caller — it goes straight to the store.
func (r *Prompt) Secret(label string) (string, error) {
	fmt.Fprintf(r.out, "%s: ", label)
	restore := disableEcho()
	text, err := r.in.ReadString('\n')
	restore()
	fmt.Fprintln(r.out)
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimSpace(text), nil
}

// disableEcho turns terminal echo off via stty (present in busybox, so it also
// works in the alpine based images). It degrades to a no-op when there is no
// terminal, e.g. in CI.
func disableEcho() (restore func()) {
	noop := func() {}
	if runtime.GOOS == "windows" || !IsTerminal(os.Stdin) {
		return noop
	}
	cmd := exec.Command("stty", "-echo")
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return noop
	}
	return func() {
		c := exec.Command("stty", "echo")
		c.Stdin = os.Stdin
		_ = c.Run()
	}
}

// IsTerminal reports whether f is attached to a character device, which is how
// the tools decide if interactive prompting is possible at all.
func IsTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
