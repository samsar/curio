// Command clusterreport measures the interests grouping curio ships on a
// copy of a home's database: the quality of a fresh grouping, how many
// names a warm rebuild keeps after a 5% change and a fresh rebuild after
// 5% added, an 8-step chain of warm rebuilds from 60% to 100% of the
// library, the clusterer the grouping replaced as a baseline, and the
// copy's latest stored run with its labels. It runs the production pieces
// in the engine's order (see regroup) and prints a text report; -json
// writes every number as well.
//
// It is a developer's tool, run by `make cluster-report` and never
// shipped. It migrates the database it is given, so it refuses a
// directory holding daemon.pid: take a copy first,
//
//	sqlite3 -readonly ~/.curio/curio.db ".backup copy.db"
//	clusterreport -db copy.db [-json report.json] [-draws 3] [-seed 0]
//
// A read-only open needs the database's -shm file, which a daemon that
// stopped cleanly removed with its WAL; the main file then holds
// everything, and an immutable open copies it:
//
//	sqlite3 "file:$HOME/.curio/curio.db?immutable=1" ".backup copy.db"
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
	"path/filepath"
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
	db    string
	json  string
	draws uint
	seed  uint64
}

// Exit codes.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

// run is the command: it parses args, measures, writes the text report to
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
	log := slog.New(slog.NewTextHandler(stderr, nil))
	if err := measureAndReport(ctx, opts, stdout, log); err != nil {
		complain(stderr, fmt.Sprintf("clusterreport: %v\n", err))
		return exitFailure
	}
	return exitOK
}

// complain writes msg to stderr. A failed write there has no one left to
// report to.
func complain(stderr io.Writer, msg string) { _, _ = io.WriteString(stderr, msg) }

// parseFlags parses args, writing what is wrong and the usage to out on a
// usage error, and the usage alone for -h.
func parseFlags(args []string, out *bytes.Buffer) (options, error) {
	var opts options
	fs := flag.NewFlagSet("clusterreport", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&opts.db, "db", "", "a copy of a home's curio.db, never the home's own (required)")
	fs.StringVar(&opts.json, "json", "", "also write every number to this file, as JSON")
	fs.UintVar(&opts.draws, "draws", 3, "random draws per change and of the chain, at least 1")
	fs.Uint64Var(&opts.seed, "seed", 0, "added to every draw's seed; 0 makes the draws docs/decisions.md's measurements used")
	fs.Usage = func() {
		out.WriteString("usage: clusterreport -db <copy of curio.db> [-json <file>] [-draws n] [-seed n]\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	var problem string
	switch {
	case opts.db == "":
		problem = "-db is required"
	case opts.draws < 1:
		problem = "-draws must be at least 1"
	case fs.NArg() > 0:
		problem = fmt.Sprintf("unexpected argument %q", fs.Arg(0))
	default:
		return opts, nil
	}
	fmt.Fprintf(out, "clusterreport: %s\n", problem)
	fs.Usage()
	return options{}, errors.New(problem)
}

// measureAndReport reads the library, measures it and writes the report:
// the text to stdout, then the JSON file, which is written only once every
// measurement succeeded.
func measureAndReport(ctx context.Context, opts options, stdout io.Writer, log *slog.Logger) error {
	if opts.json != "" {
		if err := checkOutput(opts.json); err != nil {
			return err
		}
	}
	lib, err := openLibrary(ctx, opts.db, log)
	if err != nil {
		return err
	}
	rep, err := newMeasurer(ctx, log, lib.docs, opts.draws, opts.seed).measure(lib)
	if err != nil {
		return err
	}
	rep.Database = opts.db
	if err := rep.writeText(stdout); err != nil {
		return fmt.Errorf("write the report: %w", err)
	}
	if opts.json == "" {
		return nil
	}
	if err := writeJSON(opts.json, rep); err != nil {
		return err
	}
	log.Info("wrote the JSON report", "path", opts.json)
	return nil
}

// checkOutput fails, before minutes of measuring, unless the JSON file
// can take its place: its directory exists, and the path isn't a directory
// itself, which the final rename can't replace.
func checkOutput(path string) error {
	switch info, err := os.Stat(path); {
	case err == nil && info.IsDir():
		return fmt.Errorf("the -json file %s is a directory", path)
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("the -json file: %w", err)
	}
	dir := filepath.Dir(path)
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("the -json file's directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("the -json file's directory %s is not a directory", dir)
	}
	// A directory the report can't write to would fail only at the end,
	// after the whole measurement: try a temporary file there now.
	probe, err := os.CreateTemp(dir, ".clusterreport-probe-*")
	if err != nil {
		return fmt.Errorf("the -json file's directory: %w", err)
	}
	name := probe.Name()
	if err := probe.Close(); err != nil {
		return fmt.Errorf("the -json file's directory: %w", err)
	}
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("the -json file's directory: %w", err)
	}
	return nil
}
