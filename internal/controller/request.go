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
	switch ev.cmd {
	case CmdStart: // 引数あり
		return []string{"not implemented"}
	case CmdStop: // 引数あり
		return []string{"not implemented"}
	case CmdRestart: // 引数あり
		return []string{"not implemented"}
	case CmdStatus: // 引数あり
		targets := ev.targets
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
		if t == "all" {
			for _, k := range c.order {
				for _, p := range c.groups[k].procs {
					add(p)
				}
			}
			continue
		}
		name, index, hasIndex := strings.Cut(t, ":")
		g, ok := c.groups[name]
		if !ok {
			errs = append(errs, fmt.Sprintf("%s: ERROR (no such program)", t))
			continue
		}
		if !hasIndex {
			for _, p := range g.procs {
				add(p)
			}
			continue
		}
		i, err := strconv.Atoi(index) // checkTarget が数字だけを通し、失敗するのはoverflowの時のみ
		if err != nil || i >= len(g.procs) {
			errs = append(errs, fmt.Sprintf("%s: ERROR (no such process, numprocs is %d)", t, len(g.procs)))
			continue
		}
		add(g.procs[i])
	}
	return procs, errs
}
