// Package cli is the minimal command layer every dataprep tool is built on.
// It replaces the cement framework of the Python prototype: an App owns
// global flags, help rendering and exit codes; a Command owns its own flags
// and the call into the operations layer.
//
// Deliberately stdlib only — the tools stay dependency free, build offline and
// start in a few milliseconds inside a container.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// POSIX style exit codes shared by all tools.
const (
	ExitOK    = 0
	ExitError = 1
	ExitUsage = 2
)

// UsageError makes a command exit with ExitUsage and print its usage.
type UsageError struct{ msg string }

func Usagef(format string, args ...any) error {
	return &UsageError{msg: fmt.Sprintf(format, args...)}
}

func (e *UsageError) Error() string { return e.msg }

// Context is handed to every command implementation.
type Context struct {
	Log     *Logger
	Globals *Globals
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
}

// Globals are the flags every tool understands, so the tools stay
// interchangeable and can later be folded into one umbrella CLI.
type Globals struct {
	ConfigDir string
	Profile   string
	Output    string
	Verbose   bool
	Debug     bool
	help      bool
}

// Env vars honoured by every tool.
const (
	EnvConfigDir = "DATAPREP_CONFIG_DIR"
	EnvProfile   = "DATAPREP_PROFILE"
	EnvOutput    = "DATAPREP_OUTPUT"
)

// Register binds the global flags onto fs.
func (g *Globals) Register(fs *flag.FlagSet) {
	fs.StringVar(&g.ConfigDir, "config-dir", os.Getenv(EnvConfigDir),
		"configuration directory (default: OS config dir, env "+EnvConfigDir+")")
	fs.StringVar(&g.Profile, "profile", os.Getenv(EnvProfile),
		"profile to operate on (default: the configured default profile, env "+EnvProfile+")")
	fs.StringVar(&g.Output, "output", envOr(EnvOutput, string(FormatText)),
		"output format: text or json")
	fs.BoolVar(&g.Verbose, "verbose", false, "print progress information on stderr")
	fs.BoolVar(&g.Debug, "debug", false, "print structured debug logs (JSON lines) on stderr")
	fs.BoolVar(&g.help, "help", false, "show this help and exit")
	fs.BoolVar(&g.help, "h", false, "show this help and exit")
}

// merge copies the global flags that were explicitly given after the sub
// command over the ones parsed before it.
func (g *Globals) merge(after *Globals, fs *flag.FlagSet) {
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "config-dir":
			g.ConfigDir = after.ConfigDir
		case "profile":
			g.Profile = after.Profile
		case "output":
			g.Output = after.Output
		case "verbose":
			g.Verbose = after.Verbose
		case "debug":
			g.Debug = after.Debug
		case "help", "h":
			g.help = after.help
		}
	})
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Command is one CLI verb.
type Command struct {
	Name     string
	Short    string
	Long     string
	Args     string // argument spec shown in usage, e.g. "<name>"
	Examples []string
	SetFlags func(fs *flag.FlagSet)
	Run      func(ctx *Context, args []string) error

	flags *flag.FlagSet
}

// App is one tool: its own binary, its own container image, its own help.
type App struct {
	Name     string
	Short    string
	Long     string
	Examples []string

	// Main runs when the tool is a single verb (init, connect, doctor).
	Main *Command
	// Commands are the sub verbs when the tool groups several (auth, config).
	Commands []*Command

	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Execute parses args (without the program name) and returns the exit code.
func (a *App) Execute(args []string) int {
	a.applyDefaults()

	globals := &Globals{}
	fs := a.newFlagSet(a.Name)
	globals.Register(fs)
	if a.Main != nil && a.Main.SetFlags != nil {
		a.Main.SetFlags(fs)
	}

	if err := fs.Parse(args); err != nil {
		return a.usageError(err)
	}
	if globals.help {
		a.PrintHelp(a.Stdout)
		return ExitOK
	}

	rest := fs.Args()

	// "version" is understood by every tool, with or without sub commands.
	if len(rest) > 0 && rest[0] == "version" {
		return a.runVersion(globals)
	}
	if len(rest) > 0 && (rest[0] == "help" || rest[0] == "--help") {
		return a.helpFor(rest[1:])
	}

	ctx, err := a.newContext(globals)
	if err != nil {
		return a.usageError(err)
	}

	if len(a.Commands) == 0 {
		if a.Main == nil {
			fmt.Fprintf(a.Stderr, "%s: no command implemented\n", a.Name)
			return ExitError
		}
		return a.finish(ctx, a.Main, a.Main.Run(ctx, rest))
	}

	if len(rest) == 0 {
		a.PrintHelp(a.Stdout)
		return ExitUsage
	}

	cmd := a.lookup(rest[0])
	if cmd == nil {
		fmt.Fprintf(a.Stderr, "%s: unknown command %q\n\n", a.Name, rest[0])
		a.PrintHelp(a.Stderr)
		return ExitUsage
	}

	// Global flags may appear on either side of the sub command. They are
	// registered a second time on the sub flag set, into a separate struct, and
	// only the ones actually given there override what came before it.
	sub := a.newFlagSet(a.Name + " " + cmd.Name)
	subGlobals := &Globals{}
	subGlobals.Register(sub)
	if cmd.SetFlags != nil {
		cmd.SetFlags(sub)
	}
	cmd.flags = sub
	if err := sub.Parse(rest[1:]); err != nil {
		return a.usageError(err)
	}
	globals.merge(subGlobals, sub)

	if globals.help {
		a.printCommandHelp(a.Stdout, cmd)
		return ExitOK
	}
	// Globals may have been re-read after the sub command, refresh the context.
	ctx, err = a.newContext(globals)
	if err != nil {
		return a.usageError(err)
	}
	return a.finish(ctx, cmd, cmd.Run(ctx, sub.Args()))
}

// Run executes with os.Args and exits the process.
func (a *App) Run() {
	os.Exit(a.Execute(os.Args[1:]))
}

func (a *App) applyDefaults() {
	if a.Stdin == nil {
		a.Stdin = os.Stdin
	}
	if a.Stdout == nil {
		a.Stdout = os.Stdout
	}
	if a.Stderr == nil {
		a.Stderr = os.Stderr
	}
}

func (a *App) newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	fs.Usage = func() { a.PrintHelp(a.Stderr) }
	return fs
}

func (a *App) newContext(g *Globals) (*Context, error) {
	format, err := ParseFormat(g.Output)
	if err != nil {
		return nil, err
	}
	log := NewLogger(a.Stdout, a.Stderr)
	log.SetFormat(format)
	log.SetVerbose(g.Verbose)
	log.SetDebug(g.Debug)
	return &Context{
		Log:     log,
		Globals: g,
		Stdin:   a.Stdin,
		Stdout:  a.Stdout,
		Stderr:  a.Stderr,
	}, nil
}

func (a *App) finish(ctx *Context, cmd *Command, err error) int {
	if err == nil {
		return ExitOK
	}
	var ue *UsageError
	if errors.As(err, &ue) {
		fmt.Fprintf(a.Stderr, "%s: %s\n\n", a.Name, ue.Error())
		if cmd != nil && len(a.Commands) > 0 {
			a.printCommandHelp(a.Stderr, cmd)
		} else {
			a.PrintHelp(a.Stderr)
		}
		return ExitUsage
	}
	ctx.Log.Errorf("%s", err.Error())
	return ExitError
}

func (a *App) usageError(err error) int {
	if err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintf(a.Stderr, "%s: %s\n", a.Name, err.Error())
	}
	if errors.Is(err, flag.ErrHelp) {
		a.PrintHelp(a.Stdout)
		return ExitOK
	}
	return ExitUsage
}

func (a *App) runVersion(g *Globals) int {
	info := Current(a.Name)
	ctx, err := a.newContext(g)
	if err != nil {
		return a.usageError(err)
	}
	if err := ctx.Log.Result(info, "%s", info.String()); err != nil {
		return ExitError
	}
	return ExitOK
}

func (a *App) lookup(name string) *Command {
	for _, c := range a.Commands {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func (a *App) helpFor(args []string) int {
	if len(args) == 0 {
		a.PrintHelp(a.Stdout)
		return ExitOK
	}
	cmd := a.lookup(args[0])
	if cmd == nil {
		fmt.Fprintf(a.Stderr, "%s: unknown command %q\n", a.Name, args[0])
		return ExitUsage
	}
	a.printCommandHelp(a.Stdout, cmd)
	return ExitOK
}

// PrintHelp renders the tool help screen.
func (a *App) PrintHelp(w io.Writer) {
	a.applyDefaults()

	fmt.Fprintf(w, "%s - %s\n\n", a.Name, a.Short)
	if len(a.Commands) > 0 {
		fmt.Fprintf(w, "Usage:\n  %s [flags] <command> [flags] [args]\n\n", a.Name)
	} else {
		args := ""
		if a.Main != nil && a.Main.Args != "" {
			args = " " + a.Main.Args
		}
		fmt.Fprintf(w, "Usage:\n  %s [flags]%s\n\n", a.Name, args)
	}

	if a.Long != "" {
		fmt.Fprintf(w, "%s\n\n", strings.TrimSpace(a.Long))
	}

	if len(a.Commands) > 0 {
		fmt.Fprintf(w, "Commands:\n")
		names := make([]*Command, len(a.Commands))
		copy(names, a.Commands)
		sort.Slice(names, func(i, j int) bool { return names[i].Name < names[j].Name })
		width := 0
		for _, c := range names {
			if len(c.Name) > width {
				width = len(c.Name)
			}
		}
		if width < len("version") {
			width = len("version")
		}
		for _, c := range names {
			fmt.Fprintf(w, "  %-*s  %s\n", width, c.Name, c.Short)
		}
		fmt.Fprintf(w, "  %-*s  %s\n", width, "version", "print version and build information")
		fmt.Fprintln(w)
	}

	fmt.Fprintf(w, "Flags:\n")
	fmt.Fprint(w, a.flagUsage(func(fs *flag.FlagSet) {
		if a.Main != nil && a.Main.SetFlags != nil {
			a.Main.SetFlags(fs)
		}
	}))

	if len(a.Examples) > 0 {
		fmt.Fprintf(w, "\nExamples:\n")
		for _, ex := range a.Examples {
			fmt.Fprintf(w, "  %s\n", ex)
		}
	}
	if len(a.Commands) > 0 {
		fmt.Fprintf(w, "\nRun '%s <command> --help' for command specific flags.\n", a.Name)
	} else {
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "Environment: %s, %s, %s\n", EnvConfigDir, EnvProfile, EnvOutput)
}

func (a *App) printCommandHelp(w io.Writer, cmd *Command) {
	args := ""
	if cmd.Args != "" {
		args = " " + cmd.Args
	}
	fmt.Fprintf(w, "%s %s - %s\n\n", a.Name, cmd.Name, cmd.Short)
	fmt.Fprintf(w, "Usage:\n  %s %s [flags]%s\n\n", a.Name, cmd.Name, args)
	if cmd.Long != "" {
		fmt.Fprintf(w, "%s\n\n", strings.TrimSpace(cmd.Long))
	}
	fmt.Fprintf(w, "Flags:\n")
	fmt.Fprint(w, a.flagUsage(func(fs *flag.FlagSet) {
		if cmd.SetFlags != nil {
			cmd.SetFlags(fs)
		}
	}))
	if len(cmd.Examples) > 0 {
		fmt.Fprintf(w, "\nExamples:\n")
		for _, ex := range cmd.Examples {
			fmt.Fprintf(w, "  %s\n", ex)
		}
	}
}

// flagUsage renders global flags plus whatever register adds, on a throwaway
// flag set so help never mutates parsed state.
func (a *App) flagUsage(register func(fs *flag.FlagSet)) string {
	var buf strings.Builder
	fs := flag.NewFlagSet("help", flag.ContinueOnError)
	fs.SetOutput(&buf)
	(&Globals{}).Register(fs)
	if register != nil {
		register(fs)
	}
	fs.PrintDefaults()
	return buf.String()
}
