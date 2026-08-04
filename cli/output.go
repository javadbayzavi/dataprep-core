// Output layer of the dataprep tools.
//
// Two streams, kept strictly apart so the tools compose in a pipeline:
//
//	stdout — results only (plain text, or one JSON document with -output=json)
//	stderr — diagnostics (warnings, verbose and debug lines)
//
// Nothing in here ever formats a credential: callers pass already redacted
// values.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// Format selects the rendering of results and diagnostics.
type Format string

const (
	FormatText Format = "text"
	FormatJSON Format = "json"
)

// ParseFormat validates a -output value.
func ParseFormat(s string) (Format, error) {
	switch Format(s) {
	case FormatText:
		return FormatText, nil
	case FormatJSON:
		return FormatJSON, nil
	default:
		return "", fmt.Errorf("unknown output format %q (want text or json)", s)
	}
}

// Logger writes diagnostics to stderr and results to stdout.
type Logger struct {
	out     io.Writer
	err     io.Writer
	format  Format
	verbose bool
	debug   bool
}

// NewLogger builds a logger over the given streams.
func NewLogger(out, errOut io.Writer) *Logger {
	return &Logger{out: out, err: errOut, format: FormatText}
}

func (l *Logger) SetFormat(f Format) { l.format = f }
func (l *Logger) SetVerbose(v bool)  { l.verbose = v }

// SetDebug turns on debug lines; debug implies verbose.
func (l *Logger) SetDebug(v bool) {
	l.debug = v
	if v {
		l.verbose = true
	}
}

func (l *Logger) Format() Format { return l.format }
func (l *Logger) IsJSON() bool   { return l.format == FormatJSON }
func (l *Logger) Out() io.Writer { return l.out }
func (l *Logger) Err() io.Writer { return l.err }

// Warnf reports a recoverable problem on stderr.
func (l *Logger) Warnf(format string, args ...any) {
	l.diag("warn", fmt.Sprintf(format, args...))
}

// Errorf reports a failure on stderr.
func (l *Logger) Errorf(format string, args ...any) {
	l.diag("error", fmt.Sprintf(format, args...))
}

// Verbosef prints only with -verbose (or -debug).
func (l *Logger) Verbosef(format string, args ...any) {
	if !l.verbose {
		return
	}
	l.diag("info", fmt.Sprintf(format, args...))
}

// Debugf prints only with -debug, as structured JSON lines.
func (l *Logger) Debugf(format string, args ...any) {
	if !l.debug {
		return
	}
	l.diag("debug", fmt.Sprintf(format, args...))
}

func (l *Logger) diag(level, msg string) {
	if l.format == FormatJSON || l.debug {
		line, err := json.Marshal(map[string]string{
			"ts":    time.Now().UTC().Format(time.RFC3339),
			"level": level,
			"msg":   msg,
		})
		if err == nil {
			fmt.Fprintf(l.err, "%s\n", line)
			return
		}
	}
	fmt.Fprintf(l.err, "%s: %s\n", level, msg)
}

// Result writes the outcome of a command to stdout: text as given, or data
// marshalled as JSON when -output=json.
func (l *Logger) Result(data any, text string, args ...any) error {
	if l.format == FormatJSON {
		enc := json.NewEncoder(l.out)
		enc.SetIndent("", "  ")
		return enc.Encode(data)
	}
	if text == "" {
		return nil
	}
	_, err := fmt.Fprintf(l.out, text+"\n", args...)
	return err
}

// Print writes a raw line to stdout, ignored in JSON mode so machine readable
// output stays a single document.
func (l *Logger) Print(format string, args ...any) {
	if l.format == FormatJSON {
		return
	}
	fmt.Fprintf(l.out, format+"\n", args...)
}
