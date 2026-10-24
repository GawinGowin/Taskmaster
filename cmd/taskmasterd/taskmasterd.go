package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"taskmaster/internal/config"
	"taskmaster/internal/controller"
	"taskmaster/internal/process"
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
	if process.IsSpawned(os.Args) {
		exitcode, err := process.RunSpawn(os.Args)
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(exitcode)
	}
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
	logfile, err := os.OpenFile(*logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("log file: %w", err)
	}
	c, err := controller.New(cfg)
	if err != nil {
		return err
	}
	defer logfile.Close()
	logger := slog.New(slog.NewTextHandler(logfile, nil))

	self := os.Getpid()
	logger.LogAttrs(context.Background(), slog.LevelInfo, "startup", []slog.Attr{
		slog.Int("self", self),
		slog.String("version", version),
		slog.String("config", *cfgPath),
	}...)
	c.Run(logger)

	err = c.Close()
	logger.LogAttrs(context.Background(), slog.LevelInfo, "exit", slog.Int("self", self))
	if err != nil {
		return fmt.Errorf("output: %w", err)
	}
	return nil
}
