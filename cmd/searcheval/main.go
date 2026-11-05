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
// A copy of a home indexed before chunk context holds chunks embedded from
// their text alone, which the report counts (documents_to_reembed) and
// warns of. -reindex re-embeds them first, through the daemon's own index
// path (jobs.IndexDocument), from the markdown under -content, the content
// directory of the home the copy is from, which it checks for every
// document before it embeds anything: an hour or more for a large library,
// resumable (each document is committed whole), and nothing on a copy
// re-embedded already.
//
//	searcheval -db copy.db -queries my-queries.yaml -reindex -content ~/.curio/content
//
// It embeds each query, and the cards, through the Ollama config.yaml
// names, as the daemon does, and never pulls a model. An Ollama too busy
// to embed a query within search.embed_timeout_seconds degrades that
// search, which is ranked again once; a second degraded search fails the
// run: run it again with Ollama idle.
//
// The planned variants measure search understanding: before the first,
// the tool loads config.yaml's planner model (whatever search.understand
// says), runs its fitness check, and plans every query once, as of
// -today, each plan searched in place of the query's filters, which then
// serve as the oracle the plan's are compared with. The check's verdict is
// reported, and the variants run whatever it is: the tool measures the
// planner. -plans replays a previous report's plans instead, asking no
// model, so a later change is measured without the planner's variance.
//
// -summarize writes the copy's summaries first, as the daemon's summary
// pass would, with config.yaml's summaries role (whatever
// summaries.enabled says) from the markdown under -content: one page at a
// time, each stored as it is written, so a stopped run resumes where it
// stopped; -documents holds it to a list of document IDs (a sample), and
// -summaries-out appends each summary and what it cost to a file, which
// -summaries replays into another copy, asking no model. The writer's
// fitness check is reported, never a gate. No card reads a summary: the
// summary list (search.summary_weight, 0 by default) alone searches them.
//
//	searcheval -db copy.db -queries my-queries.yaml -summarize -content ~/.curio/content \
//		-documents sample.txt -summaries-out summaries.jsonl
//	searcheval -db another-copy.db -queries my-queries.yaml -summaries summaries.jsonl
//
// The reranked variants (hybrid-reranked, planned-reranked) rerank their
// base ranking's head as the daemon does a search that asks: the first
// search.rerank_depth hits read as candidates, config.yaml's reranker
// asked (whatever search.rerank says), routed by each plan's kind, the
// rest in first-stage order. -rerank-with changes the reranker for a grid
// point (method forces one method for every query, keywords included;
// model, depth, blend, input, textless, folders, claims, timeout).
// -reranks-out records every answer the model gives, a JSON line each,
// and -reranks replays them, asking no model; with both, only what the
// file lacks is asked. The answers hold no page text, but keep them out
// of the repository. A recorded rerank longer than the timeout is scored
// as the daemon would have answered it, a timeout keeping the first
// ranking, at the timeout's cost; a replayed plan's or rerank's recorded
// time counts in its stage and the total, which is Enter to results. The
// fitness check, run before the variants whenever a model may be asked, is
// reported with its calls' time apart, never in a query's.
//
//	searcheval -db copy.db -queries my-queries.yaml -variants planned,planned-reranked -plans base.json \
//		-summaries summaries.jsonl -rerank-with method=listwise,depth=30 -reranks-out answers.jsonl
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
	"time"
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
	// today is the day the planner is told is today, as its noon local
	// time, and plans the report whose plans the planned variants replay.
	today time.Time
	plans string
	// reindex re-embeds the copy's chunks that predate the chunk context
	// before any query, from the markdown under content.
	reindex bool
	content string
	// summarize writes the copy's summaries with config.yaml's summaries
	// role before any query, those in documents alone when it is set,
	// appending each to summariesOut when it is set; summaries replays
	// such a file instead, asking no model.
	summarize    bool
	documents    string
	summariesOut string
	summaries    string
	// rerankWith changes the reranked variants' reranker from the
	// daemon's; reranks replays a -reranks-out file's answers, and
	// reranksOut records the answers the model gives.
	rerankWith rerankWith
	reranks    string
	reranksOut string
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

// dropQueryText leaves a query's text, and its plan's rewrites and names,
// out of the log, should any record carry them: the tool names queries by
// id alone, as its report does, so a private test set stays off the
// terminal and out of any log a run is kept in.
func dropQueryText(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && slices.Contains(privateKeys, a.Key) {
		return slog.Attr{}
	}
	return a
}

// privateKeys are the record keys that could carry a query's text, a
// plan's, a page's or a model's reply.
var privateKeys = []string{"query", "rewrites", "names", "question", "candidates", "prompt", "reply"}

// complain writes msg to stderr. A failed write there has no one left to
// report to.
func complain(stderr io.Writer, msg string) { _, _ = io.WriteString(stderr, msg) }

const usageLine = "usage: searcheval -db <copy of curio.db> -queries <file> [-config <config.yaml>] [-json <file>] " +
	"[-baseline <previous -json>] " +
	"[-variants hybrid,bm25,vector,fields,cards,titles,summaries,planned,planned-filters,planned-rewrites," +
	"planned-names,hybrid-reranked,planned-reranked] " +
	"[-today YYYY-MM-DD] [-plans <previous -json>] [-reindex] [-content <dir>] " +
	"[-summarize [-documents <file>] [-summaries-out <file>] | -summaries <file>] " +
	"[-rerank-with key=value,...] [-reranks <file>] [-reranks-out <file>]\n"

// parseFlags parses args, writing what is wrong and the usage to out on a
// usage error, and the usage alone for -h.
func parseFlags(args []string, out *bytes.Buffer) (options, error) {
	var (
		opts       options
		variants   string
		today      string
		rerankWith string
	)
	fs := flag.NewFlagSet("searcheval", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&opts.db, "db", "", "a copy of a home's curio.db, never the home's own (required)")
	fs.StringVar(&opts.queries, "queries", "", "the graded query file (required)")
	fs.StringVar(&opts.config, "config", "", "the config.yaml to search with; the defaults without it")
	fs.StringVar(&opts.json, "json", "", "also write every number to this file, as JSON")
	fs.StringVar(&opts.baseline, "baseline", "", "a previous run's -json report to compare with")
	fs.StringVar(&variants, "variants", "hybrid", "the variants to run, in order: "+strings.Join(variantNames(), ", "))
	fs.StringVar(&today, "today", "", "the day the planner is told is today, YYYY-MM-DD (default: the local date)")
	fs.StringVar(&opts.plans, "plans", "", "a previous run's -json report whose plans the planned variants replay, "+
		"asking no model")
	fs.BoolVar(&opts.reindex, "reindex", false, "re-embed the copy's chunks that predate the chunk context first, "+
		"from the markdown under -content")
	fs.StringVar(&opts.content, "content", "", "the content directory of the home the copy is from (with -reindex "+
		"or -summarize)")
	fs.BoolVar(&opts.summarize, "summarize", false, "write the copy's summaries first with config.yaml's summaries "+
		"role, one page at a time, from the markdown under -content; a rerun writes only what is left")
	fs.StringVar(&opts.documents, "documents", "", "with -summarize, write only the summaries of the documents "+
		"this file lists, an ID a line")
	fs.StringVar(&opts.summariesOut, "summaries-out", "", "with -summarize, append each summary, with what it cost, "+
		"to this file, a JSON line each (it holds what pages say: keep it out of the repository)")
	fs.StringVar(&opts.summaries, "summaries", "", "replay a -summaries-out file's summaries first, asking no model")
	fs.StringVar(&rerankWith, "rerank-with", "", "with a reranked variant, change its reranker from the daemon's: "+
		"method, model, depth, blend, input, textless, folders, claims, timeout, as key=value,...")
	fs.StringVar(&opts.reranks, "reranks", "", "with a reranked variant, replay a -reranks-out file's answers; a "+
		"call it has no answer to fails the run, unless -reranks-out records the model's")
	fs.StringVar(&opts.reranksOut, "reranks-out", "", "with a reranked variant, ask the model for each answer "+
		"-reranks doesn't hold and append it to this file, a JSON line each (keep it out of the repository)")
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
	case opts.reindex && opts.content == "":
		problem = "-reindex needs -content, the content directory of the home the copy is from"
	case opts.summarize && opts.content == "":
		problem = "-summarize needs -content, the content directory of the home the copy is from"
	case opts.content != "" && !opts.reindex && !opts.summarize:
		problem = "-content is read only with -reindex or -summarize"
	case opts.summarize && opts.summaries != "":
		problem = "-summarize writes the summaries and -summaries replays them: pass one"
	case !opts.summarize && (opts.documents != "" || opts.summariesOut != ""):
		problem = "-documents and -summaries-out are read only with -summarize"
	default:
		opts.variants, problem = parseVariants(variants)
	}
	if problem == "" {
		var err error
		switch opts.rerankWith, err = parseRerankWith(rerankWith); {
		case err != nil:
			problem = err.Error()
		case !anyReranked(opts.variants) && (rerankWith != "" || opts.reranks != "" || opts.reranksOut != ""):
			problem = "-rerank-with, -reranks and -reranks-out are read only with a reranked variant"
		}
	}
	if problem == "" {
		var err error
		if opts.today, err = parseToday(today, time.Now()); err != nil {
			problem = err.Error()
		}
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
