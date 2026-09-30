package fetcher

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/urlutil"
)

// DefaultYouTubeSubLangs is the yt-dlp --sub-langs value used when
// YouTubeOptions.SubLangs is empty. yt-dlp full-matches each
// comma-separated item, as a case-insensitive regular expression, against
// the language key of every caption track, uploaded and automatic, and
// downloads each match: one caption request per matched track.
//
//   - "en" is the uploaded English track or, without one, YouTube's
//     automatic track; for a video in another language that is its
//     captions machine-translated into English.
//   - "en-(?-i:[A-Z]{2})" adds uploaded regional English tracks (en-GB,
//     en-US). The region is matched case-sensitively, which keeps out the
//     translations YouTube keys "en-<source language>" (en-zh, en-ca,
//     en-en-GB, one per uploaded caption language) and "en-orig", a copy
//     of the automatic track.
//
// That is one or two caption requests per video, where "en.*" made one
// more for every caption language the video was uploaded with.
const DefaultYouTubeSubLangs = "en,en-(?-i:[A-Z]{2})"

type YouTubeOptions struct {
	Bin     string
	Timeout time.Duration
	// SubLangs is passed to yt-dlp's --sub-langs as is; empty means
	// DefaultYouTubeSubLangs.
	SubLangs string
	// MaxConcurrent bounds how many yt-dlp processes run at once. Default
	// 2: each one is slow and talks to YouTube, whose anti-bot measures
	// punish bursts.
	MaxConcurrent int
	Log           *slog.Logger
}

type YouTube struct {
	bin      string
	timeout  time.Duration
	subLangs string
	slots    chan struct{} // one per running yt-dlp process
	cooldown cooldown      // shared by every run; a 429 extends it
	clock    clock
	log      *slog.Logger
}

const (
	// youtubeRateLimitCooldown is how long every yt-dlp run waits after one
	// of them met an HTTP 429. yt-dlp passes on no Retry-After, and
	// YouTube's caption and player throttles are per IP and last minutes,
	// so this is a fixed step, longer than GitHub's minute without a hint.
	youtubeRateLimitCooldown = 2 * time.Minute
	// maxInlineYouTubeWait is the longest cooldown a fetch sits out before
	// running yt-dlp. A longer one defers the fetch at once: sitting it out
	// would hold a fetch worker.
	maxInlineYouTubeWait = 30 * time.Second
	// youTubeHoldReason is what a deferred YouTube fetch waits for.
	youTubeHoldReason = "YouTube's rate limit to pass"
)

func NewYouTube(opts YouTubeOptions) *YouTube {
	if opts.Timeout == 0 {
		opts.Timeout = 60 * time.Second
	}
	if opts.SubLangs == "" {
		opts.SubLangs = DefaultYouTubeSubLangs
	}
	if opts.MaxConcurrent <= 0 {
		opts.MaxConcurrent = 2
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	return &YouTube{
		bin:      opts.Bin,
		timeout:  opts.Timeout,
		subLangs: opts.SubLangs,
		slots:    make(chan struct{}, opts.MaxConcurrent),
		clock:    realClock,
		log:      opts.Log,
	}
}

func (*YouTube) Name() string { return "youtube" }

func (y *YouTube) Fetch(ctx context.Context, rawURL string) (*Result, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("youtube: invalid url: %w", err)
	}
	videoID, ok := urlutil.YouTubeVideoID(u)
	if !ok {
		return nil, &PermanentError{Err: fmt.Errorf("youtube: cannot extract video ID from %s (%w URL)", rawURL, ErrUnsupported)}
	}

	canonicalURL := "https://www.youtube.com/watch?v=" + videoID

	// A cooldown too long to sit out defers the fetch before it queues for
	// a slot.
	if err := y.awaitCooldown(ctx, canonicalURL); err != nil {
		return nil, err
	}
	// Queue for a process slot before the per-run timeout starts, so time
	// spent waiting doesn't count against it.
	select {
	case y.slots <- struct{}{}:
		defer func() { <-y.slots }()
	case <-ctx.Done():
		return nil, fmt.Errorf("youtube: wait for a yt-dlp slot: %w", ctx.Err())
	}
	// A 429 that another run met while this one queued pauses it too.
	if err := y.awaitCooldown(ctx, canonicalURL); err != nil {
		return nil, err
	}

	tmpDir, err := os.MkdirTemp("", "curio-yt-*")
	if err != nil {
		return nil, fmt.Errorf("youtube: create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	meta, captionFailures, err := y.runYTDLP(ctx, videoID, canonicalURL, tmpDir)
	if err != nil {
		return nil, err
	}

	transcript, source, err := findTranscript(tmpDir, meta)
	if err != nil {
		return nil, err
	}

	markdown := formatYouTubeMarkdown(meta, transcript)

	published := parseYTDate(meta.UploadDate)

	result := &Result{
		Markdown:    markdown,
		FinalURL:    canonicalURL,
		ContentType: store.ContentTypeVideo,
		Title:       meta.Title,
		Author:      meta.Channel,
		PublishedAt: published,
		// Without a transcript the document is only the description.
		Partial: transcript == "",
		Meta: map[string]any{
			"via":               "yt-dlp",
			"video_id":          videoID,
			"channel":           meta.Channel,
			"channel_id":        meta.ChannelID,
			"duration_seconds":  meta.Duration,
			"view_count":        meta.ViewCount,
			"like_count":        meta.LikeCount,
			"categories":        meta.Categories,
			"tags":              meta.Tags,
			"transcript_source": source,
		},
	}

	if meta.Language != "" {
		result.Language = meta.Language
	}
	switch {
	case result.Partial:
		result.PartialReason = y.partialReason(captionFailures)
	case len(captionFailures) > 0:
		y.log.Warn("youtube: caption track not downloaded, transcript taken from another",
			"video_id", videoID, "err", strings.Join(captionFailures, "; "))
	}

	return result, nil
}

// awaitCooldown sits out a rate-limit cooldown that ends within
// maxInlineYouTubeWait (see pace; the daemon paces yt-dlp starts itself).
// A longer one returns at once, without running yt-dlp, a *DeferError until
// the cooldown ends around a 429 carrying the time left.
func (y *YouTube) awaitCooldown(ctx context.Context, videoURL string) error {
	left, err := pace(ctx, nil, &y.cooldown, y.clock, maxInlineYouTubeWait)
	if err != nil {
		return fmt.Errorf("youtube: %w", err)
	}
	if left > 0 {
		se := &HTTPStatusError{StatusCode: http.StatusTooManyRequests, URL: videoURL, RetryAfter: left}
		return heldBack(&y.cooldown, youTubeHoldReason,
			fmt.Errorf("youtube: not run, rate-limit cooldown has %s left: %w", left.Round(time.Second), se))
	}
	return nil
}

// partialReason says why a video has no transcript: the caption downloads
// that failed, or else that no track matching sub_langs had any text.
func (y *YouTube) partialReason(captionFailures []string) string {
	if len(captionFailures) > 0 {
		return snippet([]byte("transcript not downloaded: yt-dlp: " + strings.Join(captionFailures, "; ")))
	}
	return fmt.Sprintf("no usable captions for sub_langs %q", y.subLangs)
}

type ytdlpMeta struct {
	Title       string   `json:"title"`
	Channel     string   `json:"channel"`
	ChannelID   string   `json:"channel_id"`
	UploadDate  string   `json:"upload_date"`
	Duration    float64  `json:"duration"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Categories  []string `json:"categories"`
	ViewCount   int64    `json:"view_count"`
	LikeCount   int64    `json:"like_count"`
	Language    string   `json:"language"`
	// Uploaded caption tracks by language. Only the keys matter: a
	// downloaded <id>.<lang>.vtt whose language isn't listed here is
	// YouTube's automatic track, which yt-dlp names the same way.
	Subtitles map[string]json.RawMessage `json:"subtitles"`
}

// runYTDLP runs yt-dlp for one video into tmpDir and returns its info.json
// and yt-dlp's message for each caption track it couldn't download.
func (y *YouTube) runYTDLP(ctx context.Context, videoID, videoURL, tmpDir string) (meta *ytdlpMeta, captionFailures []string, err error) {
	args := []string{
		"--write-info-json",
		"--write-subs", "--write-auto-subs",
		"--sub-langs", y.subLangs,
		// yt-dlp writes subtitles before info.json, and by default one
		// caption track that fails to download aborts the whole video: no
		// info.json, exit 1, the remaining tracks never tried. With
		// --ignore-errors that failure is a WARNING and the rest is still
		// written, while extraction errors (unavailable, private, removed,
		// bot checks, format errors) still exit non-zero.
		"--ignore-errors",
		"--skip-download",
		"--no-playlist",
		"-o", filepath.Join(tmpDir, "%(id)s"),
		videoURL,
	}

	stderr, err := runCapped(ctx, y.timeout, nil, y.bin, args...)
	rateLimited := y.noteRateLimit(stderr, videoID)
	if err != nil {
		msg := extractYTDLPError(stderr)
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && isYTDLPPermanent(msg) {
			return nil, nil, &PermanentError{Err: fmt.Errorf("youtube: %s", msg)}
		}
		err = toolError("youtube: yt-dlp", err, msg)
		if rateLimited {
			// This run met the limit, so it fails like any answer: the queue
			// retries it on its own backoff, and a retry that comes inside
			// the cooldown is deferred until it ends (awaitCooldown), as are
			// this fetcher's other runs.
			se := &HTTPStatusError{StatusCode: http.StatusTooManyRequests, URL: videoURL, RetryAfter: youtubeRateLimitCooldown}
			return nil, nil, fmt.Errorf("%w (rate limited, yt-dlp runs paused for %s: %w)", err, youtubeRateLimitCooldown, se)
		}
		return nil, nil, err
	}

	infoFiles, err := filepath.Glob(filepath.Join(tmpDir, "*.info.json"))
	if err != nil {
		return nil, nil, fmt.Errorf("youtube: find info.json: %w", err)
	}
	if len(infoFiles) == 0 {
		return nil, nil, errors.New("youtube: yt-dlp produced no info.json")
	}
	data, err := os.ReadFile(infoFiles[0])
	if err != nil {
		return nil, nil, fmt.Errorf("youtube: read info.json: %w", err)
	}

	meta = &ytdlpMeta{}
	if err := json.Unmarshal(data, meta); err != nil {
		return nil, nil, fmt.Errorf("youtube: parse yt-dlp json: %w", err)
	}
	return meta, parseCaptionFailures(stderr), nil
}

// noteRateLimit reports whether a yt-dlp run met an HTTP 429, whether it
// failed the extraction (an ERROR) or one caption download (a WARNING),
// and if so extends the cooldown every run on this fetcher shares:
// YouTube throttles per IP, so the next video would meet the same limit.
func (y *YouTube) noteRateLimit(stderr, videoID string) bool {
	if !strings.Contains(stderr, "HTTP Error 429") {
		return false
	}
	y.cooldown.extend(y.clock.now(), youtubeRateLimitCooldown)
	y.log.Warn("youtube: rate limited, pausing yt-dlp runs",
		"video_id", videoID, "cooldown", youtubeRateLimitCooldown.String())
	return true
}

// captionFailureRE matches the warning yt-dlp prints, under
// --ignore-errors, for a caption track it couldn't download:
//
//	WARNING: Unable to download video subtitles for 'en': HTTP Error 429: Too Many Requests
var captionFailureRE = regexp.MustCompile(`^WARNING: (Unable to download video subtitles for '[^']*': .*)$`)

// parseCaptionFailures returns yt-dlp's message for each caption track it
// couldn't download, from the warnings in its stderr.
func parseCaptionFailures(stderr string) []string {
	var failures []string
	for line := range strings.SplitSeq(stderr, "\n") {
		if m := captionFailureRE.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			failures = append(failures, m[1])
		}
	}
	return failures
}

var permanentPatterns = []string{
	"video unavailable",
	"private video",
	"this video has been removed",
	"sign in to confirm your age",
	"this video is not available",
	"copyright claim",
	"account associated with this video has been terminated",
}

// extractYTDLPError filters yt-dlp stderr to only ERROR lines,
// dropping WARNING lines that are noisy but harmless (e.g. "ffmpeg
// not found", impersonation warnings). Falls back to full stderr
// if no ERROR lines are found.
func extractYTDLPError(stderr string) string {
	var errLines []string
	for line := range strings.SplitSeq(stderr, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "ERROR:") {
			errLines = append(errLines, trimmed)
		}
	}
	if len(errLines) > 0 {
		return strings.Join(errLines, "; ")
	}
	return strings.TrimSpace(stderr)
}

func isYTDLPPermanent(msg string) bool {
	lower := strings.ToLower(msg)
	for _, p := range permanentPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// Transcript sources, recorded as the transcript_source meta value.
const (
	transcriptManual = "manual" // captions uploaded with the video
	transcriptAuto   = "auto"   // YouTube's speech recognition
	transcriptNone   = "none"
)

// subtitleFile is one <id>.<lang>.vtt yt-dlp downloaded.
type subtitleFile struct {
	name   string
	lang   string
	source string // transcriptManual or transcriptAuto
}

// findTranscript picks the best caption file yt-dlp wrote into tmpDir and
// returns its text and source. Uploaded captions beat automatic ones, then
// the shortest language tag wins ("en" over "en-orig"), then the
// lexicographically first. The two kinds share the <id>.<lang>.vtt naming,
// so which is which comes from info.json. A file that parses to nothing
// falls through to the next. No usable file yields source "none".
func findTranscript(tmpDir string, meta *ytdlpMeta) (transcript, source string, err error) {
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return "", "", fmt.Errorf("youtube: list subtitles: %w", err)
	}
	var files []subtitleFile
	for _, e := range entries {
		base, ok := strings.CutSuffix(e.Name(), ".vtt")
		if !ok {
			continue
		}
		_, lang, _ := strings.Cut(base, ".")
		src := transcriptAuto
		if _, uploaded := meta.Subtitles[lang]; uploaded {
			src = transcriptManual
		}
		files = append(files, subtitleFile{name: e.Name(), lang: lang, source: src})
	}
	slices.SortFunc(files, func(a, b subtitleFile) int {
		return cmp.Or(
			cmp.Compare(sourceRank(a.source), sourceRank(b.source)),
			cmp.Compare(len(a.lang), len(b.lang)),
			strings.Compare(a.lang, b.lang),
		)
	})

	for _, f := range files {
		raw, err := os.ReadFile(filepath.Join(tmpDir, f.name))
		if err != nil {
			return "", "", fmt.Errorf("youtube: read subtitles: %w", err)
		}
		if text := parseVTT(raw); text != "" {
			return text, f.source, nil
		}
	}
	return "", transcriptNone, nil
}

func sourceRank(source string) int {
	if source == transcriptManual {
		return 0
	}
	return 1
}

var (
	vttTimestampLine = regexp.MustCompile(`^\d{2}:\d{2}:\d{2}\.\d{3}\s*-->`)
	vttInlineTag     = regexp.MustCompile(`<[^>]+>`)
	vttCueID         = regexp.MustCompile(`^\d+$`)
)

func parseVTT(raw []byte) string {
	lines := strings.Split(string(raw), "\n")

	var textLines []string
	var prev string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || line == "WEBVTT" || strings.HasPrefix(line, "Kind:") ||
			strings.HasPrefix(line, "Language:") || strings.HasPrefix(line, "NOTE") {
			continue
		}
		if vttTimestampLine.MatchString(line) || vttCueID.MatchString(line) {
			continue
		}

		// Strip inline tags (<c>, </c>, <00:00:01.234>, etc.)
		cleaned := vttInlineTag.ReplaceAllString(line, "")
		cleaned = strings.TrimSpace(cleaned)
		if cleaned == "" {
			continue
		}

		// Deduplicate consecutive identical lines (auto-captions
		// use a rolling window that repeats each line).
		if cleaned == prev {
			continue
		}
		prev = cleaned
		textLines = append(textLines, cleaned)
	}

	if len(textLines) == 0 {
		return ""
	}

	// Group into paragraphs: a new paragraph every ~5 lines to give
	// the chunker reasonable units to work with.
	var paragraphs []string
	for i := 0; i < len(textLines); i += 5 {
		end := min(i+5, len(textLines))
		paragraphs = append(paragraphs, strings.Join(textLines[i:end], " "))
	}

	return strings.Join(paragraphs, "\n\n")
}

func formatYouTubeMarkdown(meta *ytdlpMeta, transcript string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# %s\n\n", meta.Title)
	if meta.Channel != "" {
		fmt.Fprintf(&b, "**Channel:** %s\n", meta.Channel)
	}
	if meta.UploadDate != "" {
		if d := formatYTDate(meta.UploadDate); d != "" {
			fmt.Fprintf(&b, "**Published:** %s\n", d)
		}
	}
	if meta.Duration > 0 {
		fmt.Fprintf(&b, "**Duration:** %s\n", formatDuration(int(meta.Duration)))
	}

	if meta.Description != "" {
		fmt.Fprintf(&b, "\n## Description\n\n%s\n", strings.TrimSpace(meta.Description))
	}

	if transcript != "" {
		fmt.Fprintf(&b, "\n## Transcript\n\n%s\n", transcript)
	}

	return b.String()
}

func parseYTDate(yyyymmdd string) *time.Time {
	if len(yyyymmdd) != 8 {
		return nil
	}
	t, err := time.Parse("20060102", yyyymmdd)
	if err != nil {
		return nil
	}
	return &t
}

func formatYTDate(yyyymmdd string) string {
	if t := parseYTDate(yyyymmdd); t != nil {
		return t.Format("2006-01-02")
	}
	return ""
}

func formatDuration(totalSeconds int) string {
	h := totalSeconds / 3600
	m := (totalSeconds % 3600) / 60
	s := totalSeconds % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

// YouTubeHosts lists the hostnames the built-in routing sends to the
// YouTube fetcher.
var YouTubeHosts = []string{
	"youtube.com",
	"www.youtube.com",
	"m.youtube.com",
	"youtu.be",
}
