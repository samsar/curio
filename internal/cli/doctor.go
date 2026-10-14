package cli

import (
	"context"
	"fmt"
	"io"
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
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/textutil"
)

// newDoctorCmd diagnoses the parts of curio that fail silently.
//
// Inspired by `brew doctor`: walk through every dependency, print a
// status line per check, end with a one-line summary and a suggested
// next action if anything's wrong. Its checks of the machine, the home,
// config.yaml, Ollama, the models, the daemon, its launchd agent and the
// embeddings are `curio up`'s own (setup.Runner.Checks), so the two agree
// on what healthy means; doctor never creates the home to look at it.
func newDoctorCmd(flags *rootFlags, d deps) *cobra.Command {
	return &cobra.Command{
		Use:         "doctor",
		Short:       "Diagnose curio's environment: the Mac, Ollama and its models, the home, the daemon, the fetcher",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{ownsEnvironment: "true"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			r, err := flags.setupFor(d, d.newUI(cmd.InOrStdin(), cmd.ErrOrStderr(), false), setup.Options{})
			if err != nil {
				return err
			}
			rep := newDoctorReport()
			ctx := cmd.Context()
			for _, c := range r.Checks() {
				res := c.Run(ctx)
				rep.add(c.Name, statusOf(res.Status), res.Detail, doctorHint(res))
			}
			runOwnChecks(ctx, flags, d, rep)
			rep.print(cmd.OutOrStdout())
			if rep.failures > 0 {
				return fmt.Errorf("%d check(s) failed", rep.failures)
			}
			return nil
		},
	}
}

// statusOf is a setup check's status as a doctor line's.
func statusOf(s setup.Status) checkStatus {
	switch s {
	case setup.OK:
		return statusOK
	case setup.Warn:
		return statusWarn
	case setup.Fail:
	}
	return statusFail // a failure, or a status this curio doesn't know: no pass
}

// doctorHint is what a setup check's line suggests: for what curio up
// fixes, the commands it would run or that it does it; otherwise the
// check's own hint.
func doctorHint(res setup.Result) string {
	switch {
	case res.Fix == nil:
		return res.Hint
	case len(res.Fix.Commands) > 0:
		return "run " + textutil.ShellList(res.Fix.Commands...) + ", or `curio up`, which does it"
	default:
		return "`curio up` does it: " + res.Fix.Summary
	}
}

type checkStatus int

const (
	statusOK checkStatus = iota
	statusWarn
	statusFail
)

// marker is the status's one-character column in the report.
func (s checkStatus) marker() string {
	switch s {
	case statusOK:
		return "✓"
	case statusWarn:
		return "!"
	case statusFail:
		return "✗"
	default:
		return "?"
	}
}

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
	case statusOK:
	case statusWarn:
		r.warns++
	case statusFail:
		r.failures++
	}
}

func (r *doctorReport) print(w io.Writer) {
	for _, c := range r.checks {
		fmt.Fprintf(w, "%s %-22s %s\n", c.status.marker(), c.name, c.detail)
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

// runOwnChecks adds the checks curio up has no part in: the upstreams
// fetches depend on, the interests' rebuilds, the search fields and the
// cards left to embed, as the daemon reports them, the fetcher, and
// whether the content directory is writable. They read config.yaml, or the
// defaults when it doesn't load (its own check says why); with no home
// there is no daemon or content directory to check.
func runOwnChecks(ctx context.Context, flags *rootFlags, d deps, r *doctorReport) {
	cfg := config.Default()
	if d.defaults != nil {
		cfg = *d.defaults
	}
	path, err := daemonctl.HomePath(*flags.home)
	if err != nil {
		r.add("home", statusFail, err.Error(), "")
		return
	}
	home, homeErr := curiohome.Open(path)
	configPath := filepath.Join(path, curiohome.ConfigFile)
	// Whether GitHub requests carry a token: config.yaml's, unless the
	// daemon says otherwise (it also reads CURIO_GITHUB_TOKEN from its own
	// environment, which this shell's may not be).
	githubToken := cfg.Fetcher.GitHub.Token != ""
	if homeErr == nil {
		configPath = home.ConfigPath()
		if loaded, err := config.Load(configPath); err == nil {
			cfg = loaded
			githubToken = cfg.Fetcher.GitHub.Token != ""
		}
		daemon := client.New(daemonctl.BaseURL(cfg, *flags.daemonURL))
		health, err := daemon.Healthz(ctx)
		if err == nil && daemonctl.SameHome(health.Home, path) {
			// The services fetches depend on (the Jina fallback), as the
			// daemon has seen them answer.
			for _, u := range health.Upstreams {
				status, detail, hint := upstreamCheck(u)
				r.add(u.Name, status, detail, hint)
			}
			if health.GitHubToken != nil {
				githubToken = *health.GitHubToken
			}
			// Where automatic rebuilds of the interests stand.
			if s := health.Interests; s != nil {
				status, detail, hint := interestsCheck(*s, time.Now())
				r.add("interests", status, detail, hint)
			}
			// What the daemon's start found of the search fields.
			if f := health.DocumentFields; f != nil {
				status, detail, hint := searchFieldsCheck(*f)
				r.add("search fields", status, detail, hint)
			}
			// Whether every bookmark's saved date is classified.
			if s := health.SavedDates; s != nil {
				status, detail, hint := savedDatesCheck(*s)
				r.add("saved dates", status, detail, hint)
			}
			// Whether the daemon holds its index and cards jobs for an
			// embedding model that doesn't answer: only while it does.
			if h := health.EmbedderHold; h != nil {
				status, detail, hint := embedderHoldCheck(*h)
				r.add("index jobs", status, detail, hint)
			}
			// Whether what is left to embed has jobs to embed it: the cards,
			// and the documents to re-embed for the chunk context, which
			// the re-embed's brake may stop.
			if stats, err := daemon.Stats(ctx); err != nil {
				r.add("cards", statusWarn, "couldn't count the cards left to embed: "+err.Error(), "")
				r.add("chunk context", statusWarn, "couldn't count the documents left to re-embed: "+err.Error(), "")
			} else {
				status, detail, hint := cardsCheckOf(ctx, daemon, stats.CardsStale)
				r.add("cards", status, detail, hint)
				status, detail, hint = reembedCheckOf(ctx, daemon, stats.Reembed, health.ReembedBrake)
				r.add("chunk context", status, detail, hint)
			}
		}
	}
	status, detail, hint := githubCheck(githubToken, configPath)
	r.add("github", status, detail, hint)

	// The fetcher backend: native is always fine; web2md needs the bin.
	switch cfg.Fetcher.Default {
	case "native":
		r.add("fetcher", statusOK, "native (Go, no external deps)", "")
	case "web2md":
		bin := cfg.Fetcher.Web2MD.Bin
		if _, err := exec.LookPath(bin); err != nil {
			if _, statErr := os.Stat(bin); statErr != nil {
				r.add("fetcher", statusFail, "web2md not found at "+bin,
					"install Node + web2md, or switch to fetcher.default: native")
				break
			}
		}
		r.add("fetcher", statusOK, "web2md at "+bin, "")
	}

	if homeErr == nil {
		checkContentDir(home.ContentDir(), r)
	}
}

// searchFieldsFix is what fixes search fields the daemon's start couldn't
// check: another start, which checks their index against them and
// rebuilds it when the two disagree.
const searchFieldsFix = "restart the daemon: `curio daemon stop`, then any curio command starts it again, and its " +
	"start checks the index and rebuilds it; if that fails too, see \"database disk image is malformed\" under " +
	"Troubleshooting in docs/setup.md"

// searchFieldsCheck is doctor's check of the documents' search fields as
// the daemon's start found them. It says when the start checked, so it is
// never mistaken for a check doctor made.
func searchFieldsCheck(f client.DocumentFields) (status checkStatus, detail, hint string) {
	at := localTime(f.CheckedAt)
	switch {
	case f.Error != "":
		return statusFail, fmt.Sprintf("the daemon's start (%s) couldn't check them: %s; search may miss documents",
			at, f.Error), searchFieldsFix
	case f.Rebuilt:
		return statusOK, fmt.Sprintf("checked when the daemon started (%s); their index didn't match them, "+
			"and was rebuilt", at), ""
	}
	return statusOK, fmt.Sprintf("checked when the daemon started (%s)", at), ""
}

// savedDatesRestart is how a restart reclassifies every bookmark's saved
// date: the daemon's start does.
const savedDatesRestart = "restart the daemon: `curio daemon stop`, then any curio command starts it again, and " +
	"its start classifies every bookmark"

// savedDatesCheck is doctor's check of the bookmarks' saved-date classes:
// it fails while the daemon's start couldn't classify them, or an import's
// source is owed a classification, whose bookmarks claim no date until
// the daemon's retry, every minute, or a restart classifies them. It says
// when the start classified, so it is never mistaken for a check doctor
// made.
func savedDatesCheck(s client.SavedDates) (status checkStatus, detail, hint string) {
	at := localTime(s.CheckedAt)
	switch {
	case s.Error != "":
		return statusFail, fmt.Sprintf("the daemon's start (%s) couldn't classify them: %s; date filters read the "+
			"classes the bookmarks had", at, s.Error), savedDatesRestart
	case len(s.Owed) > 0:
		oldest := s.Owed[0]
		for _, o := range s.Owed[1:] {
			if o.Since.Before(oldest.Since) {
				oldest = o
			}
		}
		return statusFail, fmt.Sprintf("the %s bookmarks an import stored aren't classified, failing since %s: %s; "+
				"date filters leave them out", owedSources(s.Owed), localTime(oldest.Since), oldest.Error),
			"the daemon retries every minute; or " + savedDatesRestart
	}
	return statusOK, fmt.Sprintf("classified when the daemon started (%s)", at), ""
}

// cardsCheckOf asks c for the cards jobs pending and running when cards
// are left to embed (stale), and checks them (cardsCheck).
func cardsCheckOf(ctx context.Context, c *client.Client, stale int) (status checkStatus, detail, hint string) {
	if stale == 0 {
		return cardsCheck(0, nil, nil)
	}
	pending, err := newestJobs(ctx, c, store.JobKindCards, store.JobStatusPending)
	if err != nil {
		return statusWarn, "couldn't list the cards jobs: " + err.Error(), ""
	}
	running, err := newestJobs(ctx, c, store.JobKindCards, store.JobStatusRunning)
	if err != nil {
		return statusWarn, "couldn't list the cards jobs: " + err.Error(), ""
	}
	return cardsCheck(stale, pending, running)
}

// newestJobs lists the jobs of kind in status, the newest alone: whether
// there are any is what the checks ask (a tenant has at most one cards job
// pending, and one runs at a time).
func newestJobs(ctx context.Context, c *client.Client, kind store.JobKind, status store.JobStatus) ([]client.Job,
	error) {
	list, err := c.ListJobs(ctx, client.JobListOpts{Kind: string(kind), Status: string(status), Limit: 1})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

// embedderFix is what makes the embedding model answer the daemon again.
const embedderFix = "start Ollama, and make sure it serves the embedding model: the ollama and models checks " +
	"say how, and `curio up` does it; the daemon asks every minute, and its jobs resume at the first answer"

// embedderHoldCheck is doctor's check of the daemon's hold over its index
// and cards jobs, which it reports only while the embedding model doesn't
// answer: a warning, since the jobs wait without failing.
func embedderHoldCheck(h client.EmbedderHold) (status checkStatus, detail, hint string) {
	return statusWarn, fmt.Sprintf("held since %s: the embedding model doesn't answer (%s); index and cards jobs "+
		"wait for it, failing nothing", localTime(h.Since), h.Error), embedderFix
}

// reembedCheckOf asks c whether index jobs are pending or running when
// documents are left to re-embed for the chunk context (left, nil when
// none is), and checks them with the re-embed's brake (reembedCheck).
func reembedCheckOf(ctx context.Context, c *client.Client, left *client.ReembedStats,
	brake *client.ReembedBrake) (status checkStatus, detail, hint string) {
	if left == nil {
		return reembedCheck(nil, false, nil)
	}
	if brake != nil {
		return reembedCheck(left, false, brake)
	}
	queued := false
	for _, status := range []store.JobStatus{store.JobStatusRunning, store.JobStatusPending} {
		jobs, err := newestJobs(ctx, c, store.JobKindIndex, status)
		if err != nil {
			return statusWarn, "couldn't list the index jobs: " + err.Error(), ""
		}
		queued = queued || len(jobs) > 0
	}
	return reembedCheck(left, queued, nil)
}

// reembedRestart is how a restart resumes a re-embed that stopped: the
// daemon's start feeds it again, a document whose re-embed keeps failing
// for its own reasons keeping its earlier vectors after its third failure.
const reembedRestart = "restart the daemon (`curio daemon stop`, then any curio command starts it): its start " +
	"resumes the re-embed, which tries a document that fails on its own up to 3 times, then leaves it its " +
	"earlier vectors"

// reembedBrakeFix is what lets a re-embed its brake stopped go on.
const reembedBrakeFix = "check that Ollama runs and serves the embedding model (`ollama list`, `ollama ps`, and " +
	"Ollama's log): the daemon tries one document again at that time and goes on once one is re-embedded; a " +
	"restart (`curio daemon stop`, then any curio command starts it) tries at once. `curio docs show <id>` " +
	"shows one of the documents named, and `curio refetch <id>` fetches it again"

// reembedCheck is doctor's check of what is left to re-embed for the chunk
// context (nil: nothing), given whether index jobs are queued (pending or
// running) and the re-embed's brake (nil while it is off): a warning while
// the brake stops it, which an embedding model fixed lets go on, fine
// while jobs are queued, which the re-embed is, and a warning when none
// is, which a restart fixes. A re-embed stops when its start couldn't
// begin it (the search fields unchecked, or the database failing).
func reembedCheck(left *client.ReembedStats, queued bool, brake *client.ReembedBrake) (status checkStatus, detail,
	hint string) {
	if left == nil {
		return statusOK, "every document's chunks are embedded with their context", ""
	}
	them := "them"
	if left.Documents == 1 {
		them = "it"
	}
	docs := fmt.Sprintf("%s (%s) to re-embed for the new chunk context%s", plural(left.Documents, "document"),
		plural(left.Chunks, "chunk"), reembedETA(left))
	if brake != nil {
		return statusWarn, fmt.Sprintf("%s; the re-embed waits until %s, the re-embeds of %s having failed in a "+
			"row%s: %s; until then each keeps its earlier vectors", docs, localTime(brake.Until),
			plural(brake.Failures, "document"), brakeDocuments(*brake), brake.Error), reembedBrakeFix
	}
	if queued {
		return statusOK, docs + "; index jobs are queued to re-embed " + them, ""
	}
	return statusWarn, docs + ", and no index job is queued to re-embed " + them, reembedRestart
}

// brakeDocuments names the documents whose failures put the re-embed's
// brake on, as the daemon names them: " (a, b and c)", " (a, b, c, d, e and
// 2 more)", or "" from a daemon that names none.
func brakeDocuments(b client.ReembedBrake) string {
	names := slices.Clone(b.Documents)
	if len(names) == 0 {
		return ""
	}
	if more := b.Failures - len(names); more > 0 {
		names = append(names, fmt.Sprintf("%d more", more))
	}
	if len(names) == 1 {
		return " (" + names[0] + ")"
	}
	return " (" + strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1] + ")"
}

// reembedETA is how long the re-embed has left at its rate, as status and
// doctor say it: ", about 20 min left at 10.4 chunks/s", or "" until the
// daemon has measured it.
func reembedETA(left *client.ReembedStats) string {
	if left.ETASeconds <= 0 {
		return ""
	}
	return fmt.Sprintf(", %s left at %.1f chunks/s", roughly(time.Duration(left.ETASeconds)*time.Second),
		left.ChunksPerSecond)
}

// roughly says how long d is, to the minute: "under a minute", "about 20
// min", "about 1 h 25 min".
func roughly(d time.Duration) string {
	d = d.Round(time.Minute)
	switch {
	case d < time.Minute:
		return "under a minute"
	case d < time.Hour:
		return fmt.Sprintf("about %d min", int(d/time.Minute))
	}
	return fmt.Sprintf("about %d h %d min", int(d/time.Hour), int(d%time.Hour/time.Minute))
}

// cardsCheck is doctor's check of the cards left to embed, stale of them,
// given the cards jobs pending and running: fine while a job runs or waits
// to run, a warning while the job waiting carries an error (deferred while
// the embedding model doesn't answer, or retrying), and a warning when none
// is queued, which a restart fixes: the daemon's start queues one.
func cardsCheck(stale int, pending, running []client.Job) (status checkStatus, detail, hint string) {
	left := fmt.Sprintf("%d cards to embed", stale)
	switch {
	case stale == 0:
		return statusOK, "every card is embedded", ""
	case len(running) > 0:
		return statusOK, left + "; the cards job is running", ""
	case len(pending) > 0 && pending[0].LastError == nil:
		return statusOK, left + "; the cards job runs at " + localTime(pending[0].RunAfter), ""
	case len(pending) > 0:
		return statusWarn, left + "; the cards job waits to run at " + localTime(pending[0].RunAfter) + ": " +
			*pending[0].LastError, "`curio jobs --status pending` shows it; it runs again by itself"
	}
	return statusWarn, left + ", and no cards job is queued to embed them",
		"restart the daemon (`curio daemon stop`, then any curio command starts it): its start queues the job"
}

// githubCheck says whether GitHub requests carry a token. Without one
// GitHub allows 60 API requests an hour, which importing a library with
// many github.com pages runs through, and the pages wait for the limit to
// reset; with any token, 5,000. The token needs no access at all: it only
// identifies the requests.
func githubCheck(hasToken bool, configPath string) (status checkStatus, detail, hint string) {
	if hasToken {
		return statusOK, "a token is set: 5,000 API requests an hour", ""
	}
	const noToken = "no token: GitHub allows 60 API requests an hour, so github.com pages wait for its hourly " +
		"limit (about 30 repositories an hour)"
	return statusWarn, noToken,
		"any token works, even one that can access nothing: a classic token with no scopes ticked, or a " +
			"fine-grained token with public repositories (read-only) and no permissions. Set fetcher.github.token in " +
			configPath + ", then `curio daemon stop` (the next command starts it again)"
}

// checkContentDir checks that the daemon can write extracted content to dir
// by writing, then removing, a probe file.
func checkContentDir(dir string, r *doctorReport) {
	probe := filepath.Join(dir, ".doctor-probe")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		r.add("content dir", statusFail, "cannot create "+dir+": "+err.Error(), "")
		return
	}
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		r.add("content dir", statusFail, "not writable: "+err.Error(), "fix perms on "+dir)
		return
	}
	if err := os.Remove(probe); err != nil {
		r.add("content dir", statusWarn, dir+" writable, but the probe file stayed: "+err.Error(), "delete "+probe)
		return
	}
	r.add("content dir", statusOK, dir+" writable", "")
}

// upstreamCheck is doctor's check of an upstream the daemon reports, the
// Jina fallback today. It renders the state the daemon derived, and the
// sites the upstream blocks for now, which leave the status alone; only the
// failure class the hint is about is chosen here (dominantFailure).
func upstreamCheck(u client.UpstreamHealth) (status checkStatus, detail, hint string) {
	if u.State == client.UpstreamDisabled {
		return statusOK, "off (fetcher.native.jina_fallback: false)", ""
	}
	status, detail, hint = upstreamState(u)
	return status, detail + sitePausesText(u.SitePauses), hint
}

// upstreamState renders the state of an upstream that is configured on.
func upstreamState(u client.UpstreamHealth) (status checkStatus, detail, hint string) {
	window := windowText(u.WindowSeconds)
	switch u.State {
	case client.UpstreamIdle:
		return statusOK, "no calls in the last " + window + lastAnswer(u), ""
	case client.UpstreamOK:
		calls, _ := callCounts(u.Recent)
		return statusOK, fmt.Sprintf("%d calls in the last %s (%s)%s", calls, window, formatMap(u.Recent), lastAnswer(u)), ""
	case client.UpstreamDegraded:
		calls, failed := callCounts(u.Recent)
		return statusWarn, fmt.Sprintf("degraded: %d of %d calls in the last %s failed (%s)",
			failed, calls, window, formatMap(u.Recent)), upstreamHint(u)
	case client.UpstreamPaused:
		return statusWarn, "paused until " + localTime(u.CooldownUntil), upstreamHint(u)
	case client.UpstreamFailing:
		return statusFail, fmt.Sprintf("failing: no answer since %s; last failure %s at %s",
			noAnswerSince(u), u.LastFailureClass, localTime(u.LastFailureAt)), upstreamHint(u)
	}
	return statusWarn, fmt.Sprintf("state %q, which this curio doesn't know", u.State), "update curio"
}

// maxSitePausesShown is how many blocked sites doctor names before it
// counts the rest: enough for the few a busy import trips at once.
const maxSitePausesShown = 3

// sitePausesText says which sites Jina Reader blocks for now and until
// when, after a "; ", or nothing when it blocks none.
func sitePausesText(pauses []client.SitePause) string {
	if len(pauses) == 0 {
		return ""
	}
	sites := "1 site"
	if len(pauses) > 1 {
		sites = fmt.Sprintf("%d sites", len(pauses))
	}
	named := make([]string, 0, maxSitePausesShown+1)
	for _, p := range pauses[:min(len(pauses), maxSitePausesShown)] {
		named = append(named, p.Site+" until "+localTime(p.Until))
	}
	if more := len(pauses) - maxSitePausesShown; more > 0 {
		named = append(named, fmt.Sprintf("and %d more", more))
	}
	return fmt.Sprintf("; Jina Reader blocks keyless reads of %s for now: %s", sites, strings.Join(named, ", "))
}

// failureClasses are the call classes that count as an upstream's failures.
var failureClasses = []string{
	client.CallChallenged, client.CallForbidden, client.CallRateLimited,
	client.CallAuth, client.CallServerError, client.CallNetwork,
}

// jinaAdvice says, per failure class, what it means for the Jina fallback,
// the one upstream the daemon reports, and what to do about it.
var jinaAdvice = map[string]string{
	client.CallChallenged: "r.jina.ai is challenging curio; see Troubleshooting in docs/setup.md",
	client.CallForbidden:  "r.jina.ai answered HTTP 403 without naming a target; see `curio daemon logs`",
	client.CallRateLimited: "Jina is rate-limiting curio, or blocking keyless reads of a busy site, " +
		"which curio waits out; a fetcher.native.jina_api_key raises the limit (a block names anonymous access only)",
	client.CallAuth: "check fetcher.native.jina_api_key or CURIO_JINA_API_KEY " +
		"(401: the key is invalid, 402: it has no balance left)",
	client.CallServerError: "r.jina.ai is failing on its side; fetches keep retrying",
	client.CallNetwork:     "curio can't reach r.jina.ai, or it doesn't answer in time; check connectivity",
}

// upstreamHint names the failure class that dominates u and what to do
// about it, or is empty when there is none to name.
func upstreamHint(u client.UpstreamHealth) string {
	class := dominantFailure(u)
	advice, ok := jinaAdvice[class]
	if !ok {
		return ""
	}
	return class + ": " + advice
}

// dominantFailure is the failure class with the most calls in u's window,
// or its last failure's class when two classes tie for the most or the
// window holds no failure.
func dominantFailure(u client.UpstreamHealth) string {
	var best string
	var most int
	tie := false
	for _, class := range failureClasses {
		switch n := u.Recent[class]; {
		case n > most:
			best, most, tie = class, n, false
		case n > 0 && n == most:
			tie = true
		}
	}
	if best == "" || tie {
		return u.LastFailureClass
	}
	return best
}

// callCounts sums an upstream's recent calls, and those of them that
// failed.
func callCounts(recent map[string]int) (calls, failed int) {
	for class, n := range recent {
		calls += n
		if slices.Contains(failureClasses, class) {
			failed += n
		}
	}
	return calls, failed
}

// lastAnswer says when an upstream last answered, after a "; ", or nothing
// when it hasn't since the daemon started.
func lastAnswer(u client.UpstreamHealth) string {
	if u.LastSuccessAt.IsZero() {
		return ""
	}
	return "; last answer " + localTime(u.LastSuccessAt)
}

// noAnswerSince says since when an upstream has not answered.
func noAnswerSince(u client.UpstreamHealth) string {
	if u.LastSuccessAt.IsZero() {
		return "the daemon started"
	}
	return localTime(u.LastSuccessAt)
}

// windowText writes a window of seconds without its zero units: "15m",
// "1h", "1m30s".
func windowText(seconds int) string {
	s := (time.Duration(seconds) * time.Second).String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// localTime writes t in local time, to the minute.
func localTime(t time.Time) string {
	return t.Local().Format("2006-01-02 15:04")
}
