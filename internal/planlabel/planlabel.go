// Package planlabel says what a search plan's filters keep, in a person's
// words: "Videos", "From Safari", "Saved in May 2016". The dashboard's
// chips and curio search's understood line say them alike.
package planlabel

import (
	"strings"
	"time"

	"github.com/samsar/curio/internal/store"
)

// Filter says what one dimension of a plan's filters keeps: field names it
// (content_type, source, site, host, folder, saved or published), values
// are a list dimension's, and after and before a window's bounds, each a
// day as written or an RFC 3339 time, whose day is the one in loc.
func Filter(field string, values []string, after, before string, loc *time.Location) string {
	switch field {
	case "content_type":
		return orList(mapped(values, ContentType))
	case "source":
		return "From " + orList(mapped(values, Source))
	case "site":
		return "On " + orList(values)
	case "host":
		return "At " + orList(values)
	case "folder":
		return "In " + orList(values)
	case "saved":
		return window("Saved", after, before, loc)
	case "published":
		return window("Published", after, before, loc)
	}
	return field
}

// ContentType names a content type's pages; one it doesn't know is said as
// stored.
func ContentType(t string) string {
	switch store.ContentType(t) {
	case store.ContentTypeArticle:
		return "Articles"
	case store.ContentTypeRepo:
		return "Repos"
	case store.ContentTypeVideo:
		return "Videos"
	case store.ContentTypePDF:
		return "PDFs"
	case store.ContentTypeThread:
		return "Threads"
	case store.ContentTypeUnknown:
		return "Pages of no known type"
	}
	return t
}

// Source names a bookmark's source for people; one it doesn't know is said
// as stored.
func Source(source string) string {
	switch source {
	case store.SourceChrome:
		return "Chrome"
	case store.SourceSafari:
		return "Safari"
	case store.SourceFirefox:
		return "Firefox"
	case store.SourceHTML:
		return "a bookmarks file"
	case store.SourceManual:
		return "curio"
	}
	return source
}

func mapped(values []string, f func(string) string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = f(v)
	}
	return out
}

// orList joins alternatives: "a", "a or b", "a, b or c".
func orList(values []string) string {
	if len(values) < 2 {
		return strings.Join(values, "")
	}
	return strings.Join(values[:len(values)-1], ", ") + " or " + values[len(values)-1]
}

// window says a date window [after, before) in days: a whole year as "in
// 2017", a whole month as "in May 2016", else its first and last days, the
// end shown inclusive; one bound as "before" or "since" its day.
func window(verb, after, before string, loc *time.Location) string {
	from, hasFrom := day(after, loc)
	until, hasUntil := day(before, loc)
	switch {
	case hasFrom && hasUntil:
		last := until.AddDate(0, 0, -1)
		switch {
		case from.YearDay() == 1 && until.Equal(from.AddDate(1, 0, 0)):
			return verb + " in " + from.Format("2006")
		case from.Day() == 1 && until.Equal(from.AddDate(0, 1, 0)):
			return verb + " in " + from.Format("January 2006")
		case !last.After(from):
			return verb + " on " + from.Format(dayLayout)
		case from.Year() == last.Year():
			return verb + " " + from.Format("Jan 2") + " – " + last.Format(dayLayout)
		}
		return verb + " " + from.Format(dayLayout) + " – " + last.Format(dayLayout)
	case hasUntil:
		return verb + " before " + until.Format(dayLayout)
	case hasFrom:
		return verb + " since " + from.Format(dayLayout)
	}
	return verb
}

// dayLayout is a label's day: "Mar 4, 2017".
const dayLayout = "Jan 2, 2006"

// day is the calendar day of a window's bound, as midnight UTC so that days
// add up whatever loc's offsets: a day as written, or an RFC 3339 time's
// day in loc. False for "" or a bound neither parses.
func day(bound string, loc *time.Location) (time.Time, bool) {
	if t, err := time.Parse(time.DateOnly, bound); err == nil {
		return t, true
	}
	t, err := time.Parse(time.RFC3339, bound)
	if err != nil {
		return time.Time{}, false
	}
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC), true
}
