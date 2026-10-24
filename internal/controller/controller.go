package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
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
	byID     map[string]*process.Process
	logger   *slog.Logger
	sinks    map[string]*process.Sink
}

// バックオフ 500 msは暫定値
const backoffUnit = 500 * time.Millisecond

func New(cfg *config.Config) (*Controller, error) {
	var c Controller
	c.order = slices.Sorted(maps.Keys(cfg.Programs))
	c.groups = make(map[string]*ProgramGroup, len(c.order))
	c.events = make(chan event, 64) // バッファ数 64 は暫定値
	c.byID = make(map[string]*process.Process)
	c.sinks = make(map[string]*process.Sink)

	for _, name := range c.order {
		program := cfg.Programs[name]
		group, err := c.newProgramGroup(&program, name)
		if err != nil {
			return nil, errors.Join(err, c.Close())
		}
		c.groups[name] = group
		for _, p := range group.procs {
			c.byID[p.ID()] = p
		}
	}
	return &c, nil
}

func (c *Controller) Close() error {
	var errs []error
	for path, s := range c.sinks {
		if err := s.Close(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
		}
	}
	clear(c.sinks)
	return errors.Join(errs...)
}

func (c *Controller) Run(logger *slog.Logger) {
	c.logger = logger

	sigCh := make(chan os.Signal, 1)
	defer signal.Stop(sigCh)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for sig := range sigCh {
			s, ok := sig.(syscall.Signal)
			if ok {
				c.events <- evSignal{sig: s}
			}
		}
	}()

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
			c.to(slog.LevelInfo, p, process.Running, slog.Int("starttime", p.Spec().Starttime))

		case evExited:
			p := c.byID[ev.id]
			if p == nil {
				continue
			}
			if p.Gen() != ev.gen {
				continue
			}
			var ok bool
			how := death(ev.ps, ev.err)
			if ev.err == nil {
				ok = isExpected(ev.ps, p.Spec().Exitcodes)
			}
			switch p.State() {
			case process.Starting:
				c.startFailed(p, how)

			case process.Stopping:
				c.to(slog.LevelInfo, p, process.Stopped, how)

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
				lvl := slog.LevelInfo
				if !ok {
					lvl = slog.LevelWarn
				}
				c.to(lvl, p, process.Exited, how, slog.Bool("expected", ok))
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

		case evSignal:
			signal := slog.Int("signal", int(ev.sig))
			switch ev.sig {
			case syscall.SIGINT, syscall.SIGTERM:
				if c.shutdown {
					c.logger.LogAttrs(context.Background(), slog.LevelInfo, "signal",
						signal,
						slog.String("action", "ignore"),
						slog.String("reason", "already_shutting_down"),
					)
				} else {
					c.logger.LogAttrs(context.Background(), slog.LevelInfo, "signal",
						signal,
						slog.String("action", "shutdown"),
					)
					c.shutdown = true
					for _, name := range c.order {
						for _, p := range c.groups[name].procs {
							c.stop(p)
						}
					}
				}
			case syscall.SIGHUP:
				c.logger.LogAttrs(context.Background(), slog.LevelInfo, "signal",
					signal,
					slog.String("action", "ignore"),
					slog.String("reason", "not_implemented"),
				)
			}

		case evStopTimeout:
			p := c.byID[ev.id]
			if p == nil {
				continue
			}
			if p.Gen() != ev.gen || p.State() != process.Stopping {
				continue
			}
			c.logger.LogAttrs(context.Background(), slog.LevelWarn, "kill", []slog.Attr{
				slog.String("id", p.ID()),
				slog.Uint64("gen", p.Gen()),
				slog.Int("pid", p.Pid()),
				slog.Int("signal", int(syscall.SIGKILL)),
				slog.Int("stoptime", p.Spec().Stoptime),
			}...)
			c.sendSignal(p, syscall.SIGKILL)
		}
	}
}

func (c *Controller) to(lvl slog.Level, p *process.Process, next process.State, attrs ...slog.Attr) {
	from := p.State()
	a := []slog.Attr{
		slog.String("id", p.ID()),
		slog.String("from", from.String()),
		slog.String("to", next.String()),
		slog.Uint64("gen", p.Gen()),
	}
	if (from == process.Starting || from == process.Running || from == process.Stopping) && p.Pid() != 0 {
		a = append(a, slog.Int("pid", p.Pid()))
	}
	a = append(a, attrs...)
	p.SetState(next)
	c.logger.LogAttrs(context.Background(), lvl, "transition", a...)
}

func (c *Controller) start(p *process.Process) {
	gen := p.NextGen()
	id := p.ID()
	c.to(slog.LevelInfo, p, process.Starting)
	wait, err := p.Start()
	if err != nil {
		c.startFailed(p, slog.String("err", err.Error()))
		return
	}

	c.logger.LogAttrs(context.Background(), slog.LevelInfo, "spawn", []slog.Attr{
		slog.String("id", id),
		slog.Uint64("gen", gen),
		slog.Int("pid", p.Pid()),
	}...)

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

func (c *Controller) startFailed(p *process.Process, how slog.Attr) {
	sp := p.Spec()
	retries := p.NextRetries()
	delay := time.Duration(retries) * backoffUnit

	a := []slog.Attr{
		how,
		slog.Int("retries", retries),
		slog.Int("startretries", sp.Startretries),
	}
	giveUp := retries > sp.Startretries
	if !giveUp {
		a = append(a, slog.Duration("delay", delay))
	}
	c.to(slog.LevelWarn, p, process.Backoff, a...)
	if giveUp {
		p.ResetRetries()
		c.to(slog.LevelError, p, process.Fatal,
			slog.Int("retries", retries),
			slog.Int("startretries", sp.Startretries),
		)
		return
	}
	time.AfterFunc(delay, func() {
		c.events <- evBackoffElapsed{id: p.ID(), gen: p.Gen()}
	})
}

func (c *Controller) stop(p *process.Process) {
	by := slog.String("by", "shutdown")

	switch p.State() {
	case process.Starting, process.Running:
		gen := p.Gen()
		id := p.ID()

		stopsignal := syscall.Signal(p.Spec().Stopsignal)
		stoptime := p.Spec().Stoptime

		a := []slog.Attr{
			slog.Int("stopsignal", int(stopsignal)),
			slog.Int("stoptime", stoptime),
			by,
		}
		c.to(slog.LevelInfo, p, process.Stopping, a...)
		c.sendSignal(p, stopsignal)
		time.AfterFunc(time.Duration(stoptime)*time.Second, func() {
			c.events <- evStopTimeout{id: id, gen: gen}
		})

	case process.Backoff:
		c.to(slog.LevelInfo, p, process.Stopped, by)
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
	if err == nil {
		return
	}
	a := []slog.Attr{
		slog.String("id", id),
		slog.Uint64("gen", p.Gen()),
		slog.Int("pid", pid),
		slog.Int("signal", int(sig)),
		slog.String("err", err.Error()),
	}
	if errors.Is(err, syscall.ESRCH) {
		c.logger.LogAttrs(context.Background(), slog.LevelInfo, "signal_error", a...)
	} else {
		c.logger.LogAttrs(context.Background(), slog.LevelError, "signal_error", a...)
	}
}

type ProgramGroup struct {
	spec   config.Program // type Process も同様に値として持つので暫定で持たせる
	stdout *process.Sink
	stderr *process.Sink
	procs  []*process.Process
}

func (c *Controller) newProgramGroup(p *config.Program, name string) (*ProgramGroup, error) {
	var pg ProgramGroup
	pg.spec = *p

	var err error
	pg.stdout, err = c.sinkFor(string(p.Stdout))
	if err != nil {
		return nil, fmt.Errorf("%s: stdout: %w", name, err)
	}
	pg.stderr, err = c.sinkFor(string(p.Stderr))
	if err != nil {
		return nil, fmt.Errorf("%s: stderr: %w", name, err)
	}
	for i := 0; i < p.Numprocs; i++ {
		proc, err := process.New(p, name, i, pg.stdout, pg.stderr)
		if err != nil {
			return nil, err
		}
		pg.procs = append(pg.procs, proc)
	}
	return &pg, nil
}

func (c *Controller) sinkFor(path string) (*process.Sink, error) {
	if path == "" {
		return nil, nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if s, ok := c.sinks[abs]; ok {
		return s, nil
	}
	f, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	s, err := process.NewSink(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	c.sinks[abs] = s
	return s, nil
}

func death(ps *os.ProcessState, err error) slog.Attr {
	if err != nil {
		return slog.String("err", err.Error())
	}
	ws := ps.Sys().(syscall.WaitStatus)
	if ws.Signaled() {
		return slog.Int("signal", int(ws.Signal()))
	}
	return slog.Int("exitcode", ps.ExitCode())
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
