package fetcher

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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

type YouTubeOptions struct {
	Bin      string
	Timeout  time.Duration
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
	log      *slog.Logger
}

func NewYouTube(opts YouTubeOptions) *YouTube {
	if opts.Timeout == 0 {
		opts.Timeout = 60 * time.Second
	}
	if opts.SubLangs == "" {
		opts.SubLangs = "en.*,en"
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
		log:      opts.Log,
	}
}

func (y *YouTube) Name() string { return "youtube" }

func (y *YouTube) Fetch(ctx context.Context, rawURL string) (*Result, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("youtube: invalid url: %w", err)
	}
	videoID, ok := urlutil.YouTubeVideoID(u)
	if !ok {
		return nil, &PermanentError{Err: fmt.Errorf("youtube: cannot extract video ID from %s", rawURL)}
	}

	canonicalURL := "https://www.youtube.com/watch?v=" + videoID

	// Queue for a process slot before the per-run timeout starts, so time
	// spent waiting doesn't count against it.
	select {
	case y.slots <- struct{}{}:
		defer func() { <-y.slots }()
	case <-ctx.Done():
		return nil, fmt.Errorf("youtube: wait for a yt-dlp slot: %w", ctx.Err())
	}

	tmpDir, err := os.MkdirTemp("", "curio-yt-*")
	if err != nil {
		return nil, fmt.Errorf("youtube: create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	meta, err := y.runYTDLP(ctx, canonicalURL, tmpDir)
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

	return result, nil
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
	// Available caption tracks by language: uploaded ones and YouTube's
	// automatic ones. Only the keys matter; they say which kind a
	// downloaded <id>.<lang>.vtt is.
	Subtitles         map[string]json.RawMessage `json:"subtitles"`
	AutomaticCaptions map[string]json.RawMessage `json:"automatic_captions"`
}

func (y *YouTube) runYTDLP(ctx context.Context, videoURL, tmpDir string) (*ytdlpMeta, error) {
	args := []string{
		"--write-info-json",
		"--write-subs", "--write-auto-subs",
		"--sub-langs", y.subLangs,
		"--skip-download",
		"--no-playlist",
		"-o", filepath.Join(tmpDir, "%(id)s"),
		videoURL,
	}

	stderr, err := runCapped(ctx, y.timeout, nil, y.bin, args...)
	if err != nil {
		msg := extractYTDLPError(stderr)
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && isYTDLPPermanent(msg) {
			return nil, &PermanentError{Err: fmt.Errorf("youtube: %s", msg)}
		}
		return nil, toolError("youtube", err, msg)
	}

	infoFiles, err := filepath.Glob(filepath.Join(tmpDir, "*.info.json"))
	if err != nil {
		return nil, fmt.Errorf("youtube: find info.json: %w", err)
	}
	if len(infoFiles) == 0 {
		return nil, fmt.Errorf("youtube: yt-dlp produced no info.json")
	}
	data, err := os.ReadFile(infoFiles[0])
	if err != nil {
		return nil, fmt.Errorf("youtube: read info.json: %w", err)
	}

	var meta ytdlpMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("youtube: parse yt-dlp json: %w", err)
	}
	return &meta, nil
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
	for _, line := range strings.Split(stderr, "\n") {
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
