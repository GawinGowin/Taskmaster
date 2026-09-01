package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"taskmaster/internal/config"

	yaml "gopkg.in/yaml.v3"
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

type usageError struct{ err error }

func (e *usageError) Error() string { return e.err.Error() }
func (e *usageError) Unwrap() error { return e.err }

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
		return &usageError{fmt.Errorf("unexpected argument: %q", flag.Arg(0))}
	}

	f, err := os.Open(*cfgPath)
	if err != nil {
		return err
	}
	defer f.Close()
	var m config.Config

	d := yaml.NewDecoder(f)
	d.KnownFields(true)
	if err := d.Decode(&m); err != nil {
		return fmt.Errorf("%s: %w", *cfgPath, err)
	}

	ret, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	fmt.Printf("%s\n", string(ret))
	return nil
}
