package controller

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"taskmaster/internal/process"
)

func (c *Controller) handleRequest(ev evRequest) []string {
	var err error
	for _, t := range ev.targets {
		if err = checkTarget(t); err != nil {
			break
		}
	}
	if err != nil {
		return []string{err.Error()}
	}
	targets := ev.targets
	switch ev.cmd {
	case CmdStart: // 引数あり
		if len(targets) == 0 {
			return []string{"start: requires a target"}
		}
		msg := []string{}
		seen := map[string]*process.Process{}
		for _, t := range targets {
			if c.shutdown {
				msg = append(msg, fmt.Sprintf("%s: ERROR (shutting down)", t))
				continue
			}
			covered := false
			ps, individual, errLine := c.resolveOne(t)
			if errLine != "" {
				msg = append(msg, errLine)
				continue
			}
			for _, p := range ps {
				if _, ok := seen[p.ID()]; ok {
					covered = true
					continue
				}
				seen[p.ID()] = p

				isStartable := (p.State() == process.Stopped || p.State() == process.Exited || p.State() == process.Fatal)
				if isStartable {
					p.ResetRetries()
					c.start(p, slog.String("by", "shell"))
					if p.State() == process.Starting {
						msg = append(msg, fmt.Sprintf("%s: starting (pid %d, gen %d)", p.ID(), p.Pid(), p.Gen()))
					} else if p.State() == process.Backoff || p.State() == process.Fatal {
						msg = append(msg, fmt.Sprintf("%s: spawn failed (%s), now %s", p.ID(), p.SpawnErr(), p.State()))
					}
					covered = true
				} else if individual {
					covered = true
					msg = append(msg, fmt.Sprintf("%s: ERROR (already %s)", p.ID(), p.State()))
				}
			}
			if !covered && !individual {
				msg = append(msg, fmt.Sprintf("%s: nothing to start", t))
			}
		}
		return msg
	case CmdStop: // 引数あり
		return []string{"not implemented"}
	case CmdRestart: // 引数あり
		return []string{"not implemented"}
	case CmdStatus: // 引数あり
		if len(targets) == 0 {
			targets = append(targets, "all")
		}
		ps, errs := c.resolve(targets)
		ret := []string{}
		sts := []process.Status{}
		for _, p := range ps {
			st := process.Status{
				ID:       p.ID(),
				State:    p.State(),
				Gen:      p.Gen(),
				Pid:      p.Pid(),
				Started:  p.Started(),
				Stopped:  p.Stopped(),
				SpawnErr: p.SpawnErr(),
			}
			sts = append(sts, st)
		}
		if len(sts) > 0 {
			ret = append(ret, process.Table(sts, time.Now())...)
		}
		ret = append(ret, errs...)
		return ret
	case CmdReload:
		return []string{"not implemented"}
	case CmdQuit:
		c.beginShutdown("cmd", slog.String("cmd", "quit"))
		return []string{"shutting down"}
	default:
		return []string{"unknown command"}
	}
}

func (c *Controller) resolve(targets []string) (procs []*process.Process, errs []string) {
	seen := map[string]bool{}
	add := func(p *process.Process) {
		if seen[p.ID()] {
			return
		}
		seen[p.ID()] = true
		procs = append(procs, p)
	}
	for _, t := range targets {
		ps, _, errLine := c.resolveOne(t)
		for _, p := range ps {
			add(p)
		}
		if errLine != "" {
			errs = append(errs, errLine)
		}
	}
	return procs, errs
}

func (c *Controller) resolveOne(t string) (procs []*process.Process, individual bool, errLine string) {
	// name もしくは all でまとめて指定したターゲット: individual == false
	// name:index による個別指定したターゲット: individual == true
	if t == "all" {
		for _, k := range c.order {
			for _, p := range c.groups[k].procs {
				procs = append(procs, p)
			}
		}
		return procs, false, ""
	}
	name, index, hasIndex := strings.Cut(t, ":")
	g, ok := c.groups[name]
	if !ok {
		return procs, hasIndex, fmt.Sprintf("%s: ERROR (no such program)", t)
	}
	if !hasIndex {
		procs = append(procs, g.procs...)
		return procs, hasIndex, ""
	}
	i, err := strconv.Atoi(index) // checkTarget が数字だけを通し、失敗するのはoverflowの時のみ
	if err != nil || i >= len(g.procs) {
		return procs, hasIndex, fmt.Sprintf("%s: ERROR (no such process, numprocs is %d)", t, len(g.procs))
	}
	procs = append(procs, g.procs[i])
	return procs, hasIndex, ""
}
