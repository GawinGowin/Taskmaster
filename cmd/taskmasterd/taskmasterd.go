package main

import (
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

var Usage = func() {
	w := flag.CommandLine.Output()
	fmt.Fprintf(w, "usage: %s [flags]\n\nflags:\n", os.Args[0])
	flag.PrintDefaults()
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	flag.Usage = Usage
	flag.Parse()

	if *showVersion {
		fmt.Printf("taskmasterd %s (commit %s, built %s)\n", version, commit, buildDate)
		return nil
	}

	if flag.NArg() > 0 {
		fmt.Fprintf(flag.CommandLine.Output(), "unexpected argument: %q\n\n", flag.Arg(0))
		flag.Usage()
		os.Exit(2)
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
		return err
	}
	fmt.Printf("%v\n", m)
	return nil
}
