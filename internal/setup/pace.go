package setup

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/daemonctl"
	"github.com/samsar/curio/internal/store"
)

// Pace is when the queue works through an import.
type Pace int

const (
	// PaceFull works through it now, with every worker.
	PaceFull Pace = iota
	// PaceGentle works through it now, throttled gently.
	PaceGentle
	// PaceOvernight works through it only overnight.
	PaceOvernight
)

func (p Pace) String() string {
	switch p {
	case PaceFull:
		return "now, at full speed"
	case PaceGentle:
		return "now, gently"
	case PaceOvernight:
		return "only overnight, " + overnightText
	default:
		return fmt.Sprintf("pace(%d)", int(p))
	}
}

// overnight is the window the overnight pace keeps the queue to.
var overnight = store.DailyWindow{Start: 22 * 60, End: 7 * 60}

// overnightText is overnight as a sentence says it.
const overnightText = "22:00 to 07:00"

// paceUpdate is the one queue update a pace and a keep-awake answer make:
// the queue opened, the pace's throttle and schedule, and keep-awake as
// answered.
func paceUpdate(p Pace, keepAwake bool) client.QueueUpdate {
	u := client.QueueUpdate{Paused: new(false), Throttle: client.ThrottleNormal, Schedule: client.ScheduleOff,
		KeepAwake: new(keepAwake)}
	switch p {
	case PaceGentle:
		u.Throttle = client.ThrottleGentle
	case PaceOvernight:
		u.Schedule = overnight.String()
	case PaceFull:
	}
	return u
}

// paced is the estimate of an import at pace p, and when to check back.
func paced(p Pace, pages int, cfg config.Config, rate float64, why string, now time.Time) (Estimate, time.Time) {
	limit, window := cfg.Daemon.FetchWorkers, store.DailyWindow{}
	switch p {
	case PaceGentle:
		limit = store.ThrottleGentle.Limit(store.JobKindFetch, limit)
	case PaceOvernight:
		window = overnight
	case PaceFull:
	}
	e := estimateImport(pages, limit, rate, why)
	return e, finishAt(now, e.Work(), window)
}

// choosePace says how long the import's new pages take, asks when the
// queue should work through them (full speed; gently; only overnight, the
// default on a Mac with under 16 GiB), then whether to keep the Mac awake,
// defaulting to the queue's own setting so --yes never turns it on, and
// sets both on the queue in one update, before a bookmark is sent.
func (w *world) choosePace(ctx context.Context, ui UI, env daemonctl.Env, hs homeState, pages []string,
	report *ImportReport) error {
	cfg := w.baseConfig(hs)
	rate, why := w.indexRate(ctx, hs)
	now := w.deps.Now()
	full, _ := paced(PaceFull, len(pages), cfg, rate, why, now)
	st, err := env.Controller.Status(ctx)
	managed := err == nil && st.Managed()
	for _, line := range importNotes(full, rate, pages, cfg, hs.home.ConfigPath(), managed) {
		ui.Info(line)
	}

	fetchCap, _ := store.ThrottleGentle.Cap(store.JobKindFetch)
	indexCap, _ := store.ThrottleGentle.Cap(store.JobKindIndex)
	labels := map[Pace]string{
		PaceFull: "Now, at full speed",
		PaceGentle: fmt.Sprintf("Now, gently (at most %d fetches and %d embedding at a time, to keep the Mac cool)",
			fetchCap, indexCap),
		PaceOvernight: "Only overnight, " + overnightText,
	}
	paces := []Pace{PaceFull, PaceGentle, PaceOvernight}
	options := make([]string, len(paces))
	for i, p := range paces {
		_, at := paced(p, len(pages), cfg, rate, why, now)
		options[i] = fmt.Sprintf("%s: check back after %s", labels[p], CheckBackText(now, at))
	}
	def := PaceFull
	if m, err := w.probeMachine(ctx); err == nil && m.Memory > 0 && m.Memory < smallTier {
		def = PaceOvernight
	}
	i, err := ui.Select(ctx, "When should curio work through them?", options, int(def))
	if err != nil {
		return err
	}
	pace := paces[i]

	keepAwake, err := askKeepAwake(ctx, ui, env, pace)
	if err != nil {
		return err
	}
	if _, err := env.Client.UpdateQueue(ctx, paceUpdate(pace, keepAwake)); err != nil {
		return fmt.Errorf("set the queue to import %s, keep-awake %s: %w", pace, onOff(keepAwake), err)
	}
	report.Pace, report.KeepAwake = pace, keepAwake
	_, report.CheckBack = paced(pace, len(pages), cfg, rate, why, now)
	return nil
}

// askKeepAwake asks whether to keep the Mac awake while the queue has
// work, the queue's own setting first, so it is the default (off when it
// can't be read).
func askKeepAwake(ctx context.Context, ui UI, env daemonctl.Env, pace Pace) (bool, error) {
	current := false
	if q, err := env.Client.Queue(ctx); err == nil {
		current = q.KeepAwake
	}
	ui.Info("Keep-awake holds the Mac out of idle sleep only on AC power, and only while there is work queued; " +
		"closing a laptop's lid still sleeps it.")
	if pace == PaceOvernight {
		ui.Info("With only overnight, it also keeps the Mac awake until the window opens at 22:00.")
	}
	answers := []string{"no", "yes"}
	if current {
		answers = []string{"yes", "no"}
	}
	i, err := ui.Ask(ctx, "Keep the Mac awake while it imports?", answers...)
	if err != nil {
		return false, err
	}
	return answers[i] == "yes", nil
}

// onOff says a switch.
func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// importNotes are what to know before an import of pages starts: how long
// it takes at full speed, the sites that may take longer, the GitHub pages
// that need a token, the GPU, and what closing the terminal or sleeping
// does. rate is the measured index rate, 0 when unknown; configPath is
// where fetcher.github.token goes; managed says launchd keeps the daemon
// running.
func importNotes(e Estimate, rate float64, pages []string, cfg config.Config, configPath string, managed bool) []string {
	notes := []string{
		fmt.Sprintf("%s to fetch and index:", plural(e.Pages, "new page")),
		fmt.Sprintf("  fetching: %s at full speed (%d to %d pages a minute, %d at a time from any one site)",
			e.Fetching(), fetchPerMinuteLow, fetchPerMinuteHigh, perHostInFlight),
	}
	if rate > 0 {
		notes = append(notes, fmt.Sprintf("  indexing: %s (this Mac embeds about %.0f chunks a second, and a page "+
			"makes about %d)", e.Indexing(), rate, chunksPerPage))
	} else {
		notes = append(notes, "  indexing: "+e.Indexing())
	}
	if hosts := dominantHosts(pages, cfg.Daemon.FetchWorkers); len(hosts) > 0 {
		named := make([]string, len(hosts))
		for i, h := range hosts {
			named[i] = fmt.Sprintf("%s (%d)", h.Host, h.Pages)
		}
		notes = append(notes, fmt.Sprintf("  %s hold many of them, and curio fetches at most %d pages at a time "+
			"from one site: those may take longer than this", strings.Join(named, ", "), perHostInFlight))
	}
	if n := countHost(pages, githubHost); n > 0 && cfg.Fetcher.GitHub.Token == "" {
		notes = append(notes, fmt.Sprintf("  %s on github.com: without a token GitHub allows 60 API requests "+
			"an hour, 2 a repository. Set fetcher.github.token in %s, then `curio daemon stop` (the next command "+
			"starts it again); `curio refetch --all --state=failed` fetches the ones that fail",
			plural(n, "page"), configPath))
	}
	restart := "the next curio command starts the daemon again"
	if managed {
		restart = "launchd keeps the daemon running"
	}
	return append(notes,
		"While it indexes, Ollama keeps the GPU busy: `curio throttle gentle` eases it.",
		"Closing the terminal or restarting the Mac is fine: "+restart+". A sleeping Mac pauses the import.")
}
