package controller

import (
	"fmt"
	"os"
	"time"

	"taskmaster/internal/config"
	"taskmaster/internal/process"
)

type Controller struct {
	groups   map[string]*ProgramGroup
	order    []string
	events   chan any
	shutdown bool
	log      []process.Transition
	t0       time.Time
}

type ProgramGroup struct {
	spec   config.Program // type Process も同様に値として持つので暫定で持たせる
	stdout *os.File
	stderr *os.File
	procs  []*process.Process
}

func newProgramGroup(p *config.Program, name string) (*ProgramGroup, error) {
	var pg ProgramGroup
	pg.spec = *p

	var err error
	pg.stdout, err = process.OpenRedirect(string(p.Stdout))
	if err != nil {
		return nil, fmt.Errorf("%s: stdout: %w", name, err)
	}
	if p.Stderr == p.Stdout {
		pg.stderr = pg.stdout
	} else {
		pg.stderr, err = process.OpenRedirect(string(p.Stderr))
		if err != nil {
			pg.stdout.Close()
			return nil, fmt.Errorf("%s: stderr: %w", name, err)
		}
	}
	for i := 0; i < p.Numprocs; i++ {
		proc, err := process.New(p, name, i, pg.stdout, pg.stderr)
		if err != nil {
			if pg.stdout != pg.stderr {
				pg.stdout.Close()
			}
			pg.stderr.Close()
			return nil, err
		}
		pg.procs = append(pg.procs, proc)
	}
	return &pg, nil
}
