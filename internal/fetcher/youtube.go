package fetcher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/samsar/curio/internal/urlutil"
)

type YouTubeOptions struct {
	Bin      string
	Timeout  time.Duration
	SubLangs string
	Log      *slog.Logger
}

type YouTube struct {
	bin      string
	timeout  time.Duration
	subLangs string
	log      *slog.Logger
}

func NewYouTube(opts YouTubeOptions) *YouTube {
	if opts.Timeout == 0 {
		opts.Timeout = 60 * time.Second
	}
	if opts.SubLangs == "" {
		opts.SubLangs = "en.*,en"
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	return &YouTube{
		bin:      opts.Bin,
		timeout:  opts.Timeout,
		subLangs: opts.SubLangs,
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

	tmpDir, err := os.MkdirTemp("", "curio-yt-*")
	if err != nil {
		return nil, fmt.Errorf("youtube: create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	ctx, cancel := context.WithTimeout(ctx, y.timeout)
	defer cancel()

	meta, err := y.runYTDLP(ctx, canonicalURL, tmpDir)
	if err != nil {
		return nil, err
	}

	transcript, source := y.findTranscript(tmpDir)

	markdown := formatYouTubeMarkdown(meta, transcript)

	published := parseYTDate(meta.UploadDate)

	result := &Result{
		Markdown:    markdown,
		FinalURL:    canonicalURL,
		ContentType: "video",
		Title:       meta.Title,
		Author:      meta.Channel,
		PublishedAt: published,
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

	cmd := exec.CommandContext(ctx, y.bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := extractYTDLPError(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		if isYTDLPPermanent(msg) {
			return nil, &PermanentError{Err: fmt.Errorf("youtube: %s", msg)}
		}
		return nil, fmt.Errorf("youtube: yt-dlp: %s", msg)
	}

	infoFiles, _ := filepath.Glob(filepath.Join(tmpDir, "*.info.json"))
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

// PermanentError signals the job system not to retry.
type PermanentError struct {
	Err error
}

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

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
	var errors []string
	for _, line := range strings.Split(stderr, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "ERROR:") {
			errors = append(errors, trimmed)
		}
	}
	if len(errors) > 0 {
		return strings.Join(errors, "; ")
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

func (y *YouTube) findTranscript(tmpDir string) (string, string) {
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return "", "none"
	}

	// Prefer manual subs over auto-generated. Manual subs don't have
	// the pattern ".en-orig" in the filename — yt-dlp writes them as
	// "<id>.<lang>.vtt". Auto-generated ones are written alongside.
	// When both --write-subs and --write-auto-subs are used, manual
	// subs take precedence in the file naming.
	var vttFiles []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".vtt") {
			vttFiles = append(vttFiles, e.Name())
		}
	}
	if len(vttFiles) == 0 {
		return "", "none"
	}

	// Pick the best subtitle file. yt-dlp names them as:
	// <id>.<lang>.vtt (manual) or <id>.<lang>.vtt (auto, when no manual exists)
	// When both exist, we get both files. Prefer the shortest name
	// (manual subs use simpler naming).
	best := vttFiles[0]
	for _, f := range vttFiles[1:] {
		if len(f) < len(best) {
			best = f
		}
	}

	raw, err := os.ReadFile(filepath.Join(tmpDir, best))
	if err != nil {
		return "", "none"
	}

	transcript := parseVTT(raw)
	if transcript == "" {
		return "", "none"
	}

	source := "manual"
	if len(vttFiles) > 1 {
		source = "manual"
	}
	// If the only file has auto-generation markers, it's auto-generated
	if len(vttFiles) == 1 && isAutoGenerated(raw) {
		source = "auto"
	}

	return transcript, source
}

func isAutoGenerated(vttContent []byte) bool {
	// Auto-generated VTT files from YouTube typically contain
	// <c> tags for word-level timing and "align:start" positioning.
	return bytes.Contains(vttContent, []byte("<c>")) ||
		bytes.Contains(vttContent, []byte("align:start"))
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

// IsYouTubeURL reports whether rawURL points to a YouTube video.
func IsYouTubeURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	_, ok := urlutil.YouTubeVideoID(u)
	return ok
}

// YouTubeHosts returns the set of hostnames the PatternDispatcher
// should route to the YouTube fetcher.
var YouTubeHosts = []string{
	"youtube.com",
	"www.youtube.com",
	"m.youtube.com",
	"youtu.be",
}
