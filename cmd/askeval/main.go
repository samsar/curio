// Command askeval is the developer's evaluation of curio ask: it asks a
// private question file's questions of a .backup copy of a home's
// database, through the daemon's own ask path (internal/ask over
// internal/retrieve), records every writer call and every ask in an answer
// book so later runs replay them asking no model, has a model other than
// the writer judge each answer against the sources it was written from,
// and reports the metrics docs/decisions.md "Choosing Ask's prompt and K"
// defines. It is never built into ./bin or shipped.
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
	db, questions, config, json string
	// answers replays an answer book, answersOut records one;
	// judgments and judgmentsOut likewise for the judge's verdicts, and
	// judgments2 and judgments2Out for the second judge's.
	answers, answersOut       string
	judgeModel, judge2Model   string
	judgments, judgmentsOut   string
	judgments2, judgments2Out string
	// recorded is a report whose answer-route questions the recorded
	// variant takes as it recorded them.
	recorded string
	variants []string
	today    time.Time
}

// The variants: the daemon's ask (understood, and reranked as ask.rerank
// says), the question's words alone, understood without a rerank, and an
// earlier report's answers to the questions the answer route takes, the
// rest asked as ask asks them.
var variantNames = []string{"ask", "literal", "norerank", variantRecorded}

// Exit codes.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var usage bytes.Buffer
	opts, err := parseFlags(args, &usage)
	switch {
	case errors.Is(err, flag.ErrHelp):
		_, _ = io.WriteString(stderr, usage.String())
		return exitOK
	case err != nil:
		_, _ = io.WriteString(stderr, usage.String())
		return exitUsage
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{ReplaceAttr: dropPrivate}))
	if err := evaluate(ctx, opts, stdout, log); err != nil {
		_, _ = fmt.Fprintf(stderr, "askeval: %v\n", err)
		return exitFailure
	}
	return exitOK
}

// dropPrivate leaves out of the log any record key that could carry a
// question's text, a page's or a model's reply: the tool names questions
// by id alone.
func dropPrivate(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && slices.Contains([]string{"question", "prompt", "reply", "answer", "sources"}, a.Key) {
		return slog.Attr{}
	}
	return a
}

const usageLine = "usage: askeval -db <copy of curio.db> -questions <file> [-config <config.yaml>] [-json <file>] " +
	"[-answers <book>] [-answers-out <book>] [-judge <model>|none] [-judgments <book>] [-judgments-out <book>] " +
	"[-judge2 <model>] [-judgments2 <book>] [-judgments2-out <book>] [-recorded <report.json>] " +
	"[-variants ask,literal,norerank,recorded] [-today YYYY-MM-DD]\n"

func parseFlags(args []string, out *bytes.Buffer) (options, error) {
	var opts options
	var variants, today string
	fs := flag.NewFlagSet("askeval", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&opts.db, "db", "", "a copy of a home's curio.db, never the home's own (required)")
	fs.StringVar(&opts.questions, "questions", "", "the question file (required)")
	fs.StringVar(&opts.config, "config", "", "the config.yaml to ask with; the defaults without it")
	fs.StringVar(&opts.json, "json", "", "also write every number, and each answer's sentences and verdicts, to "+
		"this file (it holds the questions: keep it out of the repository)")
	fs.StringVar(&opts.answers, "answers", "", "replay an answer book: each question's plan and order are sent "+
		"as supplied, and each writer call answered from the book, asking no model")
	fs.StringVar(&opts.answersOut, "answers-out", "", "append each ask and writer call the book hasn't to this "+
		"file, asking the model for them (0600; keep it out of the repository)")
	fs.StringVar(&opts.judgeModel, "judge", "gemma4:12b", "the judge's model, another than the writer's; none "+
		"judges nothing")
	fs.StringVar(&opts.judgments, "judgments", "", "replay a judgments book, asking no judge")
	fs.StringVar(&opts.judgmentsOut, "judgments-out", "", "append each verdict the book hasn't to this file, "+
		"asking the judge")
	fs.StringVar(&opts.judge2Model, "judge2", "", "a second judge, of another family than the first, asked the "+
		"same prompt: the report says how its verdicts agree with the first's (raw and Cohen's kappa); none "+
		"by default")
	fs.StringVar(&opts.judgments2, "judgments2", "", "replay the second judge's judgments book")
	fs.StringVar(&opts.judgments2Out, "judgments2-out", "", "append each of the second judge's verdicts its "+
		"book hasn't to this file, asking it")
	fs.StringVar(&opts.recorded, "recorded", "", "an earlier JSON report of the same question file and judges: "+
		"the recorded variant takes from it the answers to the questions the answer route takes, asking nothing, "+
		"and asks the rest live")
	fs.StringVar(&variants, "variants", "ask", "the variants to run, in order: "+strings.Join(variantNames, ", "))
	fs.StringVar(&today, "today", "", "the day the planner and the writer are told is today, YYYY-MM-DD "+
		"(default: the local date)")
	fs.Usage = func() {
		out.WriteString(usageLine)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	problem := ""
	switch {
	case opts.db == "":
		problem = "-db is required"
	case opts.questions == "":
		problem = "-questions is required"
	case fs.NArg() > 0:
		problem = fmt.Sprintf("unexpected argument %q", fs.Arg(0))
	case opts.judge2Model != "" && (opts.judgeModel == "none" || opts.judge2Model == opts.judgeModel):
		problem = "-judge2 is compared with -judge: a judge, and another model"
	case strings.Contains(","+variants+",", ","+variantRecorded+",") != (opts.recorded != ""):
		problem = "the recorded variant takes its answers from -recorded, and -recorded is for it alone"
	}
	for v := range strings.SplitSeq(variants, ",") {
		switch {
		case problem != "":
		case !slices.Contains(variantNames, v):
			problem = fmt.Sprintf("unknown variant %q (the variants are: %s)", v, strings.Join(variantNames, ", "))
		case slices.Contains(opts.variants, v):
			problem = fmt.Sprintf("variant %q named twice", v)
		default:
			opts.variants = append(opts.variants, v)
		}
	}
	if problem == "" {
		day := time.Now()
		if today != "" {
			var err error
			if day, err = time.ParseInLocation(time.DateOnly, today, time.Local); err != nil {
				problem = "-today: " + err.Error()
			}
		}
		opts.today = time.Date(day.Year(), day.Month(), day.Day(), 12, 0, 0, 0, time.Local)
	}
	if problem != "" {
		fmt.Fprintf(out, "askeval: %s\n", problem)
		fs.Usage()
		return options{}, errors.New(problem)
	}
	return opts, nil
}
