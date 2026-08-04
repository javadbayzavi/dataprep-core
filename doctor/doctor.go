// Package doctor runs the health checks of the workspace: is it there, is
// it readable, are the permissions safe, is the profile usable. It never
// mutates anything.
package doctor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/javadbayzavi/dataprep-core/auth"
	"github.com/javadbayzavi/dataprep-core/workspace"
)

// Status of a single check.
type Status string

const (
	StatusOK   Status = "ok"
	StatusWarn Status = "warn"
	StatusFail Status = "fail"
)

// Check is one diagnostic line.
type Check struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`
}

// Report is the whole run.
type Report struct {
	Healthy   bool    `json:"healthy"`
	ConfigDir string  `json:"config_dir"`
	Profile   string  `json:"profile,omitempty"`
	Checks    []Check `json:"checks"`
}

// Run performs every check and reports whether the workspace is healthy.
// A warn does not make the report unhealthy; a fail does.
func Run(opts workspace.Ref) *Report {
	dir := workspace.ResolveDir(opts.ConfigDir)
	report := &Report{
		ConfigDir: dir,
		Checks:    []Check{},
	}
	add := func(c Check) { report.Checks = append(report.Checks, c) }

	// 1. config directory
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		add(Check{"config-dir", StatusFail, dir + " does not exist", "run dataprep-init"})
		report.Healthy = false
		return report
	case err != nil:
		add(Check{"config-dir", StatusFail, err.Error(), "check the mount and its ownership"})
		report.Healthy = false
		return report
	case !info.IsDir():
		add(Check{"config-dir", StatusFail, dir + " is not a directory", "remove the file and run dataprep-init"})
		report.Healthy = false
		return report
	default:
		add(Check{"config-dir", StatusOK, dir + " exists", ""})
	}

	// 2. config directory permissions
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			add(Check{"config-dir-permissions", StatusWarn,
				fmt.Sprintf("%s is %#o, readable beyond the owner", dir, perm),
				fmt.Sprintf("chmod %#o %s", workspace.DirPerm, dir)})
		} else {
			add(Check{"config-dir-permissions", StatusOK, fmt.Sprintf("%#o", info.Mode().Perm()), ""})
		}
	}

	// 3. config file
	cfg, err := workspace.Load(dir)
	if err != nil {
		status, hint := StatusFail, "check the file, or re-run dataprep-init"
		if errors.Is(err, workspace.ErrNotInitialized) {
			hint = "run dataprep-init"
		}
		add(Check{"config-file", status, err.Error(), hint})
		report.Healthy = false
		return report
	}
	add(Check{"config-file", StatusOK,
		fmt.Sprintf("%s parsed, schema %s, %d profile(s)", workspace.FilePath(dir), cfg.SchemaVersion, len(cfg.Profiles)), ""})

	// 4. profile
	profile, err := cfg.ResolveProfile(opts.Profile)
	if err != nil {
		add(Check{"profile", StatusFail, err.Error(), "run dataprep-init -profile <name>"})
		report.Healthy = false
	} else {
		report.Profile = profile.Name
		add(Check{"profile", StatusOK,
			fmt.Sprintf("%q (default: %q)", profile.Name, cfg.DefaultProfile), ""})

		// 5. workspace
		if profile.Workspace == "" {
			add(Check{"workspace", StatusWarn, "no workspace directory configured",
				"dataprep-init -profile " + profile.Name + " -workspace <dir> -force"})
		} else {
			add(checkWritable(profile.Workspace))
		}

		// 6. sources
		if len(profile.Sources) == 0 {
			add(Check{"sources", StatusWarn, "no data source configured",
				"dataprep-config set-source -name <name> -endpoint <url>"})
		} else {
			add(Check{"sources", StatusOK,
				fmt.Sprintf("%d configured: %v", len(profile.Sources), profile.SourceNames()), ""})
		}
	}

	// 7. credentials file
	store := auth.NewStore(dir)
	ok, mode, err := store.CheckPermissions()
	switch {
	case errors.Is(err, os.ErrNotExist):
		add(Check{"credentials-file", StatusWarn, store.Path() + " does not exist",
			"it is created by dataprep-init, or on first dataprep-auth login"})
	case err != nil:
		add(Check{"credentials-file", StatusFail, err.Error(), ""})
		report.Healthy = false
	case !ok:
		add(Check{"credentials-file", StatusFail,
			fmt.Sprintf("%s is %#o, readable beyond the owner", store.Path(), mode),
			fmt.Sprintf("chmod %#o %s", workspace.FilePerm, store.Path())})
		report.Healthy = false
	default:
		add(Check{"credentials-file", StatusOK, fmt.Sprintf("%s is %#o", store.Path(), mode), ""})
	}

	// 8. stored credentials of the profile
	if report.Profile != "" {
		creds, err := store.List(report.Profile)
		if err != nil {
			add(Check{"credentials", StatusFail, err.Error(), ""})
			report.Healthy = false
		} else if len(creds) == 0 {
			add(Check{"credentials", StatusWarn, "no credential stored for profile " + report.Profile,
				"dataprep-auth login -provider <name> -api-key <key>"})
		} else {
			providers := make([]string, 0, len(creds))
			for _, c := range creds {
				providers = append(providers, c.Provider)
			}
			add(Check{"credentials", StatusOK, fmt.Sprintf("%d stored: %v", len(creds), providers), ""})
		}
	}

	// 9. environment overrides, so surprises are visible
	for _, key := range []string{workspace.EnvConfigDir, "DATAPREP_PROFILE", "DATAPREP_OUTPUT", "XDG_CONFIG_HOME"} {
		if v := os.Getenv(key); v != "" {
			add(Check{"env:" + key, StatusOK, v, ""})
		}
	}

	report.Healthy = true
	for _, c := range report.Checks {
		if c.Status == StatusFail {
			report.Healthy = false
			break
		}
	}
	return report
}

// checkWritable verifies the tools can actually write into a directory, which
// is the usual container failure (a read-only or root-owned mount).
func checkWritable(dir string) Check {
	info, err := os.Stat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return Check{"workspace", StatusWarn, dir + " does not exist",
			"create it, or re-run dataprep-init with -workspace"}
	}
	if err != nil {
		return Check{"workspace", StatusFail, err.Error(), ""}
	}
	if !info.IsDir() {
		return Check{"workspace", StatusFail, dir + " is not a directory", ""}
	}
	probe := filepath.Join(dir, fmt.Sprintf(".dataprep-doctor-%d", time.Now().UnixNano()))
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_EXCL, workspace.FilePerm)
	if err != nil {
		return Check{"workspace", StatusFail, dir + " is not writable: " + err.Error(),
			"mount it read-write, or check ownership of the volume"}
	}
	f.Close()
	os.Remove(probe)
	return Check{"workspace", StatusOK, dir + " is writable", ""}
}
