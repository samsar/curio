package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/daemonctl"
	"github.com/samsar/curio/internal/setup"
	"github.com/samsar/curio/internal/textutil"
)

func newUpCmd(flags *rootFlags, d deps) *cobra.Command {
	var opts setup.Options
	cmd := &cobra.Command{
		Use:   "up",
		Short: "Set curio up, or check that it is: the Mac, Ollama, the models, the home, the daemon, your bookmarks",
		Long: `Set curio up on this Mac, and keep it set up. curio up checks each part
(the Mac, Ollama, the search and writing models, the home and its
config.yaml, the daemon and its launchd agent), shows what it would do,
and then does it, asking before each step. Every command it runs is shown
first; it never uses sudo. A run where everything checks out changes
nothing and says so, and a run cut short picks up where it stopped.

With an empty library it offers to import your bookmarks: from Chrome,
Firefox, Safari or an exported HTML file, each with how many are new;
--import names the source up front.

Without a terminal it never asks: it shows the plan and exits 1, unless
--yes answers for you, and imports only what --import names. See
docs/setup.md.`,
		Args:        cobra.NoArgs,
		Annotations: map[string]string{ownsEnvironment: "true"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			if d.geteuid() == 0 {
				return errors.New("curio up sets curio up for your own user, and never uses sudo: run it without sudo")
			}
			ui := d.newUI(cmd.InOrStdin(), cmd.ErrOrStderr(), opts.Yes)
			r, err := flags.setupFor(d, ui, opts)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			outcome, err := r.Run(cmd.Context(), func(p setup.Plan) { printPlan(out, p) })
			if err != nil {
				return err
			}
			switch {
			case outcome.DryRun:
				fmt.Fprintln(out, "Dry run: nothing was changed.")
			case !outcome.Changed:
				fmt.Fprintln(out, "Nothing to do: curio is up.")
				printUpStatus(out, outcome.Status)
			case outcome.Imported != nil:
				fmt.Fprintln(out, "curio is up.")
				printUpStatus(out, outcome.Status)
				printHandOff(out, *outcome.Imported, time.Now())
			default:
				fmt.Fprintln(out, "curio is up.")
				printUpStatus(out, outcome.Status)
				printNext(out)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&opts.Yes, "yes", false,
		"answer every question: yes to each step, --fresh's move included, and the default to each choice")
	f.BoolVar(&opts.NoInstall, "no-install", false,
		"install or start nothing with Homebrew or the Ollama app; the models, the home, config.yaml and the launchd agent are still set up")
	f.BoolVar(&opts.DryRun, "dry-run", false, "show the plan and change nothing")
	f.BoolVar(&opts.Fresh, "fresh", false,
		"move the existing home aside to <home>.bak-<YYYYMMDD-HHMMSS>, deleting nothing, and start a new one")
	f.StringVar(&opts.EmbeddingModel, "embedding-model", "",
		"embed a new home with this Ollama model, tag included (an existing home keeps its own)")
	f.StringVar(&opts.GenerationModel, "generation-model", "",
		"write interest labels with this Ollama model, when config.yaml doesn't name one")
	f.StringVar(&opts.Import, "import", "",
		"import from this source without a menu: chrome, chrome:<profile>, safari, firefox or html:<file>")
	return cmd
}

// printPlan shows what curio up would do: each fix with the commands it
// runs and the notes behind it, what blocks it, and the warnings.
func printPlan(w io.Writer, p setup.Plan) {
	if fixes := p.Fixes(); len(fixes) > 0 {
		fmt.Fprintln(w, "curio up will:")
		for i, it := range fixes {
			fmt.Fprintf(w, "  %d. %s: %s\n", i+1, it.Step, it.Result.Fix.Summary)
			for _, argv := range it.Result.Fix.Commands {
				fmt.Fprintf(w, "       $ %s\n", textutil.ShellJoin(argv))
			}
			for _, note := range it.Result.Notes {
				fmt.Fprintf(w, "       %s\n", note)
			}
			if it.Result.Hint != "" {
				fmt.Fprintf(w, "       %s\n", it.Result.Hint)
			}
		}
	}
	if blockers := p.Blockers(); len(blockers) > 0 {
		fmt.Fprintln(w, "To fix by hand first:")
		for _, it := range blockers {
			fmt.Fprintf(w, "  ✗ %s: %s\n", it.Step, it.Result.Detail)
			if it.Result.Hint != "" {
				fmt.Fprintf(w, "    → %s\n", it.Result.Hint)
			}
		}
	}
	for _, it := range p.Warnings() {
		fmt.Fprintf(w, "  ! %s: %s\n", it.Step, it.Result.Detail)
		for _, note := range it.Result.Notes {
			fmt.Fprintf(w, "    %s\n", note)
		}
	}
	fmt.Fprintln(w)
}

// printUpStatus shows curio's state after a run: the daemon, Ollama and
// the models, the library, the queue, and what to know. A part that
// couldn't be read says why.
func printUpStatus(w io.Writer, s setup.Snapshot) {
	switch d := s.Daemon; {
	case d.Err != nil:
		fmt.Fprintf(w, "daemon:   unavailable: %v\n", d.Err)
	case d.Managed:
		fmt.Fprintf(w, "daemon:   running (pid %d), kept running by its launchd agent\n", d.PID)
	default:
		fmt.Fprintf(w, "daemon:   running (pid %d), started on demand\n", d.PID)
	}
	if o := s.Ollama; o.Err != nil {
		fmt.Fprintf(w, "ollama:   unavailable: %v\n", o.Err)
	} else {
		fmt.Fprintf(w, "ollama:   %s; %s\n", o.Version, describeModels(o.Models))
	}
	if s.DocumentsErr != nil {
		fmt.Fprintf(w, "library:  unavailable: %v\n", s.DocumentsErr)
	} else {
		fmt.Fprintf(w, "library:  %s\n", plural(s.Documents, "document"))
	}
	if s.QueueErr != nil {
		fmt.Fprintf(w, "queue:    unavailable: %v\n", queueError(s.QueueErr))
	} else {
		fmt.Fprintf(w, "queue:    %s\n", describeQueue(s.Queue))
	}
	if d := s.Drift; d != nil {
		fmt.Fprintf(w, "warning: embeddings drifted since the library was indexed (%s); run `%s`\n",
			setup.DriftChanges(d), d.Fix)
	}
	for _, warning := range s.Warnings {
		fmt.Fprintf(w, "warning: %s\n", warning.Text)
		for _, note := range warning.Notes {
			fmt.Fprintf(w, "  %s\n", note)
		}
	}
}

// describeModels says which of curio's models Ollama has.
func describeModels(models []setup.ModelSnapshot) string {
	parts := make([]string, 0, len(models))
	for _, m := range models {
		switch {
		case m.Err != nil:
			parts = append(parts, fmt.Sprintf("%s unknown (%v)", m.Name, m.Err))
		case m.Present:
			parts = append(parts, m.Name+" present")
		default:
			parts = append(parts, m.Name+" missing")
		}
	}
	return strings.Join(parts, ", ")
}

// cardLine is a command a card names, and what it does.
type cardLine struct{ command, does string }

// nextCard is what a run that changed something and started no import
// ends with; the MCP line follows it.
var nextCard = []cardLine{
	{"curio import chrome", "import more bookmarks (or safari, firefox, html <file>)"},
	{"curio status", "see what the daemon is doing"},
	{`curio search "..."`, "search your library"},
	{"curio pause | resume", "stop starting new work, and start again"},
	{"curio throttle gentle", "fewer jobs at once, to keep the Mac cool"},
}

// handOffCard is what a run that started an import ends with, after when
// to check back; the MCP line follows it.
var handOffCard = []cardLine{
	{"curio status --follow", "follow the import until it is done"},
	{"curio pause | resume", "stop starting new work, and start again"},
	{"curio throttle gentle", "fewer jobs at once, to keep the Mac cool"},
	{`curio search "..."`, "search your library, as it grows"},
}

// mcpLine lets Claude Code search the library.
func mcpLine() cardLine { return cardLine{mcpCommand(), "let Claude Code search your library (MCP)"} }

// printCard prints lines under title.
func printCard(w io.Writer, title string, lines []cardLine) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, title)
	for _, line := range append(slices.Clone(lines), mcpLine()) {
		fmt.Fprintf(w, "  %-36s %s\n", line.command, line.does)
	}
}

// printNext is the card a run that changed something, and started no
// import, ends with: what to do next, and how to give Claude the library.
func printNext(w io.Writer) { printCard(w, "Next:", nextCard) }

// printHandOff is the card a run that started an import ends with: when
// to check back, what was imported, and what to do meanwhile.
func printHandOff(w io.Writer, r setup.ImportReport, now time.Time) {
	fmt.Fprintln(w)
	switch {
	case r.Pages == 0:
		fmt.Fprintf(w, "Imported %s from %s; their pages were in the library already.\n",
			plural(r.Created, "new bookmark"), r.Source)
	case r.CheckBack.IsZero():
		fmt.Fprintf(w, "Import started; when it is done isn't known, but fetching takes %s.\n", r.Estimate.Fetching())
		fmt.Fprintf(w, "  %s from %s, %s to fetch and index\n", plural(r.Created, "new bookmark"), r.Source,
			plural(r.Pages, "page"))
	default:
		fmt.Fprintf(w, "Import started. Check back after %s.\n", setup.CheckBackText(now, r.CheckBack))
		fmt.Fprintf(w, "  %s from %s, %s to fetch and index %s\n", plural(r.Created, "new bookmark"), r.Source,
			plural(r.Pages, "page"), r.Pace)
	}
	printCard(w, "Meanwhile:", handOffCard)
}

// mcpByName registers the curio-mcp on PATH with Claude Code.
const mcpByName = "claude mcp add curio -- curio-mcp"

// mcpCommand registers this curio's MCP server with Claude Code
// (mcpCommandFor).
func mcpCommand() string {
	exe, err := os.Executable()
	if err != nil {
		return mcpByName // no path to name; the one on PATH is the best guess
	}
	return mcpCommandFor(exe)
}

// mcpCommandFor registers the curio-mcp next to the curio at exe: by name
// when the curio-mcp on PATH is that one, and by its absolute path
// otherwise, so Claude Code runs this curio's.
func mcpCommandFor(exe string) string {
	next := filepath.Join(filepath.Dir(exe), "curio-mcp")
	if onPath, err := exec.LookPath("curio-mcp"); err == nil && daemonctl.SameFile(onPath, next) {
		return mcpByName
	}
	return "claude mcp add curio -- " + textutil.ShellQuote(next)
}

// upHint is the line bare `curio` adds under its help, pointing at `curio
// up`: for a home that doesn't exist yet, and for one whose daemon serves
// an empty library. It creates nothing and starts nothing, and gives the
// daemon a second at most; anything it can't tell means no hint.
func upHint(ctx context.Context, homeFlag, daemonURL string) string {
	path, err := daemonctl.HomePath(homeFlag)
	if err != nil {
		return ""
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return "curio isn't set up here yet: run `curio up`."
	}
	home, err := curiohome.Open(path)
	if err != nil {
		return ""
	}
	cfg, err := config.Load(home.ConfigPath())
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	c := client.New(daemonctl.BaseURL(cfg, daemonURL))
	h, err := c.Healthz(ctx)
	if err != nil || !daemonctl.SameHome(h.Home, path) {
		return ""
	}
	stats, err := c.Stats(ctx)
	if err != nil || stats.BookmarksTotal > 0 {
		return ""
	}
	return "Your library is empty: run `curio up`, which imports your bookmarks."
}
