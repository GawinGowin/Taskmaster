package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"taskmaster/internal/config"
	"taskmaster/internal/controller"
)

var (
	cfgPath     = flag.String("c", "taskmasterd.yaml", "path to config file")
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
	c.Run()

	return nil
}
