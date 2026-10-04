package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/samsar/curio/internal/client"
)

// reindexFix is what lifts a hold on the interests: re-embedding the
// library under the build that serves now.
const reindexFix = "curio reindex --all"

// interestsLine is curio status's line about where automatic rebuilds of
// the interests stand, at now, or "" while the daemon hasn't checked yet.
func interestsLine(s client.InterestsState, now time.Time) string {
	if s.State == client.StateUnknown {
		return ""
	}
	return "interests: " + interestsSummary(s, now)
}

// interestsSummary is interestsText followed by where the interest map
// stands, as status's line and doctor's check say it.
func interestsSummary(s client.InterestsState, now time.Time) string {
	text := interestsText(s, now)
	if m := mapText(s.Map); m != "" {
		text += " · " + m
	}
	return text
}

// mapText says where the interest map stands: built, how and how fast;
// failed, and why; or off. It says nothing of none, which a rebuild due
// to draw the map says itself, or without a map; a status this curio
// doesn't know is shown as it is.
func mapText(m *client.InterestsMap) string {
	if m == nil {
		return ""
	}
	switch m.Status {
	case client.MapNone:
		return ""
	case client.MapBuilt:
		var how []string
		if m.Kind != "" {
			how = append(how, m.Kind)
		}
		if m.TookMS != nil {
			how = append(how, "in "+tookText(*m.TookMS))
		}
		if len(how) == 0 {
			return "map built"
		}
		return "map built (" + strings.Join(how, ", ") + ")"
	case client.MapFailed:
		return "map failed (" + m.Error + ")"
	case client.MapOff:
		return "map off (insight.map: false)"
	}
	return "map " + m.Status
}

// tookText is ms milliseconds, to a tenth of a second from a second up.
func tookText(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	if d >= time.Second {
		d = d.Round(100 * time.Millisecond)
	}
	return d.String()
}

// mapOwed reports whether s is a rebuild due only to draw the map: the
// done rebuild drew none, with the map on, and nothing else makes one due.
func mapOwed(s client.InterestsState) bool {
	return s.State == client.StateDue && s.Map != nil && s.Map.Status == client.MapNone && s.FreshOwed == "" &&
		s.ChangedDocuments < s.RebuildAt
}

// interestsText says where automatic rebuilds stand, at now, in a
// sentence status, doctor and the outline share. A state this curio
// doesn't know reads as current.
func interestsText(s client.InterestsState, now time.Time) string {
	switch s.State {
	case client.StateUnknown:
		return "the daemon hasn't checked them yet"
	case client.StateOff:
		return "off (insight.enabled: false)"
	case client.StateNone:
		return fmt.Sprintf("waiting for %d indexed documents (%d so far)", s.RebuildAt, s.ChangedDocuments)
	case client.StateDue:
		return dueText(s)
	case client.StateQueued:
		return "a rebuild is queued"
	case client.StateRebuilding:
		return "rebuilding"
	case client.StateHeld:
		return fmt.Sprintf("rebuilds held: %s; run `%s`", s.HeldReason, reindexFix)
	case client.StateFailing:
		return fmt.Sprintf("the last rebuild failed (%s); %s", s.LastError, retryText(s, now))
	}
	return currentText(s, now)
}

// currentText is a current grouping: when it was rebuilt, and what has
// changed since against what makes the next due.
func currentText(s client.InterestsState, now time.Time) string {
	var b strings.Builder
	b.WriteString("rebuilt")
	if !s.LastRebuildAt.IsZero() {
		b.WriteString(" " + ago(now.Sub(s.LastRebuildAt)))
	}
	if s.LastKind != "" {
		fmt.Fprintf(&b, " (%s)", s.LastKind)
	}
	fmt.Fprintf(&b, " · %s changed, next at %d", plural(s.ChangedDocuments, "document"), s.RebuildAt)
	return b.String()
}

// dueText is a rebuild that is due, and what it waits for.
func dueText(s client.InterestsState) string {
	switch {
	case s.LastRebuildAt.IsZero():
		return fmt.Sprintf("the first grouping is due (%s indexed): waiting for the library to settle",
			plural(s.ChangedDocuments, "document"))
	case s.FreshOwed == client.FreshReindex:
		return "a fresh rebuild is due: waiting for the re-embedding to finish"
	case s.FreshOwed != "":
		return "a fresh rebuild is due (" + freshReason(s.FreshOwed) + "): waiting for the library to settle"
	case mapOwed(s):
		return "a rebuild is due to draw the map: waiting for the library to settle"
	}
	return fmt.Sprintf("a rebuild is due (%d changed, threshold %d): waiting for the library to settle",
		s.ChangedDocuments, s.RebuildAt)
}

// freshReason says why a fresh rebuild is owed.
func freshReason(owed string) string {
	switch owed {
	case client.FreshManual:
		return "asked for"
	case client.FreshParams:
		return "the grouping's parameters changed"
	}
	return owed
}

// retryText says when a failed rebuild is tried again.
func retryText(s client.InterestsState, now time.Time) string {
	if s.RetryAt.After(now) {
		return "retrying at " + clockTime(s.RetryAt, now)
	}
	return "retrying once the library settles"
}

// interestsCheck is doctor's check of where automatic rebuilds stand: a
// warning while they are held or failing, and then while the last
// rebuild's map failed, each with its fix.
func interestsCheck(s client.InterestsState, now time.Time) (status checkStatus, detail, hint string) {
	switch {
	case s.State == client.StateHeld:
		return statusWarn, "rebuilds held: " + s.HeldReason,
			"run `" + reindexFix + "`: the interests are regrouped once the re-embedding finishes"
	case s.State == client.StateFailing:
		return statusWarn, fmt.Sprintf("the last rebuild failed: %s; %s", s.LastError, retryText(s, now)),
			"`curio daemon logs` has the details; once the cause is fixed, `curio interests rebuild` tries again without waiting"
	case s.Map != nil && s.Map.Status == client.MapFailed:
		return statusWarn, "the last rebuild's interest map failed: " + s.Map.Error,
			"`curio interests rebuild` draws it again, and `curio daemon logs` has the details; " +
				"if it keeps failing, `insight.map: false` in config.yaml turns the map off"
	}
	return statusOK, interestsSummary(s, now), ""
}

// ago says how long ago d was, roughly: "just now", "5 min ago", "2 h
// ago", "3 days ago".
func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d/time.Hour))
	}
	return fmt.Sprintf("%d days ago", int(d/(24*time.Hour)))
}

// clockTime writes t in local time: the time of day when it is today, the
// date with it otherwise.
func clockTime(t, now time.Time) string {
	t, now = t.Local(), now.Local()
	if y, m, d := t.Date(); y == now.Year() && m == now.Month() && d == now.Day() {
		return t.Format("15:04")
	}
	return localTime(t)
}
