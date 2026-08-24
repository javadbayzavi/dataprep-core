# dataprep-core

The shared library behind the [dataprep](https://github.com/javadbayzavi/data-perp)
tools — a set of small, containerized data preparation CLIs that each ship as
their own binary and their own image.

```go
import "github.com/javadbayzavi/dataprep-core/cli"
```

Standard library only. Nothing to fetch, nothing to vendor, and a container
that starts in milliseconds.

## Packages

| Package | Owns | Depends on |
|---|---|---|
| `cli` | flag parsing, help, exit codes, the stdout/stderr split, version, prompts | nothing |
| `workspace` | where the config lives, `config.json`, profiles, data sources | nothing |
| `auth` | credential file format and permissions, login/status/logout | `workspace` |
| `probe` | reachability checks over HTTP and TCP | `workspace`, `auth` |
| `doctor` | read-only health checks | `workspace`, `auth` |

Dependencies only ever point down that table.

## cli

Every dataprep tool is a `cli.App`: it owns the global flags, the help screen
and the exit codes, so the tools stay interchangeable and can later be folded
behind one umbrella command.

```go
app := &cli.App{
    Name:  "dataprep-example",
    Short: "do one thing",
    Main: &cli.Command{
        SetFlags: func(fs *flag.FlagSet) { fs.StringVar(&target, "target", "", "what to act on") },
        Run: func(ctx *cli.Context, args []string) error {
            return ctx.Log.Result(result, "acted on %s", target)
        },
    },
}
os.Exit(app.Execute(os.Args[1:]))
```

What you get for free:

- **Global flags** — `-config-dir`, `-profile`, `-output`, `-verbose`, `-debug`,
  `-h/--help`, honouring `DATAPREP_CONFIG_DIR`, `DATAPREP_PROFILE` and
  `DATAPREP_OUTPUT`. They work before *or* after a sub command.
- **Two streams** — results on stdout, diagnostics on stderr, so tools compose
  in a pipeline. With `-output json` stdout is a single JSON document.
- **POSIX exit codes** — `0` success, `1` error, `2` usage. Return
  `cli.Usagef(...)` for the last one.
- **Flags after positionals** — `cmd a.json b.json -markdown` works, because
  that is how people type. `--` ends parsing; a bare `-` stays an operand.
- **`version`** — understood by every tool, filled in through `-ldflags`.

## workspace and auth

One directory, chosen by OS convention (`$XDG_CONFIG_HOME/dataprep`,
`~/.config/dataprep`, `%APPDATA%\dataprep`, or `$DATAPREP_CONFIG_DIR`), holding
`config.json`, `credentials.json` and `profiles/` — all owner-only. Separate
containers cooperate by sharing that one directory as a mounted volume.

Secrets have one door: only `auth` reads and writes the credential file, and it
hands back masked values (`****abcd`). A tool cannot print a secret by accident
because it never holds one.

## Versioning

Tagged releases; consumers pin a version:

```bash
go get github.com/javadbayzavi/dataprep-core@v0.1.0
```

The source of truth is the `core/` directory of the
[data-perp](https://github.com/javadbayzavi/data-perp) monorepo, mirrored here
with its history on each release. Open issues and pull requests there.

## Build

```bash
go test ./...
go vet ./...
```

Requires Go 1.25.

---

<sub>This file lives at `core/README.md` in
[data-perp](https://github.com/javadbayzavi/data-perp) and is mirrored here, so
that publishing a release is a fast-forward and never a rewrite.</sub>
