package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
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
	pl := make([]string, 0, len(cfg.Programs))
	for k := range cfg.Programs {
		pl = append(pl, k)
	}
	sort.Strings(pl)
	var wg sync.WaitGroup
	for _, name := range pl {
		p := cfg.Programs[name]
		if !p.Autostart {
			continue
		}
		var fout, ferr *os.File
		fout, err = process.OpenRedirect(string(p.Stdout))
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			continue
		}
		if p.Stderr == p.Stdout {
			ferr = fout
		} else {
			ferr, err = process.OpenRedirect(string(p.Stderr))
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				continue
			}
		}
		for i := 0; i < p.Numprocs; i++ {
			proc, err := process.New(&p, name, i, fout, ferr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				break
			}
			err = proc.Start()
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %s: %v\n", proc.ID(), err)
				break
			}
			wg.Go(func() {
				ps, err := proc.Wait()
				var exitErr *exec.ExitError
				if err != nil && !errors.As(err, &exitErr) {
					fmt.Fprintf(os.Stderr, "%s: wait: %v\n", proc.ID(), err)
					return
				}
				fmt.Fprintf(os.Stderr, "%s: %s\n", proc.ID(), ps)
			})
		}
	}
	wg.Wait()
	return nil
}
