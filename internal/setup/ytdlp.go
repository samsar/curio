package setup

import (
	"cmp"
	"context"
	"fmt"
	"net/url"

	"github.com/samsar/curio/internal/daemonctl"
	"github.com/samsar/curio/internal/textutil"
	"github.com/samsar/curio/internal/urlutil"
)

const (
	// ytdlpFormula is yt-dlp's Homebrew formula, and the name setup.json
	// records a decline under.
	ytdlpFormula = "yt-dlp"
	// ytdlpInstall is where yt-dlp is installed from without Homebrew.
	ytdlpInstall = "https://github.com/yt-dlp/yt-dlp#installation"
)

// youTubeVideos counts the URLs that are YouTube videos, as the daemon's
// routing takes them: youtube.com, www., m. and youtu.be.
func youTubeVideos(urls []string) int {
	n := 0
	for _, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		if _, ok := urlutil.YouTubeVideoID(u); ok {
			n++
		}
	}
	return n
}

// offerYTDLP makes sure the daemon routes the new YouTube videos to
// yt-dlp, which indexes their transcripts, before any is sent: a daemon
// that already does is said so; a yt-dlp installed since it started gets
// a restart; otherwise yt-dlp is offered (brew install yt-dlp, then a
// restart), unless it was declined before, --no-install is set or there
// is no Homebrew. A decline is remembered in setup.json. With no new
// YouTube video it says nothing.
func (w *world) offerYTDLP(ctx context.Context, ui UI, env daemonctl.Env, urls []string) error {
	n := youTubeVideos(urls)
	if n == 0 {
		return nil
	}
	h, err := env.Client.Healthz(ctx)
	if err != nil {
		return err
	}
	if h.YouTubeFetcher != "" {
		ui.Info(transcriptsThrough(n, h.YouTubeFetcher))
		return nil
	}
	d, known := w.detectYTDLP(ctx, ui)
	if !known {
		return nil
	}
	state := LoadState(env.Controller.Home.Path, ui)
	_, declined := state.Declined[ytdlpFormula]
	switch {
	case d.Formula || d.Binary != "":
		return routeYouTube(ctx, ui, env, cmp.Or(d.Binary, ytdlpFormula), n)
	case declined:
		ui.Info(fmt.Sprintf("the new bookmarks include %s, indexed without transcripts: yt-dlp was declined "+
			"before; `brew install yt-dlp`, then `curio daemon stop` (the next command starts it again), adds them",
			plural(n, "YouTube video")))
		return nil
	case w.opts.NoInstall:
		ui.Info(fmt.Sprintf("the new bookmarks include %s: `brew install yt-dlp`, then `curio daemon stop` "+
			"(the next command starts it again), indexes transcripts (--no-install installs nothing)",
			plural(n, "YouTube video")))
		return nil
	case d.Brew == "":
		ui.Info(fmt.Sprintf("the new bookmarks include %s: install yt-dlp (%s), then `curio daemon stop` "+
			"(the next command starts it again), to index transcripts", plural(n, "YouTube video"), ytdlpInstall))
		return nil
	}
	argv := Command(d, InstallFormula, ytdlpFormula)
	ui.Info("  $ " + textutil.ShellJoin(argv))
	install, err := ui.Confirm(ctx, youTubeQuestion(n), true)
	switch {
	case err != nil:
		return err
	case !install:
		w.declineYTDLP(ui, env.Controller.Home.Path, state)
		return nil
	}
	if err := w.deps.Installer.Run(ctx, ui, argv); err != nil {
		return err
	}
	return routeYouTube(ctx, ui, env, ytdlpFormula, n)
}

// detectYTDLP finds what is installed of yt-dlp; known is false, said as a
// warning, when that can't be told: yt-dlp is optional, and the import
// goes on without it.
func (w *world) detectYTDLP(ctx context.Context, ui UI) (d Detection, known bool) {
	d, err := w.deps.Installer.Detect(ctx, ytdlpFormula, "")
	if err != nil {
		ui.Warn("couldn't tell whether yt-dlp is installed, so YouTube videos are indexed without transcripts: " +
			err.Error())
	}
	return d, err == nil
}

// routeYouTube restarts the daemon, which finds yt-dlp only as it starts,
// and checks through its healthz that it now routes the n YouTube videos
// to it; bin is the yt-dlp found, for the warning when it doesn't.
func routeYouTube(ctx context.Context, ui UI, env daemonctl.Env, bin string, n int) error {
	if err := env.Controller.Restart(ctx); err != nil {
		return fmt.Errorf("restart curio-daemon to use yt-dlp: %w", err)
	}
	ui.Info("restarted curio-daemon, which finds yt-dlp as it starts")
	h, err := env.Client.Healthz(ctx)
	if err != nil {
		return err
	}
	if h.YouTubeFetcher == "" {
		ui.Warn(fmt.Sprintf("curio-daemon doesn't find %s: link it into Homebrew's bin (/opt/homebrew/bin), or set "+
			"fetcher.youtube.bin in %s to its path, then `curio daemon stop` (the next command starts it again)",
			bin, env.Controller.Home.ConfigPath()))
		return nil
	}
	ui.Info(transcriptsThrough(n, h.YouTubeFetcher))
	return nil
}

// youTubeQuestion offers yt-dlp for n YouTube videos.
func youTubeQuestion(n int) string {
	if n == 1 {
		return "1 of your bookmarks is a YouTube video. Install yt-dlp to index its transcript?"
	}
	return fmt.Sprintf("%d of your bookmarks are YouTube videos. Install yt-dlp to index their transcripts?", n)
}

// transcriptsThrough says the n YouTube videos' transcripts come through
// the yt-dlp at bin.
func transcriptsThrough(n int, bin string) string {
	if n == 1 {
		return "the YouTube video gets its transcript through " + bin
	}
	return fmt.Sprintf("the %d YouTube videos get their transcripts through %s", n, bin)
}

// declineYTDLP remembers the decline in setup.json, so a later run doesn't
// ask again; a failed save only costs that question, and is a warning.
func (w *world) declineYTDLP(ui UI, home string, state State) {
	state.Declined[ytdlpFormula] = w.deps.Now()
	if err := SaveState(home, state); err != nil {
		ui.Warn("curio up will ask about yt-dlp again: " + err.Error())
	}
	ui.Info("not installing yt-dlp: YouTube videos are indexed without transcripts; " +
		"`brew install yt-dlp`, then `curio daemon stop` (the next command starts it again), adds them later")
}
