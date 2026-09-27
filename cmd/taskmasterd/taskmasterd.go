package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"taskmaster/internal/config"
	"taskmaster/internal/controller"
)

var (
	cfgPath     = flag.String("c", "taskmasterd.yaml", "path to config file")
	logPath     = flag.String("l", "taskmasterd.log", "path to event log file")
	showVersion = flag.Bool("V", false, "print version and exit")
)

var (
	version   = "dev"
	commit    = "none"
	buildDate = "unknown"
)

// usageError は使い方の誤り。終了コード 2 と usage 表示を伴う。
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usage() {
	w := flag.CommandLine.Output()
	fmt.Fprintf(w, "usage: %s [flags]\n\nflags:\n", flag.CommandLine.Name())
	flag.PrintDefaults()
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		var ue *usageError
		if errors.As(err, &ue) {
			usage()
			os.Exit(2)
		}
		os.Exit(1)
	}
}

func run() error {
	flag.Usage = usage
	flag.Parse()

	if *showVersion {
		fmt.Printf("taskmasterd %s (commit %s, built %s)\n", version, commit, buildDate)
		return nil
	}

	if flag.NArg() > 0 {
		return &usageError{fmt.Sprintf("unexpected argument: %q", flag.Arg(0))}
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	c, err := controller.New(cfg)
	if err != nil {
		return err
	}
	logfile, err := os.OpenFile(*logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("log file: %w", err)
	}
	defer logfile.Close()
	logger := slog.New(slog.NewJSONHandler(logfile, nil))

	c.Run(logger)

	return nil
}
