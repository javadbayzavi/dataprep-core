package cli

import (
	"bytes"
	"errors"
	"flag"
	"strings"
	"testing"
)

func newTestApp(withCommands bool) (*App, *bytes.Buffer, *bytes.Buffer) {
	var out, errOut bytes.Buffer
	app := &App{
		Name:   "dataprep-test",
		Short:  "a test tool",
		Stdin:  strings.NewReader(""),
		Stdout: &out,
		Stderr: &errOut,
	}
	if withCommands {
		app.Commands = []*Command{
			{Name: "alpha", Short: "do alpha", Run: func(ctx *Context, args []string) error {
				return ctx.Log.Result(map[string]string{"ran": "alpha"}, "alpha ran with %d args", len(args))
			}},
			{Name: "beta", Short: "do beta", Run: func(ctx *Context, args []string) error {
				return errors.New("beta exploded")
			}},
		}
	} else {
		app.Main = &Command{Name: "main", Short: "do the thing", Run: func(ctx *Context, args []string) error {
			return ctx.Log.Result(map[string]int{"args": len(args)}, "ran with %d args", len(args))
		}}
	}
	return app, &out, &errOut
}

// Acceptance criterion 1 of the roadmap: --help lists the commands and exits 0.
func TestHelpListsCommandsAndExitsZero(t *testing.T) {
	for _, flagName := range []string{"--help", "-h", "help"} {
		app, out, _ := newTestApp(true)
		if code := app.Execute([]string{flagName}); code != ExitOK {
			t.Fatalf("%s exit code = %d, want 0", flagName, code)
		}
		text := out.String()
		for _, want := range []string{"Usage", "alpha", "beta", "version", "-help"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s output does not contain %q:\n%s", flagName, want, text)
			}
		}
	}
}

func TestHelpOfALeafToolHasNoCommandSection(t *testing.T) {
	app, out, _ := newTestApp(false)
	if code := app.Execute([]string{"--help"}); code != ExitOK {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if text := out.String(); strings.Contains(text, "Commands:") {
		t.Errorf("leaf tool help advertises a command list:\n%s", text)
	}
}

func TestNoArgsOnAGroupedToolPrintsHelpAndFailsUsage(t *testing.T) {
	app, out, _ := newTestApp(true)
	if code := app.Execute(nil); code != ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(out.String(), "Usage") {
		t.Error("no usage printed")
	}
}

func TestUnknownCommandExitsUsage(t *testing.T) {
	app, _, errOut := newTestApp(true)
	if code := app.Execute([]string{"gamma"}); code != ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(errOut.String(), `unknown command "gamma"`) {
		t.Errorf("stderr = %q", errOut.String())
	}
}

func TestCommandErrorExitsOne(t *testing.T) {
	app, _, errOut := newTestApp(true)
	if code := app.Execute([]string{"beta"}); code != ExitError {
		t.Fatalf("exit code = %d, want %d", code, ExitError)
	}
	if !strings.Contains(errOut.String(), "beta exploded") {
		t.Errorf("stderr = %q", errOut.String())
	}
}

func TestUsageErrorExitsTwo(t *testing.T) {
	var out, errOut bytes.Buffer
	app := &App{
		Name: "dataprep-test", Short: "s", Stdout: &out, Stderr: &errOut,
		Main: &Command{Name: "m", Run: func(ctx *Context, args []string) error {
			return Usagef("need a thing")
		}},
	}
	if code := app.Execute(nil); code != ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(errOut.String(), "need a thing") {
		t.Errorf("stderr = %q", errOut.String())
	}
}

func TestVersionIsUnderstoodByEveryTool(t *testing.T) {
	app, out, _ := newTestApp(true)
	if code := app.Execute([]string{"version"}); code != ExitOK {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "dataprep-test") {
		t.Errorf("version output = %q", out.String())
	}
}

func TestGlobalFlagsAfterTheSubCommand(t *testing.T) {
	app, out, _ := newTestApp(true)
	if code := app.Execute([]string{"alpha", "-output", "json"}); code != ExitOK {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), `"ran": "alpha"`) {
		t.Errorf("json output = %q", out.String())
	}
}

func TestBadOutputFormatIsAUsageError(t *testing.T) {
	app, _, errOut := newTestApp(false)
	if code := app.Execute([]string{"-output", "yaml"}); code != ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(errOut.String(), "unknown output format") {
		t.Errorf("stderr = %q", errOut.String())
	}
}

func TestCommandFlagsAreParsed(t *testing.T) {
	var out, errOut bytes.Buffer
	var name string
	app := &App{
		Name: "dataprep-test", Short: "s", Stdout: &out, Stderr: &errOut,
		Commands: []*Command{{
			Name:     "greet",
			Short:    "greet someone",
			SetFlags: func(fs *flag.FlagSet) { fs.StringVar(&name, "name", "", "who") },
			Run: func(ctx *Context, args []string) error {
				return ctx.Log.Result(nil, "hello %s", name)
			},
		}},
	}
	if code := app.Execute([]string{"greet", "-name", "javad"}); code != ExitOK {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "hello javad") {
		t.Errorf("stdout = %q", out.String())
	}
}

// Results belong on stdout and diagnostics on stderr, so the tools can be
// piped into each other.
func TestResultsAndDiagnosticsUseSeparateStreams(t *testing.T) {
	var out, errOut bytes.Buffer
	app := &App{
		Name: "dataprep-test", Short: "s", Stdout: &out, Stderr: &errOut,
		Main: &Command{Name: "m", Run: func(ctx *Context, args []string) error {
			ctx.Log.Verbosef("working")
			ctx.Log.Warnf("careful")
			return ctx.Log.Result(nil, "done")
		}},
	}
	if code := app.Execute([]string{"-verbose"}); code != ExitOK {
		t.Fatalf("exit code = %d", code)
	}
	if got := out.String(); got != "done\n" {
		t.Errorf("stdout = %q, want only the result", got)
	}
	if got := errOut.String(); !strings.Contains(got, "working") || !strings.Contains(got, "careful") {
		t.Errorf("stderr = %q, want the diagnostics", got)
	}
}

func TestDebugImpliesVerboseAndEmitsJSONLines(t *testing.T) {
	var out, errOut bytes.Buffer
	app := &App{
		Name: "dataprep-test", Short: "s", Stdout: &out, Stderr: &errOut,
		Main: &Command{Name: "m", Run: func(ctx *Context, args []string) error {
			ctx.Log.Debugf("inner state")
			ctx.Log.Verbosef("progress")
			return nil
		}},
	}
	if code := app.Execute([]string{"-debug"}); code != ExitOK {
		t.Fatalf("exit code = %d", code)
	}
	got := errOut.String()
	if !strings.Contains(got, `"level":"debug"`) || !strings.Contains(got, "inner state") {
		t.Errorf("stderr = %q, want a debug JSON line", got)
	}
	if !strings.Contains(got, "progress") {
		t.Errorf("stderr = %q, want -debug to imply -verbose", got)
	}
}

func TestParseFormat(t *testing.T) {
	for _, ok := range []string{"text", "json"} {
		if _, err := ParseFormat(ok); err != nil {
			t.Errorf("ParseFormat(%q): %v", ok, err)
		}
	}
	if _, err := ParseFormat("toml"); err == nil {
		t.Error("ParseFormat accepted toml")
	}
}
