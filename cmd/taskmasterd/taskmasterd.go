package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"sync"

	"taskmaster/internal/config"
	"taskmaster/internal/process"
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
	fmt.Printf("%v", cfg)

	prog := &cfg.Programs
	pl := make([]string, 0, len(*prog))
	for k := range *prog {
		pl = append(pl, k)
	}
	sort.Strings(pl)
	var wg sync.WaitGroup
	for _, name := range pl {
		p := (*prog)[name]
		var fout, ferr *os.File
		fout, err = process.OpenRedirect(string(p.Stdout))
		if err != nil {
			return err
		}
		if p.Stderr == p.Stdout {
			ferr = fout
		} else {
			ferr, err = process.OpenRedirect(string(p.Stdout))
			if err != nil {
				return err
			}
		}
		if p.Autostart {
			for i := 0; i < p.Numprocs; i++ {
				proc, err := process.New(&p, name, i, fout, ferr)
				if err != nil {
					return err
				}
				if proc.Start() != nil {
					break
				}
				wg.Go(func() {
					proc.Wait()
				})
			}
		}
	}
	wg.Wait()
	return nil
}
