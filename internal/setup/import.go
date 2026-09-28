package setup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/daemonctl"
	"github.com/samsar/curio/internal/embedder"
	"github.com/samsar/curio/internal/importer"
	"github.com/samsar/curio/internal/textutil"
)

// importStep brings the user's bookmarks into the library: from the
// source --import names, or one chosen from a menu of the sources found,
// each with how many of its bookmarks are new to the library. It has
// something to do only for an empty library, or with --import: a library
// with bookmarks is up, and parses nothing.
type importStep struct{ w *world }

func (*importStep) Name() string { return "import" }

// importSpec is what --import names.
type importSpec struct {
	kind string // chrome, safari, firefox or html
	arg  string // a Chrome profile (empty for Default), or an HTML file's path
}

// String is the spec as --import takes it.
func (s importSpec) String() string {
	if s.arg == "" {
		return s.kind
	}
	return s.kind + ":" + s.arg
}

// parseImportSpec parses --import, split at its first ':': chrome,
// chrome:<profile>, safari, firefox or html:<path>. Empty is none.
func parseImportSpec(v string) (*importSpec, error) {
	if v == "" {
		return nil, nil //nolint:nilnil // no --import is no spec, and no error
	}
	kind, arg, _ := strings.Cut(v, ":")
	spec := &importSpec{kind: kind, arg: arg}
	switch kind {
	case importer.LabelChrome:
	case importer.LabelHTML:
		if arg == "" {
			return nil, fmt.Errorf("--import %s: name the exported file, html:<path>", v)
		}
	case importer.LabelSafari, importer.LabelFirefox:
		if arg != "" {
			return nil, fmt.Errorf("--import %s: %s takes no profile; its default profile is imported", v, kind)
		}
	default:
		return nil, fmt.Errorf("--import %s: import from chrome, chrome:<profile>, safari, firefox or html:<path>", v)
	}
	return spec, nil
}

// importState is what the import step found and decided during a run.
type importState struct {
	spec *importSpec
	// sources are Deps.Sources', found once, when the step first may
	// import.
	sources []importer.Source
	// parsed are the sources' bookmarks, by Spec, parsed once a run.
	parsed map[string]parsedSource
	// survey is the sources as the last check found them, for the menu.
	survey []sourceCount
	// chosen is the source picked from the menu.
	chosen importer.Source
	// skipped: the user chose to import nothing now.
	skipped bool
	// nothingNew names the source whose import found nothing new.
	nothingNew string
	// report is the import this run started.
	report *ImportReport
	// rate is the index rate measured on this Mac, once a run.
	rate *indexRate
}

func newImportState(spec *importSpec) importState {
	return importState{spec: spec, parsed: map[string]parsedSource{}}
}

// parsedSource is a source's bookmarks, or why they couldn't be read.
type parsedSource struct {
	bms []importer.ParsedBookmark
	err error
}

// sourceCount is a source as the import step found it: whether it can be
// read, and what importing it would bring in.
type sourceCount struct {
	src   importer.Source
	av    importer.Availability
	count importer.Count
	// err is why its bookmarks couldn't be read or counted.
	err error
}

// empty reports whether the source has no bookmarks: menus leave it out.
func (c sourceCount) empty() bool { return errors.Is(c.err, importer.ErrEmpty) }

// readable reports whether the source's bookmarks were read.
func (c sourceCount) readable() bool { return c.av.State == importer.Available && c.err == nil }

// newBookmarks is how many bookmarks the import would save: those the
// source hasn't saved before, or, from a daemon that can't count them,
// every candidate.
func (c sourceCount) newBookmarks() int {
	if !c.count.Known {
		return len(c.count.URLs)
	}
	return c.count.Created
}

// newPages is how many pages the import would fetch: the new URLs, or,
// from a daemon that can't count them, every candidate.
func (c sourceCount) newPages() []string {
	if !c.count.Known {
		return c.count.URLs
	}
	return c.count.NewURLs
}

// describe is the source's line in the plan and the menu: its new count,
// or its problem. The count is of bookmarks, as the import's question and
// report count them, with the pages to fetch when fewer: a bookmark whose
// page another source brought in is new, and fetches nothing.
func (c sourceCount) describe() string {
	switch {
	case c.av.State == importer.NeedsPermission:
		return c.src.Name() + ": needs Full Disk Access"
	case c.av.State != importer.Available:
		return c.src.Name() + ": " + c.av.Reason
	case c.empty():
		return c.src.Name() + ": no bookmarks"
	case c.err != nil:
		return c.src.Name() + ": " + c.err.Error()
	case !c.count.Known:
		return fmt.Sprintf("%s: %s", c.src.Name(), plural(len(c.count.URLs), "bookmark"))
	}
	line := fmt.Sprintf("%s: %d new", c.src.Name(), c.count.Created)
	if pages := len(c.count.NewURLs); pages != c.count.Created {
		line += fmt.Sprintf(" (%s to fetch)", plural(pages, "page"))
	}
	return line
}

// library is the library an import would add to.
type library struct {
	// known: the daemon serving the home reported it, or there is no home
	// yet and it is empty.
	known bool
	// why it isn't known.
	why string
	// homeBlocked: the home itself is unusable, so no daemon will answer
	// until the home check's remedy is applied.
	homeBlocked          bool
	bookmarks, documents int
	// daemon is the client of the daemon serving it; nil when there is no
	// home yet, and every candidate is new.
	daemon *client.Client
}

// readLibrary reads the library from the daemon serving this home, bounded
// by the probe timeout. With no home yet, or one --fresh replaces, it is
// empty, and no daemon is asked: the one answering serves the old home.
func (w *world) readLibrary(ctx context.Context, hs homeState) library {
	if why := hs.unusable(); why != "" {
		return library{why: why + " (see the curio home check)", homeBlocked: true}
	}
	if hs.kind != homeOurs || w.freshPending() {
		return library{known: true}
	}
	if !w.homeReady(hs) {
		return library{why: "no daemon serves this home"}
	}
	env, err := w.connect(hs)
	if err != nil {
		return library{why: err.Error()}
	}
	h, err := bounded(ctx, w.times.Probe, env.Client.Healthz)
	switch {
	case client.StartupOf(err) != nil:
		return library{why: "the daemon is starting"}
	case errors.Is(err, client.ErrDaemonUnreachable):
		return library{why: "the daemon isn't running"}
	case err != nil:
		return library{why: err.Error()}
	case h.Home != "" && !daemonctl.SameHome(h.Home, hs.path):
		return library{why: "the daemon answering serves " + h.Home}
	}
	stats, err := bounded(ctx, w.times.Probe, env.Client.Stats)
	if err != nil {
		return library{why: err.Error()}
	}
	return library{known: true, bookmarks: stats.BookmarksTotal, documents: stats.DocumentsTotal, daemon: env.Client}
}

func (s *importStep) Check(ctx context.Context) Result {
	w := s.w
	switch st := &w.imp; {
	case st.report != nil:
		return Result{Status: OK, Detail: fmt.Sprintf("import started: %s from %s",
			plural(st.report.Created, "bookmark"), st.report.Source)}
	case st.skipped:
		return Result{Status: Warn, Detail: "skipped: nothing imported",
			Notes: []string{"run `curio up` again to choose, or `curio up --import <source>`"}}
	case st.nothingNew != "":
		return Result{Status: OK, Detail: "nothing new in " + st.nothingNew}
	}
	hs := w.readHome()
	lib := w.readLibrary(ctx, hs)
	if w.imp.spec != nil {
		return w.checkNamedImport(ctx, lib)
	}
	switch {
	case !lib.known && lib.homeBlocked:
		return Result{Status: Warn, Detail: "not checked: " + lib.why}
	case !lib.known:
		return Result{Status: Warn, Detail: "not checked: " + lib.why + "; checked again once the daemon serves"}
	case lib.bookmarks > 0:
		return Result{Status: OK, Detail: plural(lib.documents, "document") + " in the library",
			Hint: "`curio import` (chrome, safari, firefox, html <file>) imports more"}
	}
	survey := w.survey(ctx, lib)
	switch {
	case !w.deps.UI.Interactive():
		return Result{Status: Warn, Detail: "the library is empty, and without a terminal curio up imports only " +
			"what `curio up --import <source>` names", Notes: importValues(survey)}
	case w.opts.Yes:
		return Result{Status: Warn, Detail: "the library is empty, and --yes imports only what " +
			"`curio up --import <source>` names", Notes: importValues(survey)}
	}
	notes := make([]string, 0, len(survey))
	for _, c := range survey {
		notes = append(notes, w.sourceNote(ctx, c))
	}
	return Result{Status: Warn, Detail: "the library is empty", Notes: notes,
		Fix: &Fix{Summary: "choose which bookmarks to import"}}
}

// checkNamedImport is the check of an --import: the source it names must
// be there, readable and hold bookmarks, or nothing is applied.
func (w *world) checkNamedImport(ctx context.Context, lib library) Result {
	src, err := w.namedSource()
	if err != nil {
		return Result{Status: Fail, Detail: err.Error()}
	}
	c := w.countSource(ctx, lib, src)
	switch why := w.cantAskForAccess(); {
	case c.av.State == importer.NeedsPermission && why != "":
		return Result{Status: Fail, Detail: fmt.Sprintf("--import %s: macOS doesn't let curio read %s without Full "+
			"Disk Access, %s", w.imp.spec, src.Name(), why), Hint: fullDiskAccessHint(src)}
	case c.av.State == importer.NeedsPermission:
		return Result{Status: Warn, Detail: c.src.Name() + " needs Full Disk Access",
			Fix: &Fix{Summary: "import bookmarks from " + src.Name() + ", once macOS lets curio read them"}}
	case c.av.State == importer.NotInstalled:
		return Result{Status: Fail, Detail: fmt.Sprintf("--import %s: %s: %s", w.imp.spec, src.Name(), c.av.Reason)}
	case c.av.State != importer.Available:
		return Result{Status: Fail, Detail: fmt.Sprintf("--import %s: %s can't be read: %s", w.imp.spec, src.Name(),
			c.av.Reason)}
	case c.empty():
		return Result{Status: Fail, Detail: fmt.Sprintf("--import %s: %s has no bookmarks", w.imp.spec, src.Name())}
	case c.err != nil:
		return Result{Status: Fail, Detail: fmt.Sprintf("--import %s: %v", w.imp.spec, c.err)}
	case lib.known && c.count.Known && c.count.Created == 0:
		return Result{Status: OK, Detail: "nothing new in " + src.Name()}
	}
	res := Result{Status: Warn, Detail: "not imported yet", Notes: []string{w.sourceNote(ctx, c)},
		Fix: &Fix{Summary: "import " + bookmarksToImport(c, lib) + " from " + src.Name()}}
	if !lib.known {
		res.Notes = []string{"not counted: " + lib.why}
	}
	return res
}

// bookmarksToImport says how many bookmarks an import brings in: "N new
// bookmarks", or just "bookmarks" when that isn't known.
func bookmarksToImport(c sourceCount, lib library) string {
	if !lib.known || !c.count.Known {
		return "bookmarks"
	}
	return fmt.Sprintf("%d new %s", c.count.Created, pluralWord(c.count.Created, "bookmark"))
}

// namedSource is the source --import names: an HTML file by its path, a
// browser among the sources found.
func (w *world) namedSource() (importer.Source, error) {
	spec := w.imp.spec
	if spec.kind == importer.LabelHTML {
		return importer.HTMLFile(spec.arg), nil
	}
	sources := w.foundSources()
	if spec.kind == importer.LabelChrome {
		src, err := importer.FindChrome(sources, spec.arg)
		if err != nil {
			return nil, fmt.Errorf("--import %s: %w", spec, err)
		}
		return src, nil
	}
	for _, src := range sources {
		if src.Label() == spec.kind {
			return src, nil
		}
	}
	return nil, fmt.Errorf("--import %s: %s isn't installed", spec, spec.kind)
}

// foundSources are the sources on this machine, found once a run.
func (w *world) foundSources() []importer.Source {
	if w.imp.sources == nil {
		w.imp.sources = w.deps.Sources()
		if w.imp.sources == nil {
			w.imp.sources = []importer.Source{}
		}
	}
	return w.imp.sources
}

// survey checks and counts every source found, for the menu.
func (w *world) survey(ctx context.Context, lib library) []sourceCount {
	sources := w.foundSources()
	out := make([]sourceCount, 0, len(sources))
	for _, src := range sources {
		out = append(out, w.countSource(ctx, lib, src))
	}
	w.imp.survey = out
	return out
}

// countSource checks src and, when it can be read, counts what importing
// it would bring in: through the daemon's dry run when one serves the
// home, as every candidate when there is no home yet, and as unknown
// otherwise, or when the count fails.
func (w *world) countSource(ctx context.Context, lib library, src importer.Source) sourceCount {
	c := sourceCount{src: src, av: src.Check(ctx)}
	if c.av.State != importer.Available {
		return c
	}
	bms, err := w.parse(ctx, src)
	if err != nil {
		c.err = err
		return c
	}
	c.count = importer.Count{Parsed: len(bms), Candidates: importer.CandidatesOf(bms)}
	switch {
	case lib.daemon != nil:
		if counted, err := importer.CountNew(ctx, lib.daemon, src.Label(), bms); err == nil {
			c.count = counted
		}
	case lib.known:
		c.count.Known, c.count.Created, c.count.NewURLs = true, len(c.count.URLs), c.count.URLs
	}
	return c
}

// parse is src's bookmarks, parsed once a run.
func (w *world) parse(ctx context.Context, src importer.Source) ([]importer.ParsedBookmark, error) {
	p, ok := w.imp.parsed[src.Spec()]
	if !ok {
		p.bms, p.err = src.Parse(ctx)
		w.imp.parsed[src.Spec()] = p
	}
	return p.bms, p.err
}

// sourceNote is a source's line in the plan: its count, and how long its
// new pages would take: fetching always, and indexing for a dry run, whose
// plan is all it shows.
func (w *world) sourceNote(ctx context.Context, c sourceCount) string {
	note := c.describe()
	pages := c.newPages()
	if !c.readable() || len(pages) == 0 {
		return note
	}
	hs := w.readHome()
	rate, why := 0.0, "measured when the import starts"
	if w.opts.DryRun {
		rate, why = w.indexRate(ctx, hs)
	}
	e := estimateImport(len(pages), w.baseConfig(hs).Daemon.FetchWorkers, rate, why)
	note += ", fetching " + e.Fetching() + " at full speed"
	if e.IndexHi > 0 {
		note += ", indexing " + e.Indexing()
	}
	return note
}

// importValues are the --import values of the sources found, for a run
// that can't ask: one line each, with what it would bring in.
func importValues(survey []sourceCount) []string {
	var out []string
	for _, c := range survey {
		if c.empty() || (c.av.State != importer.Available && c.av.State != importer.NeedsPermission) {
			continue
		}
		out = append(out, fmt.Sprintf("--import %s  (%s)", textutil.ShellQuote(c.src.Spec()), c.describe()))
	}
	return append(out, "--import html:<file>  (an exported bookmarks file)")
}

// Confirm asks what to import: with --import, one yes-or-no for the source
// it names; otherwise a menu of the sources found, an exported HTML file,
// and skipping for now, which is no decline.
func (s *importStep) Confirm(ctx context.Context, ui UI, fix Fix) (bool, error) {
	w := s.w
	if w.imp.spec != nil {
		return ui.Confirm(ctx, capitalize(fix.Summary)+"?", true)
	}
	for {
		options, choices, def := menu(w.imp.survey)
		i, err := ui.Select(ctx, "Import which bookmarks?", options, def)
		if err != nil {
			return false, err
		}
		switch pick := choices[i]; {
		case pick.skip:
			w.imp.skipped = true
			return true, nil
		case pick.html:
			if src, ok, err := w.askHTMLFile(ctx, ui); err != nil || ok {
				w.imp.chosen = src
				return ok, err
			}
		case pick.count.av.State == importer.NeedsPermission:
			granted, err := w.fullDiskAccess(ctx, ui, pick.count.src)
			if err != nil {
				return false, err
			}
			if granted {
				w.imp.chosen = pick.count.src
				return true, nil
			}
		default:
			w.imp.chosen = pick.count.src
			return true, nil
		}
	}
}

// menuChoice is what a menu line picks.
type menuChoice struct {
	count sourceCount
	html  bool
	skip  bool
}

// menu is the import menu over the sources surveyed: each one readable,
// with its new count, and each that needs permission; then an HTML file,
// then skipping. def is the source with the most new bookmarks.
func menu(survey []sourceCount) (options []string, choices []menuChoice, def int) {
	most := -1
	for _, c := range survey {
		switch {
		case c.readable():
			if n := c.newBookmarks(); n > most {
				most, def = n, len(options)
			}
		case c.av.State == importer.NeedsPermission:
		default:
			continue
		}
		options = append(options, c.describe())
		choices = append(choices, menuChoice{count: c})
	}
	options = append(options, "An exported bookmarks file (HTML)…", "Skip for now")
	choices = append(choices, menuChoice{html: true}, menuChoice{skip: true})
	return options, choices, def
}

// askHTMLFile asks for an exported bookmarks file's path and reads it; ok
// is false, the reason said, for a path that won't do, so the menu is
// shown again. A file is read again each time it is named: the user may
// have fixed it since.
func (w *world) askHTMLFile(ctx context.Context, ui UI) (importer.Source, bool, error) {
	typed, err := ui.Input(ctx, "Path to the exported bookmarks file:", "")
	if err != nil {
		return nil, false, err
	}
	path := typedPath(typed)
	if path == "" {
		return nil, false, nil
	}
	src := importer.HTMLFile(path)
	if av := src.Check(ctx); av.State != importer.Available {
		ui.Warn(src.Name() + ": " + av.Reason)
		return nil, false, nil
	}
	delete(w.imp.parsed, src.Spec())
	_, parseErr := w.parse(ctx, src)
	if parseErr != nil {
		ui.Warn(src.Name() + ": " + parseErr.Error())
	}
	return src, parseErr == nil, nil
}

// typedPath is a path typed at a prompt, taken as a shell takes one word,
// since a file dragged into the terminal arrives quoted or with its
// spaces escaped (Safari exports "Safari Bookmarks.html"): one pair of
// surrounding quotes is dropped; otherwise each backslash escapes the
// character after it.
func typedPath(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	var b strings.Builder
	escaped := false
	for _, r := range s {
		if r == '\\' && !escaped {
			escaped = true
			continue
		}
		escaped = false
		b.WriteRune(r)
	}
	return b.String()
}

// Apply imports the chosen source: the daemon started and asked what is
// new; nothing more when nothing is. Otherwise, before a bookmark is
// sent: yt-dlp for the new YouTube videos, the estimates, and the pace
// and keep-awake, set on the queue at once. The bookmarks are then sent
// with a progress line; an import cut short says how to finish it.
func (s *importStep) Apply(ctx context.Context, ui UI) error {
	w := s.w
	if w.imp.skipped {
		return nil
	}
	src := w.imp.chosen
	if w.imp.spec != nil {
		var err error
		if src, err = w.namedSource(); err != nil {
			return err
		}
		if err := w.allowed(ctx, ui, src); err != nil {
			return err
		}
	}
	if src == nil {
		return errors.New("no source was chosen to import from")
	}
	hs := w.readHome() // --fresh may have replaced the home the plan saw
	if hs.kind != homeOurs {
		return errors.New("there is no curio home at " + hs.path)
	}
	env, err := w.connect(hs)
	if err != nil {
		return err
	}
	if err := env.Controller.EnsureRunning(ctx); err != nil {
		return err
	}
	bms, err := w.parse(ctx, src)
	if err != nil {
		return err
	}
	count, err := importer.CountNew(ctx, env.Client, src.Label(), bms)
	if err != nil {
		return err
	}
	if count.Known && count.Created == 0 {
		ui.Info("nothing new in " + src.Name())
		w.imp.nothingNew = src.Name()
		return nil
	}
	c := sourceCount{src: src, av: importer.Availability{State: importer.Available}, count: count}
	report := &ImportReport{Source: src.Name(), Spec: src.Spec(), Pages: len(c.newPages())}
	if report.Pages > 0 {
		if err := w.offerYTDLP(ctx, ui, env, c.newPages()); err != nil {
			return err
		}
		if err := w.choosePace(ctx, ui, env, hs, c.newPages(), report); err != nil {
			return err
		}
	}
	totals, err := send(ctx, ui, env, src, bms)
	if err != nil {
		return err
	}
	report.Created, report.Filtered, report.JobsEnqueued = totals.Created, totals.Filtered, totals.JobsEnqueued
	w.imp.report = report
	return nil
}

// allowed makes sure src can be read, walking the user through Full Disk
// Access when it needs it and someone can answer; a source still withheld
// is an error naming the remedy.
func (w *world) allowed(ctx context.Context, ui UI, src importer.Source) error {
	if src.Check(ctx).State != importer.NeedsPermission {
		return nil
	}
	if why := w.cantAskForAccess(); why != "" {
		return fmt.Errorf("%s: macOS doesn't let curio read it without Full Disk Access, %s (%s)", src.Name(), why,
			fullDiskAccessHint(src))
	}
	granted, err := w.fullDiskAccess(ctx, ui, src)
	switch {
	case err != nil:
		return err
	case !granted:
		return fmt.Errorf("%s: macOS doesn't let curio read it yet (%s)", src.Name(), fullDiskAccessHint(src))
	}
	return nil
}

// send imports bms from src into the daemon with a progress line. An
// import cut short, by an error or an interrupt, first says how many were
// sent and the command that finishes it.
func send(ctx context.Context, ui UI, env daemonctl.Env, src importer.Source,
	bms []importer.ParsedBookmark) (importer.Totals, error) {
	bar := ui.Progress("importing "+src.Name(), Items)
	sent := 0
	totals, err := importer.Send(ctx, env.Client, src.Label(), bms, func(n int, _ importer.Totals) {
		sent = n
		bar.Update(int64(n), int64(len(bms)))
	})
	if err != nil {
		bar.Stop()
		ui.Warn(fmt.Sprintf("the import stopped after %d of %d bookmarks from %s; `curio up --import %s` finishes it "+
			"(bookmarks already saved are skipped)", sent, len(bms), src.Name(), textutil.ShellQuote(src.Spec())))
		return totals, err
	}
	bar.Done()
	return totals, nil
}

// ImportReport is the import a run of curio up started, which the hand-off
// is made of.
type ImportReport struct {
	// Source is the source's name ("Chrome: Work"), Spec its --import
	// value.
	Source, Spec string
	// Pages is how many pages new to the library the import fetches.
	Pages                           int
	Created, Filtered, JobsEnqueued int
	// Pace and KeepAwake are what the queue was set to, and CheckBack when
	// the new pages should be done at that pace; unset when there was
	// nothing new to fetch.
	Pace      Pace
	KeepAwake bool
	CheckBack time.Time
}

// fullDiskAccessPane is System Settings' Full Disk Access list.
const fullDiskAccessPane = "x-apple.systempreferences:com.apple.preference.security?Privacy_AllFiles"

// fullDiskAccessHint is the remedy for a source macOS withholds.
func fullDiskAccessHint(src importer.Source) string {
	return fmt.Sprintf("give %s Full Disk Access in System Settings > Privacy & Security > Full Disk Access, "+
		"quit and reopen it, then run `curio up --import %s` again", terminalApp(), textutil.ShellQuote(src.Spec()))
}

// terminalApp names the terminal app curio runs in, from $TERM_PROGRAM,
// for the Full Disk Access it needs.
func terminalApp() string {
	switch os.Getenv("TERM_PROGRAM") {
	case "Apple_Terminal":
		return "Terminal"
	case "iTerm.app":
		return "iTerm2"
	default:
		return "your terminal app"
	}
}

// cantAskForAccess is why this run can't walk the user through Full Disk
// Access, empty when it can. The walk takes a terminal and someone at it:
// only the user can turn access on in System Settings, and macOS applies
// it once the terminal restarts, so --yes, answering every "check again"
// itself, would check again for good.
func (w *world) cantAskForAccess() string {
	switch {
	case !w.deps.UI.Interactive():
		return "which takes a terminal to walk through"
	case w.opts.Yes:
		return "which only you can turn on, so --yes can't answer for it"
	}
	return ""
}

// fullDiskAccess walks the user through the Full Disk Access macOS wants
// before src can be read: it explains that macOS grants it to the terminal
// app, offers to open its pane in System Settings, then checks again as
// often as the user asks. granted is false when the user stops asking.
// Only a run that can ask (cantAskForAccess) calls it.
func (w *world) fullDiskAccess(ctx context.Context, ui UI, src importer.Source) (granted bool, err error) {
	term := terminalApp()
	ui.Info(fmt.Sprintf("macOS lets an app read %s's bookmarks only with Full Disk Access, and grants it to the "+
		"terminal app curio runs in (%s), not to curio.", src.Name(), term))
	if w.opts.NoInstall {
		ui.Info("Give " + term + " Full Disk Access in System Settings > Privacy & Security > Full Disk Access " +
			"(--no-install opens nothing).")
	} else {
		argv := Command(Detection{}, OpenURL, fullDiskAccessPane)
		ui.Info("  $ " + textutil.ShellJoin(argv))
		open, err := ui.Confirm(ctx, "Open Full Disk Access in System Settings?", true)
		if err != nil || !open {
			return false, err
		}
		if err := w.deps.Installer.Run(ctx, ui, argv); err != nil {
			return false, err
		}
		ui.Info("Turn " + term + " on in the list, then come back here.")
	}
	for {
		again, err := ui.Confirm(ctx, "Check again whether curio can read "+src.Name()+"?", true)
		if err != nil || !again {
			return false, err
		}
		switch av := src.Check(ctx); av.State {
		case importer.Available:
			return true, nil
		case importer.NeedsPermission:
			ui.Info("Still withheld: macOS applies the change to " + term + " only after it restarts. " +
				"Quit and reopen it, then run `curio up` again.")
		case importer.NotInstalled, importer.Unreadable:
			ui.Warn(src.Name() + ": " + av.Reason)
			return false, nil
		}
	}
}

// indexRate is the index rate measured on this Mac, or why it wasn't.
type indexRate struct {
	chunksPerSecond float64
	why             string
}

// indexRate measures how fast this Mac indexes, once a run: with the
// home's embedding model, at its width, as its config.yaml says to reach
// Ollama, each request bounded by embedding.timeout_seconds. The rate is
// zero, with why, when there is no home to measure for or the measurement
// fails.
func (w *world) indexRate(ctx context.Context, hs homeState) (float64, string) {
	if r := w.imp.rate; r != nil {
		return r.chunksPerSecond, r.why
	}
	rate, why := w.measure(ctx, hs)
	w.imp.rate = &indexRate{chunksPerSecond: rate, why: why}
	return rate, why
}

func (w *world) measure(ctx context.Context, hs homeState) (float64, string) {
	cfg := w.baseConfig(hs)
	model, dim := cfg.Embedding.Model, cfg.Embedding.Dim
	switch {
	case hs.kind == homeOurs && !w.freshPending() && hs.metaErr == nil:
		model, dim = hs.meta.EmbeddingModel, hs.meta.EmbeddingDim
	case w.newEmbeddingModel() != w.defaults.Embedding.Model:
		return 0, "the new home's width is measured when it is made"
	}
	emb, err := embedder.NewOllama(embedder.OllamaOptions{BaseURL: cfg.Embedding.BaseURL, Model: model, Dim: dim,
		Timeout: time.Duration(cfg.Embedding.TimeoutSeconds) * time.Second})
	if err != nil {
		return 0, err.Error()
	}
	rate, err := measureIndexRate(ctx, emb, cfg.Embedding.DocumentPrefix, w.deps.Now)
	if err != nil {
		return 0, "measuring failed: " + err.Error()
	}
	return rate, ""
}

// pluralWord is word for n of it: "bookmark" or "bookmarks".
func pluralWord(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// plural is n and word for it: "1 bookmark", "3 bookmarks".
func plural(n int, word string) string { return fmt.Sprintf("%d %s", n, pluralWord(n, word)) }
