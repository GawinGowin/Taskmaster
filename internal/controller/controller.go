package controller

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"time"

	"taskmaster/internal/config"
	"taskmaster/internal/process"
)

type Controller struct {
	groups   map[string]*ProgramGroup
	order    []string
	events   chan event
	shutdown bool
	log      []process.Transition
	t0       time.Time
	byID     map[string]*process.Process
}

func New(cfg *config.Config) (*Controller, error) {
	var c Controller
	c.order = slices.Sorted(maps.Keys(cfg.Programs))
	c.groups = make(map[string]*ProgramGroup, len(c.order))
	c.events = make(chan event, 64) // バッファ数 64 は暫定値
	c.byID = make(map[string]*process.Process)
	for _, name := range c.order {
		program := cfg.Programs[name]
		group, err := newProgramGroup(&program, name)
		if err != nil {
			for _, pg := range c.groups {
				pg.closeFd()
			}
			return nil, err
		}
		c.groups[name] = group
		for _, p := range group.procs {
			c.byID[p.ID()] = p
		}
	}
	return &c, nil
}

func (c *Controller) to(p *process.Process, next process.State, why string) {
	t := process.Transition{At: time.Since(c.t0), ID: p.ID(), From: p.State(), To: next, Why: why}
	c.log = append(c.log, t)
	p.SetState(next)
	fmt.Println(t)
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
			pg.closeFd()
			return nil, err
		}
		pg.procs = append(pg.procs, proc)
	}
	return &pg, nil
}

func (pg *ProgramGroup) closeFd() {
	if pg.stdout != pg.stderr {
		pg.stdout.Close()
	}
	pg.stderr.Close()
}
