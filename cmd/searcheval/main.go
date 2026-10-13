// Command searcheval measures the search curio ships on a copy of a home's
// database against a graded query file (internal/eval's format, see
// docs/search-eval.example.yaml): every query, in each variant of the
// pipeline asked for, ranked 100 deep by the production engine
// (search.ConfigFrom over config.yaml), then scored per query, per kind and
// per tag, with each stage's latency. -baseline compares the run with an
// earlier report, query by query. It prints a text report, which names
// queries by id alone; -json writes every number, with the queries' text
// and the judged URLs, so keep it, like the query file, out of the
// repository.
//
// It is a developer's tool, run by `make search-eval` and never shipped.
// It migrates the database it is given, so it refuses a directory holding
// daemon.pid or .curio-meta.json, and a file with another hard link: take
// a copy first,
//
//	sqlite3 -readonly ~/.curio/curio.db ".backup copy.db"
//	searcheval -db copy.db -queries my-queries.yaml [-config ~/.curio/config.yaml]
//
// Before any query runs it brings the copy up to date as the daemon would
// at start (the document fields derived, the card vector index sized),
// and embeds every stale card as the daemon's cards job does: the first
// run on a copy embeds them all, a few minutes for a large library, and a
// later one on the same copy none.
//
// It embeds each query, and the cards, through the Ollama config.yaml
// names, as the daemon does, and never pulls a model. An Ollama too busy
// to embed a query within search.embed_timeout_seconds degrades that
// search, which is ranked again once; a second degraded search fails the
// run: run it again with Ollama idle.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// options are the command's flags.
type options struct {
	db       string
	queries  string
	config   string
	json     string
	baseline string
	variants []variant
}

// Exit codes.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

// run is the command: it parses args, evaluates, writes the text report to
// stdout and progress to stderr, and returns the exit code.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var usage bytes.Buffer
	opts, err := parseFlags(args, &usage)
	switch {
	case errors.Is(err, flag.ErrHelp):
		complain(stderr, usage.String())
		return exitOK
	case err != nil:
		complain(stderr, usage.String())
		return exitUsage
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{ReplaceAttr: dropQueryText}))
	if err := evaluate(ctx, opts, stdout, log); err != nil {
		complain(stderr, fmt.Sprintf("searcheval: %v\n", err))
		return exitFailure
	}
	return exitOK
}

// dropQueryText leaves a query's text out of the log. The engine names it
// in a degraded search's warning; the tool names queries by id alone, as
// its report does, so a private test set stays off the terminal and out of
// any log a run is kept in.
func dropQueryText(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && a.Key == "query" {
		return slog.Attr{}
	}
	return a
}

// complain writes msg to stderr. A failed write there has no one left to
// report to.
func complain(stderr io.Writer, msg string) { _, _ = io.WriteString(stderr, msg) }

const usageLine = "usage: searcheval -db <copy of curio.db> -queries <file> [-config <config.yaml>] [-json <file>] " +
	"[-baseline <previous -json>] [-variants hybrid,bm25,vector,fields,cards]\n"

// parseFlags parses args, writing what is wrong and the usage to out on a
// usage error, and the usage alone for -h.
func parseFlags(args []string, out *bytes.Buffer) (options, error) {
	var (
		opts     options
		variants string
	)
	fs := flag.NewFlagSet("searcheval", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&opts.db, "db", "", "a copy of a home's curio.db, never the home's own (required)")
	fs.StringVar(&opts.queries, "queries", "", "the graded query file (required)")
	fs.StringVar(&opts.config, "config", "", "the config.yaml to search with; the defaults without it")
	fs.StringVar(&opts.json, "json", "", "also write every number to this file, as JSON")
	fs.StringVar(&opts.baseline, "baseline", "", "a previous run's -json report to compare with")
	fs.StringVar(&variants, "variants", "hybrid", "the variants to run, in order: "+strings.Join(variantNames(), ", "))
	fs.Usage = func() {
		out.WriteString(usageLine)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	var problem string
	switch {
	case opts.db == "":
		problem = "-db is required"
	case opts.queries == "":
		problem = "-queries is required"
	case fs.NArg() > 0:
		problem = fmt.Sprintf("unexpected argument %q", fs.Arg(0))
	default:
		opts.variants, problem = parseVariants(variants)
	}
	if problem == "" {
		return opts, nil
	}
	fmt.Fprintf(out, "searcheval: %s\n", problem)
	fs.Usage()
	return options{}, errors.New(problem)
}

// parseVariants reads -variants, a comma list of registered names, each
// once; problem says what is wrong with it.
func parseVariants(list string) (chosen []variant, problem string) {
	for name := range strings.SplitSeq(list, ",") {
		v, ok := variantNamed(name)
		switch {
		case !ok:
			return nil, fmt.Sprintf("unknown variant %q in -variants (the variants are: %s)", name,
				strings.Join(variantNames(), ", "))
		case slices.ContainsFunc(chosen, func(c variant) bool { return c.name == name }):
			return nil, fmt.Sprintf("variant %q named twice in -variants", name)
		}
		chosen = append(chosen, v)
	}
	return chosen, ""
}
