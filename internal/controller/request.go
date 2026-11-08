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
