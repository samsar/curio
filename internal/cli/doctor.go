package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/spf13/cobra"
)

// newDoctorCmd diagnoses the parts of curio that fail silently.
//
// Inspired by `brew doctor`: walk through every dependency, print a
// status line per check, end with a one-line summary and a suggested
// next action if anything's wrong.
func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose curio's environment: daemon, Ollama, DB, config, fetcher",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, ok := getCtx(cmd.Context())
			if !ok {
				return errors.New("no context")
			}
			r := newDoctorReport()
			runDoctorChecks(cmd.Context(), ctx, r)
			r.print(cmd.OutOrStdout())
			if r.failures > 0 {
				return fmt.Errorf("%d check(s) failed", r.failures)
			}
			return nil
		},
	}
}

type checkStatus int

const (
	statusOK checkStatus = iota
	statusWarn
	statusFail
)

type checkResult struct {
	name   string
	status checkStatus
	detail string
	hint   string
}

type doctorReport struct {
	checks   []checkResult
	failures int
	warns    int
}

func newDoctorReport() *doctorReport { return &doctorReport{} }

func (r *doctorReport) add(name string, status checkStatus, detail, hint string) {
	r.checks = append(r.checks, checkResult{name, status, detail, hint})
	switch status {
	case statusFail:
		r.failures++
	case statusWarn:
		r.warns++
	}
}

func (r *doctorReport) print(w io.Writer) {
	for _, c := range r.checks {
		marker := "✓"
		switch c.status {
		case statusWarn:
			marker = "!"
		case statusFail:
			marker = "✗"
		}
		fmt.Fprintf(w, "%s %-22s %s\n", marker, c.name, c.detail)
		if c.hint != "" {
			fmt.Fprintf(w, "  → %s\n", c.hint)
		}
	}
	fmt.Fprintln(w)
	if r.failures == 0 && r.warns == 0 {
		fmt.Fprintln(w, "all checks passed")
	} else {
		fmt.Fprintf(w, "%d failure(s), %d warning(s)\n", r.failures, r.warns)
	}
}

func runDoctorChecks(httpCtx context.Context, c *Context, r *doctorReport) {
	// 1. $CURIO_HOME and marker file
	if c.Home == nil {
		r.add("curio home", statusFail, "$CURIO_HOME not initialized",
			"run any curio command to auto-init, or set CURIO_HOME")
	} else {
		meta, err := c.Home.Meta()
		if err != nil {
			r.add("curio home", statusFail, c.Home.Path+" — marker unreadable",
				"check file perms on "+c.Home.MarkerPath())
		} else {
			r.add("curio home", statusOK,
				fmt.Sprintf("%s (schema v%d, embedder %s/%d)",
					c.Home.Path, meta.SchemaVersion, meta.EmbeddingModel, meta.EmbeddingDim), "")
		}
	}

	// 2. config validates
	if err := c.Config.Validate(); err != nil {
		r.add("config", statusFail, "invalid: "+err.Error(),
			"edit "+c.Home.ConfigPath())
	} else {
		r.add("config", statusOK,
			fmt.Sprintf("workers=%d, fetcher=%s, model=%s",
				c.Config.Daemon.Workers, c.Config.Fetcher.Default, c.Config.Embedding.Model), "")
	}

	// 3. daemon reachable
	dctx, dcancel := context.WithTimeout(httpCtx, 500*time.Millisecond)
	defer dcancel()
	health, err := c.Client.Healthz(dctx)
	if err != nil {
		r.add("daemon", statusFail, "not reachable at "+c.Config.Daemon.Listen,
			"run `curio daemon start`")
	} else {
		r.add("daemon", statusOK, fmt.Sprintf("running, version %s", health.Version), "")

		// 4. ollama (via the daemon's healthz, since the daemon has the
		// concrete embedder client and knows the configured base_url)
		if health.OllamaReachable {
			r.add("ollama", statusOK, "reachable, model "+health.EmbeddingModel+" loaded", "")
		} else {
			r.add("ollama", statusFail, health.OllamaDetail, "")
		}
	}

	// 5. fetcher backend: native is always fine; web2md needs the bin to exist
	switch c.Config.Fetcher.Default {
	case "native":
		r.add("fetcher", statusOK, "native (Go, no external deps)", "")
	case "web2md":
		bin := c.Config.Fetcher.Web2MD.Bin
		if bin == "" {
			r.add("fetcher", statusFail, "web2md selected but bin is empty",
				"set fetcher.web2md.bin in "+c.Home.ConfigPath())
		} else if _, err := exec.LookPath(bin); err != nil {
			if _, statErr := os.Stat(bin); statErr != nil {
				r.add("fetcher", statusFail, "web2md not found at "+bin,
					"install Node + web2md, or switch to fetcher.default: native")
			} else {
				r.add("fetcher", statusOK, "web2md at "+bin, "")
			}
		} else {
			r.add("fetcher", statusOK, "web2md at "+bin, "")
		}
	}

	// 6. content dir writable
	if c.Home != nil {
		dir := c.Home.ContentDir()
		probe := dir + "/.doctor-probe"
		if err := os.MkdirAll(dir, 0o700); err != nil {
			r.add("content dir", statusFail, "cannot create "+dir+": "+err.Error(), "")
		} else if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
			r.add("content dir", statusFail, "not writable: "+err.Error(),
				"fix perms on "+dir)
		} else {
			_ = os.Remove(probe)
			r.add("content dir", statusOK, dir+" writable", "")
		}
	}
}

