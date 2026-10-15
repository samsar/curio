// Command plannercheck measures a writing model as search understanding's
// planner on this machine, without a library: whether it passes the
// fitness check the daemon runs before its first plan, how long its plans
// of a few made-up questions take against search.understand_timeout_seconds,
// and whether Ollama keeps it loaded beside the embedding model once a
// query has been embedded, as a planned search does. It is what to run
// for a model curio up offers (decisions.md "Understanding the question",
// the tiers left to measure) before it understands searches by default:
//
//	plannercheck -model gemma4:12b [-config ~/.curio/config.yaml]
//
// It is a developer's tool, run by `make planner-check` and never shipped.
// It talks to the Ollama config.yaml names (the planner role's server),
// never pulls a model, and unloads the planner's model afterwards when it
// was the one that loaded it. Its questions are made up, and nothing it
// reports quotes a question's plan. It exits 0 when the model passed the
// check and planned every question within the timeout, 1 when it didn't
// or a step failed, 2 on a usage error.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/embedder"
	"github.com/samsar/curio/internal/generator"
	"github.com/samsar/curio/internal/ollama"
	"github.com/samsar/curio/internal/understand"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// Exit codes.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

// questions are what the planner is asked after its check: made up, of
// every kind, about none of anyone's library.
var questions = []string{
	"kubernetes operator tutorials",
	"that blog post comparing b-trees with lsm trees",
	"talks on async rust I saved last year",
	"pdfs about vector databases from 2023",
	"the paper that introduced the raft consensus algorithm",
	"github repos for terminal file managers",
	"articles on sourdough starters I read in March",
	"threads arguing about tailwind and css modules",
}

// pingTimeout bounds asking Ollama for a model or its version.
const pingTimeout = 10 * time.Second

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var usage bytes.Buffer
	fs := flag.NewFlagSet("plannercheck", flag.ContinueOnError)
	fs.SetOutput(&usage)
	model := fs.String("model", "", "the writing model to measure as the planner (default: the planner role's)")
	configPath := fs.String("config", "", "the config.yaml whose Ollama, embedding model and timeouts to use; "+
		"the defaults without it")
	fs.Usage = func() {
		usage.WriteString("usage: plannercheck [-model <name>] [-config <config.yaml>]\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		_, _ = io.WriteString(stderr, usage.String())
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if fs.NArg() > 0 {
		fs.Usage()
		_, _ = fmt.Fprintf(stderr, "plannercheck: unexpected arguments %q\n%s", fs.Args(), usage.String())
		return exitUsage
	}
	cfg := config.Default()
	if *configPath != "" {
		var err error
		if cfg, err = config.Load(*configPath); err != nil {
			_, _ = fmt.Fprintf(stderr, "plannercheck: %v\n", err)
			return exitFailure
		}
	}
	if *model != "" {
		roles := map[config.Role]config.ModelSpec{}
		maps.Copy(roles, cfg.Generation.Roles)
		own := roles[config.RolePlanner]
		own.Model = *model
		roles[config.RolePlanner] = own
		cfg.Generation.Roles = roles
	}
	log := slog.New(slog.NewTextHandler(stderr, nil))
	var report bytes.Buffer
	fit, err := check(ctx, cfg, &report, log)
	_, _ = stdout.Write(report.Bytes())
	switch {
	case err != nil:
		_, _ = fmt.Fprintf(stderr, "plannercheck: %v\n", err)
		return exitFailure
	case !fit:
		return exitFailure
	}
	return exitOK
}

// check measures the planner role's model under cfg, writing the report to
// w, which run prints whatever happened; fit is whether it passed the
// check and planned every question in time.
func check(ctx context.Context, cfg config.Config, w *bytes.Buffer, log *slog.Logger) (fit bool, err error) {
	opts := understand.OptionsFrom(cfg)
	opts.On, opts.Log = true, log
	opts.Withheld = "" // a developer measuring a model sends made-up questions wherever it runs
	spec := opts.Model
	gen, err := generator.New(spec)
	if err != nil {
		return false, fmt.Errorf("build the planner: %w", err)
	}
	warmer, warms := gen.(generator.Warmer)
	if _, structured := gen.(generator.JSONGenerator); !structured || !warms {
		return false, fmt.Errorf("the planner's provider %s can't answer in JSON or load its model", spec.Provider)
	}
	client, err := ollama.New(spec.BaseURL, spec.Model, pingTimeout)
	if err != nil {
		return false, err
	}
	version, err := ping(ctx, client, spec)
	if err != nil {
		return false, err
	}
	fmt.Fprintf(w, "planner-check: %s at %s (Ollama %s)\n", spec.Model, spec.BaseURL, version)

	lifetime, stop := context.WithCancel(ctx)
	planner := understand.New(lifetime, gen, opts)
	defer func() {
		stop()
		planner.Wait()
	}()
	wasLoaded, err := warmer.Loaded(ctx)
	if err != nil {
		return false, fmt.Errorf("ask whether %s is loaded: %w", spec.Model, err)
	}
	if !wasLoaded {
		// Whatever happens next, the memory this tool took is given back.
		defer unload(warmer, spec.Model, w)
	}
	if err := load(ctx, warmer, wasLoaded, planner.KeepAlive(), w); err != nil {
		return false, err
	}

	start := time.Now()
	failed, err := planner.Check(ctx)
	switch {
	case err != nil:
		return false, fmt.Errorf("the check couldn't reach %s: %w", spec.Model, err)
	case failed != "":
		fmt.Fprintf(w, "check:   failed %q (%s): searches would use their words\n", failed, seconds(time.Since(start)))
	default:
		fmt.Fprintf(w, "check:   passed (%s)\n", seconds(time.Since(start)))
	}

	inTime, err := plan(ctx, planner, w)
	if err != nil {
		return false, err
	}
	if err := residency(ctx, cfg, client, spec.Model, w); err != nil {
		return false, err
	}
	return failed == "" && inTime, nil
}

// ping checks that Ollama has the model, never pulling it, and returns
// Ollama's version.
func ping(ctx context.Context, c *ollama.Client, spec config.ModelSpec) (string, error) {
	pctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	switch err := c.Ping(pctx); {
	case errors.Is(err, ollama.ErrModelNotLoaded):
		return "", fmt.Errorf("the Ollama at %s doesn't have %s, and plannercheck never pulls a model: "+
			"`ollama pull %s` first", spec.BaseURL, spec.Model, spec.Model)
	case err != nil:
		return "", fmt.Errorf("ask the Ollama at %s for %s: %w", spec.BaseURL, spec.Model, err)
	}
	version, err := c.Version(pctx)
	if err != nil {
		return "", fmt.Errorf("ask the Ollama at %s for its version: %w", spec.BaseURL, err)
	}
	return version, nil
}

// load loads the model with the planner's context size and keep-alive,
// timing a cold load.
func load(ctx context.Context, warmer generator.Warmer, wasLoaded bool, keepAlive time.Duration, w *bytes.Buffer) error {
	start := time.Now()
	if err := warmer.Warm(ctx, keepAlive); err != nil {
		return fmt.Errorf("load the planner's model: %w", err)
	}
	if wasLoaded {
		fmt.Fprintf(w, "load:    loaded already (%s)\n", seconds(time.Since(start)))
		return nil
	}
	fmt.Fprintf(w, "load:    cold, %s\n", seconds(time.Since(start)))
	return nil
}

// plan asks the planner for each question's plan, whatever the check
// found, and reports how long they took against its timeout, and their
// statuses; inTime is whether every one was planned within it.
func plan(ctx context.Context, planner *understand.Planner, w *bytes.Buffer) (inTime bool, err error) {
	var took []time.Duration
	statuses := map[understand.Status]int{}
	for _, q := range questions {
		out, err := planner.Ask(ctx, q)
		if err != nil {
			return false, err
		}
		statuses[out.Status]++
		took = append(took, out.Took)
	}
	slices.Sort(took)
	planned := statuses[understand.StatusPlanned]
	fmt.Fprintf(w, "plans:   %d of %d planned; p50 %s, p95 %s, slowest %s; the timeout is %s\n", planned,
		len(questions), seconds(percentile(took, 0.5)), seconds(percentile(took, 0.95)), seconds(took[len(took)-1]),
		planner.Timeout())
	for _, s := range understand.Statuses() {
		if n := statuses[s]; n > 0 && s != understand.StatusPlanned {
			fmt.Fprintf(w, "         %d %s\n", n, s)
		}
	}
	return planned == len(questions), nil
}

// residency embeds one query as a planned search does, then reports what
// Ollama holds in memory: whether the planner's model stayed loaded beside
// the embedding model.
func residency(ctx context.Context, cfg config.Config, planner *ollama.Client, model string, w *bytes.Buffer) error {
	emb, err := embedder.NewOllama(embedder.OllamaOptions{BaseURL: cfg.Embedding.BaseURL, Model: cfg.Embedding.Model,
		Dim: cfg.Embedding.Dim, Timeout: time.Duration(cfg.Search.EmbedTimeoutSeconds) * time.Second})
	if err != nil {
		return err
	}
	if _, err := emb.Embed(ctx, []string{cfg.Embedding.QueryPrefix + questions[0]}); err != nil {
		return fmt.Errorf("embed a query with %s: %w", cfg.Embedding.Model, err)
	}
	running, err := planner.Running(ctx)
	if err != nil {
		return fmt.Errorf("list what Ollama holds: %w", err)
	}
	fmt.Fprintln(w, "memory:  after a query embed, Ollama holds")
	var plannerIn, embedderIn bool
	for _, r := range running {
		fmt.Fprintf(w, "         %s: %s, %s in VRAM, context %d\n", r.Name, gigabytes(r.Size), gigabytes(r.SizeVRAM),
			r.ContextLength)
		plannerIn = plannerIn || r.Is(model)
		embedderIn = embedderIn || r.Is(cfg.Embedding.Model)
	}
	switch {
	case plannerIn && embedderIn:
		fmt.Fprintln(w, "         both models loaded")
	case !plannerIn:
		fmt.Fprintf(w, "         %s was unloaded for the embedder: searches would reload it\n", model)
	default:
		fmt.Fprintf(w, "         %s isn't loaded on the planner's server\n", cfg.Embedding.Model)
	}
	return nil
}

// unload frees the model this tool loaded, on a context of its own: the
// run's may be the interrupt that ended it.
func unload(warmer generator.Warmer, model string, w *bytes.Buffer) {
	ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
	defer cancel()
	if err := warmer.Unload(ctx); err != nil {
		fmt.Fprintf(w, "unload:  %s stays loaded: %v\n", model, err)
		return
	}
	fmt.Fprintf(w, "unload:  %s, which this check loaded\n", model)
}

// percentile is the p-th of sorted, by nearest rank.
func percentile(sorted []time.Duration, p float64) time.Duration {
	i := max(0, min(len(sorted)-1, int(float64(len(sorted))*p+0.5)-1))
	return sorted[i]
}

func seconds(d time.Duration) string { return fmt.Sprintf("%.2f s", d.Seconds()) }

func gigabytes(n int64) string { return fmt.Sprintf("%.1f GB", float64(n)/1e9) }
