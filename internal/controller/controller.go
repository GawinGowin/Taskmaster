package controller

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
	"syscall"
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

// バックオフ 500 msは暫定値
const backoffUnit = 500 * time.Millisecond

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

func (c *Controller) Run() {
	c.t0 = time.Now()

	for _, name := range c.order {
		if g := c.groups[name]; g.spec.Autostart {
			for _, p := range g.procs {
				c.start(p)
			}
		}
	}

	for !(c.shutdown && c.isAllTerminal()) {
		switch ev := (<-c.events).(type) {
		case evStartTimeElapsed:
			p := c.byID[ev.id]
			if p == nil {
				continue
			}
			if p.Gen() != ev.gen || p.State() != process.Starting {
				continue
			}
			p.ResetRetries()
			c.to(p, process.Running, "")

		case evExited:
			p := c.byID[ev.id]
			if p == nil {
				continue
			}
			if p.Gen() != ev.gen {
				continue
			}
			var ok bool
			var how string
			if ev.err != nil {
				ok = false
				how = ev.err.Error()
			} else {
				ok = isExpected(ev.ps, p.Spec().Exitcodes)
				how = describe(ev.ps)
			}
			switch p.State() {
			case process.Starting:
				c.startFailed(p, how)

			case process.Stopping:
				c.to(p, process.Stopped, "expected stop ("+how+")")

			case process.Running:
				restart := false
				switch p.Spec().Autorestart {
				case config.Never:
					restart = false
				case config.Unexpected:
					restart = !ok
				case config.Always:
					restart = true
				}
				if c.shutdown {
					restart = false
				}
				c.to(p, process.Exited, fmt.Sprintf("%s (expected=%v)", how, ok))
				if restart {
					c.start(p)
				}
			}

		case evBackoffElapsed:
			p := c.byID[ev.id]
			if p == nil {
				continue
			}
			if p.Gen() != ev.gen || p.State() != process.Backoff {
				continue
			}
			c.start(p)

		case evShutdown:
			if !c.shutdown {
				c.shutdown = true
				for _, name := range c.order {
					for _, p := range c.groups[name].procs {
						c.stop(p)
					}
				}
			}

		case evStopTimeout:
			p := c.byID[ev.id]
			if p == nil {
				continue
			}
			if p.Gen() != ev.gen || p.State() != process.Stopping {
				continue
			}
			c.sendSignal(p, syscall.SIGKILL)
		}
	}
}

func (c *Controller) to(p *process.Process, next process.State, why string) {
	t := process.Transition{At: time.Since(c.t0), ID: p.ID(), From: p.State(), To: next, Why: why}
	c.log = append(c.log, t)
	p.SetState(next)
	fmt.Println(t)
}

func (c *Controller) start(p *process.Process) {
	gen := p.NextGen()
	id := p.ID()
	c.to(p, process.Starting, fmt.Sprintf("gen=%d", gen))
	wait, err := p.Start()
	if err != nil {
		c.startFailed(p, err.Error())
		return
	}

	go func() {
		ps, err := wait()
		// 終了コードが 0 以外・シグナル死も *exec.ExitError で返る。
		// err には wait 自体の失敗だけを残す。
		if _, ok := errors.AsType[*exec.ExitError](err); ok {
			err = nil
		}
		c.events <- evExited{id: id, gen: gen, ps: ps, err: err}
	}()

	time.AfterFunc(time.Duration(p.Spec().Starttime)*time.Second, func() {
		c.events <- evStartTimeElapsed{id: id, gen: gen}
	})
}

func (c *Controller) startFailed(p *process.Process, why string) {
	sp := p.Spec()
	gen := p.Gen()
	retries := p.NextRetries()
	id := p.ID()
	c.to(p, process.Backoff, fmt.Sprintf("retry (%d/%d) %s", retries, sp.Startretries, why))
	if retries > p.Spec().Startretries {
		p.ResetRetries()
		c.to(p, process.Fatal, fmt.Sprintf("retries exceed: %s", why))
		return
	}
	time.AfterFunc(time.Duration(retries)*backoffUnit, func() {
		c.events <- evBackoffElapsed{id: id, gen: gen}
	})
}

func (c *Controller) stop(p *process.Process) {
	switch p.State() {
	case process.Starting, process.Running:
		gen := p.Gen()
		id := p.ID()
		stoptime := p.Spec().Stoptime
		stopsignal := syscall.Signal(p.Spec().Stopsignal)

		c.to(p, process.Stopping, fmt.Sprintf("sent %s, kill in %ds", stopsignal, stoptime))
		c.sendSignal(p, stopsignal)

		time.AfterFunc(time.Duration(stoptime)*time.Second, func() {
			c.events <- evStopTimeout{id: id, gen: gen}
		})

	case process.Backoff:
		c.to(p, process.Stopped, "backoff cancelled")
		p.NextGen()
	}
}

func (c *Controller) isAllTerminal() bool {
	for _, v := range c.byID {
		if !v.State().IsTerminal() {
			return false
		}
	}
	return true
}

func (c *Controller) sendSignal(p *process.Process, sig syscall.Signal) {
	var err error
	pid := p.Pid()
	id := p.ID()
	if pid != 0 {
		err = syscall.Kill(-pid, sig)
	}
	if errors.Is(err, syscall.ESRCH) {
		fmt.Fprintf(os.Stderr, "%s (pid %d): unable to send %s to group, probably already exited: %v\n", id, pid, sig, err)
	} else if err != nil {
		fmt.Fprintf(os.Stderr, "%s (pid %d): failed to send %s to group: %v\n", id, pid, sig, err)
	}
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

func describe(ps *os.ProcessState) string {
	ws := ps.Sys().(syscall.WaitStatus)
	if ws.Signaled() {
		return "signal " + ws.Signal().String()
	}
	return fmt.Sprintf("exit %d", ps.ExitCode())
}

// シグナル死は常に想定外。
func isExpected(ps *os.ProcessState, codes []int) bool {
	if !ps.Exited() {
		return false
	}
	for _, c := range codes {
		if ps.ExitCode() == c {
			return true
		}
	}
	return false
}
