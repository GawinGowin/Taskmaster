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
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"taskmaster/internal/config"
	"taskmaster/internal/process"
)

type Controller struct {
	cfgPath  string
	cfg      *config.Config
	groups   map[string]*ProgramGroup
	order    []string
	names    atomic.Pointer[[]string]
	events   chan event
	done     chan struct{}
	shutdown bool
	byID     map[string]*process.Process
	logger   *slog.Logger
	sinks    map[string]*process.Sink
}

// バックオフ 500 msは暫定値
const backoffUnit = 500 * time.Millisecond

func New(cfgPath string, cfg *config.Config) (*Controller, error) {
	var c Controller
	c.cfgPath = cfgPath
	c.cfg = cfg
	c.order = slices.Sorted(maps.Keys(cfg.Programs))
	c.publishNames()
	c.groups = make(map[string]*ProgramGroup, len(c.order))
	c.events = make(chan event, 64) // バッファ数 64 は暫定値
	c.byID = make(map[string]*process.Process)
	c.sinks = make(map[string]*process.Sink)

	for _, name := range c.order {
		program := cfg.Programs[name]

		outSink, err := c.sinkFor(string(program.Stdout))
		if err != nil {
			return nil, errors.Join(fmt.Errorf("%s: stdout: %w", name, err), c.Close())
		}
		errSink, err := c.sinkFor(string(program.Stderr))
		if err != nil {
			return nil, errors.Join(fmt.Errorf("%s: stderr: %w", name, err), c.Close())
		}
		group := c.newProgramGroup(&program, name, outSink, errSink)
		c.groups[name] = group
		for _, p := range group.procs {
			c.byID[p.ID()] = p
		}
	}
	c.done = make(chan struct{})
	return &c, nil
}

func (c *Controller) publishNames() {
	names := slices.Clone(c.order)
	c.names.Store(&names)
}

func (c *Controller) Names() []string {
	if p := c.names.Load(); p != nil {
		return *p
	}
	return nil
}

func (c *Controller) Events() chan<- event { return c.events }

func (c *Controller) Done() <-chan struct{} { return c.done }

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
	defer close(c.done)
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
		if g := c.groups[name]; c.cfg.Programs[name].Autostart {
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
			p.SetStoppedtime(ev.when)
			var ok bool
			how, msg := death(ev.ps, ev.err)
			if ev.err == nil {
				ok = isExpected(ev.ps, p.Spec().Exitcodes)
			}
			switch p.State() {
			case process.Starting:
				p.SetSpawnErr(msg)
				c.startFailed(p, how)

			case process.Stopping:
				c.to(slog.LevelInfo, p, process.Stopped, how)
				if g, ok := c.groups[p.Name()]; !ok || !slices.Contains(g.procs, p) {
					delete(c.byID, p.ID())
				}
				by := p.StartAfterStop()
				if by != "" {
					p.SetStartAfterStop("")
					p.ResetRetries()
					c.start(p, slog.String("by", by))
				}

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
				c.beginShutdown("signal", signal)
			case syscall.SIGHUP:
				if c.shutdown {
					c.logger.LogAttrs(context.Background(), slog.LevelInfo, "signal",
						[]slog.Attr{
							signal,
							slog.String("action", "ignore"),
							slog.String("reason", "already_shutting_down"),
						}...,
					)
				} else {
					c.logger.LogAttrs(context.Background(), slog.LevelInfo, "signal",
						signal,
						slog.String("action", "reload"),
					)
					c.reload("signal")
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
			c.logger.LogAttrs(context.Background(), slog.LevelWarn, "kill", []slog.Attr{
				slog.String("id", p.ID()),
				slog.Uint64("gen", p.Gen()),
				slog.Int("pid", p.Pid()),
				slog.Int("signal", int(syscall.SIGKILL)),
				slog.Int("stoptime", p.Spec().Stoptime),
			}...)
			c.sendSignal(p, syscall.SIGKILL)

		case evRequest:
			r := c.handleRequest(ev)
			if ev.reply != nil {
				ev.reply <- r
				close(ev.reply)
			}
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

func (c *Controller) start(p *process.Process, attrs ...slog.Attr) {
	gen := p.NextGen()
	id := p.ID()
	c.to(slog.LevelInfo, p, process.Starting, attrs...)
	p.SetSpawnErr("")
	wait, err := p.Start()
	if err != nil {
		p.SetSpawnErr(err.Error())
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
		stoptime := time.Now()
		// 終了コードが 0 以外・シグナル死も *exec.ExitError で返る。
		// err には wait 自体の失敗だけを残す。
		if _, ok := errors.AsType[*exec.ExitError](err); ok {
			err = nil
		}
		c.events <- evExited{id: id, gen: gen, ps: ps, err: err, when: stoptime}
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
	id := p.ID()
	gen := p.Gen()
	time.AfterFunc(delay, func() {
		c.events <- evBackoffElapsed{id: id, gen: gen}
	})
}

func (c *Controller) stop(p *process.Process, attrs ...slog.Attr) {
	p.SetStartAfterStop("")
	switch p.State() {
	case process.Starting, process.Running:
		gen := p.Gen()
		id := p.ID()

		stopsignal := syscall.Signal(p.Spec().Stopsignal)
		stoptime := p.Spec().Stoptime

		a := []slog.Attr{
			slog.Int("stopsignal", int(stopsignal)),
			slog.Int("stoptime", stoptime),
		}
		a = append(a, attrs...)
		c.to(slog.LevelInfo, p, process.Stopping, a...)
		c.sendSignal(p, stopsignal)
		time.AfterFunc(time.Duration(stoptime)*time.Second, func() {
			c.events <- evStopTimeout{id: id, gen: gen}
		})

	case process.Backoff:
		c.to(slog.LevelInfo, p, process.Stopped, attrs...)
	}
}

func (c *Controller) beginShutdown(msg string, attrs ...slog.Attr) {
	if c.shutdown {
		c.logger.LogAttrs(context.Background(), slog.LevelInfo, msg,
			slices.Concat(attrs, []slog.Attr{
				slog.String("action", "ignore"),
				slog.String("reason", "already_shutting_down"),
			})...,
		)
		return
	}
	c.shutdown = true
	c.logger.LogAttrs(context.Background(), slog.LevelInfo, msg,
		slices.Concat(attrs, []slog.Attr{
			slog.String("action", "shutdown"),
		})...)
	for _, name := range c.order {
		for _, p := range c.groups[name].procs {
			c.stop(p, slog.String("by", "shutdown"))
		}
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

func (c *Controller) reload(by string) []string {
	showErr := func(msg string) []string {
		c.logger.LogAttrs(context.Background(), slog.LevelError, "reload",
			[]slog.Attr{
				slog.String("by", by),
				slog.String("err", msg),
			}...)
		return []string{fmt.Sprintf("reload: ERROR (%s)", msg)}
	}

	newCfg, err := config.Load(c.cfgPath)
	if err != nil {
		return showErr(err.Error())
	}
	changes := config.Diff(c.cfg, newCfg)
	outSinks := map[string]*process.Sink{}
	errSinks := map[string]*process.Sink{}
	n := 0
	stat := [5][]string{}
	for _, change := range changes {
		setSinks := func() error {
			outSinks[change.Name], err = c.sinkFor(string(newCfg.Programs[change.Name].Stdout))
			if err != nil {
				return fmt.Errorf("%s: stdout: %v", change.Name, err)
			}
			errSinks[change.Name], err = c.sinkFor(string(newCfg.Programs[change.Name].Stderr))
			if err != nil {
				return fmt.Errorf("%s: stderr: %v", change.Name, err)
			}
			return nil
		}
		switch change.Kind {
		case config.Unchanged:
			n++
		case config.Added, config.Changed, config.Resized:
			err := setSinks()
			if err != nil {
				return showErr(err.Error())
			}
			stat[change.Kind] = append(stat[change.Kind], change.Name)
		default:
			stat[change.Kind] = append(stat[change.Kind], change.Name)
		}
	}
	attr := []slog.Attr{slog.String("by", by)}
	for i, s := range stat {
		if len(s) > 0 {
			attr = append(attr, slog.String(config.ChangeKind(i).String(), strings.Join(s, ",")))
		}
	}
	attr = append(attr, slog.Int("unchanged", n))
	c.logger.LogAttrs(context.Background(), slog.LevelInfo, "reload", attr...)
	msg := []string{}

	stopAndDelete := func(p *process.Process) {
		c.stop(p, slog.String("by", "reload"))
		if p.State().IsTerminal() {
			delete(c.byID, p.ID())
		}
	}

	for _, change := range changes {
		switch change.Kind {
		case config.Unchanged:
			continue
		case config.Added, config.Changed, config.Resized:
			prog := newCfg.Programs[change.Name]
			outSink := outSinks[change.Name]
			errSink := errSinks[change.Name]
			old := c.groups[change.Name]
			g := &ProgramGroup{stdout: outSink, stderr: errSink}
			for i := 0; i < prog.Numprocs; i++ {
				id := fmt.Sprintf("%s:%d", change.Name, i)
				p, ok := c.byID[id]
				if !ok {
					p = process.New(&prog, change.Name, i, outSink, errSink)
					c.byID[id] = p
				}
				g.procs = append(g.procs, p)
				if change.Kind == config.Resized && i < len(old.procs) {
					p.SetSpec(&prog, outSink, errSink)
				} else {
					c.applySpec(p, &prog, outSink, errSink)
				}
			}
			c.groups[change.Name] = g
			if old != nil && len(old.procs) > prog.Numprocs {
				for _, p := range old.procs[prog.Numprocs:] {
					stopAndDelete(p)
				}
			}

		case config.Removed:
			old := c.groups[change.Name]
			delete(c.groups, change.Name)
			for _, p := range old.procs {
				stopAndDelete(p)
			}
		}
		msg = append(msg, change.String())
	}
	c.order = slices.Sorted(maps.Keys(newCfg.Programs))
	c.cfg = newCfg
	c.publishNames()
	if n == len(changes) {
		msg = append(msg, "no changes")
	} else {
		msg = append(msg, fmt.Sprintf("%d unchanged", n))
	}
	return msg
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
	stdout *process.Sink
	stderr *process.Sink
	procs  []*process.Process
}

func (c *Controller) newProgramGroup(p *config.Program, name string, outSink *process.Sink, errSink *process.Sink) *ProgramGroup {
	var pg ProgramGroup
	pg.stdout = outSink
	pg.stderr = errSink
	for i := 0; i < p.Numprocs; i++ {
		pg.procs = append(pg.procs, process.New(p, name, i, pg.stdout, pg.stderr))
	}
	return &pg
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

// 新しい spec を当て、state と autostart に応じて止める・起動する
func (c *Controller) applySpec(old *process.Process, prog *config.Program, outSink, errSink *process.Sink) {
	st := old.State()
	shouldStop := st == process.Starting ||
		st == process.Running ||
		st == process.Backoff
	// 予約ありの Stopping もしくは, Starting / Running / Backoff
	isRunning := shouldStop || (old.StartAfterStop() != "" && st == process.Stopping)
	if shouldStop {
		c.stop(old, slog.String("by", "reload"))
	}
	old.SetSpec(prog, outSink, errSink)
	if isRunning || prog.Autostart {
		if old.State() == process.Stopping {
			if old.StartAfterStop() == "" {
				old.SetStartAfterStop("reload")
			}
		} else {
			old.ResetRetries()
			c.start(old, slog.String("by", "reload"))
		}
	}
}

func death(ps *os.ProcessState, err error) (slog.Attr, string) {
	if err != nil {
		msg := err.Error()
		return slog.String("err", msg), msg
	}
	ws := ps.Sys().(syscall.WaitStatus)
	if ws.Signaled() {
		msg := fmt.Sprintf("terminated by signal %d", int(ws.Signal()))
		if ws.CoreDump() {
			msg += " (core dumped)"
		}
		return slog.Int("signal", int(ws.Signal())), msg
	}
	msg := fmt.Sprintf("exit status %d", ps.ExitCode())
	return slog.Int("exitcode", ps.ExitCode()), msg
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
