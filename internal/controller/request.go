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
		return c.eachProcess("start", targets, true, c.startOne)
	case CmdStop: // 引数あり
		return c.eachProcess("stop", targets, false, c.stopOne)
	case CmdRestart: // 引数あり
		return c.eachProcess("restart", targets, true, c.restartOne)
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
	case CmdReload: // 引数無し
		if c.shutdown {
			return []string{"reload: ERROR (shutting down)"}
		}
		return c.reload("shell")
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

// eachProcess は targets を展開し、重複を除いた各プロセスに act を呼んで応答の行を返す。
// act は、何かしたら acted を true にして line を返し、何もしなければ状態を変えずに false を返す。
// false のとき、個体指定なら eachProcess が `ERROR (already <STATE>)` を出す。
// rejectWhileShuttingDown が true なら、shutdown 中は act を呼ばずに `ERROR (shutting down)` を返す。
func (c *Controller) eachProcess(
	verb string, targets []string, rejectWhileShuttingDown bool,
	act func(p *process.Process) (line string, acted bool),
) []string {
	if len(targets) == 0 {
		return []string{fmt.Sprintf("%s: requires a target", verb)}
	}
	msg := []string{}
	seen := map[string]*process.Process{}
	for _, t := range targets {
		if rejectWhileShuttingDown && c.shutdown {
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
			line, acted := act(p)
			if acted {
				covered = true
				msg = append(msg, line)
			} else if individual {
				covered = true
				msg = append(msg, fmt.Sprintf("%s: ERROR (already %s)", p.ID(), p.State()))
			}
		}
		if !covered && !individual {
			msg = append(msg, fmt.Sprintf("%s: nothing to %s", t, verb))
		}
	}
	return msg
}

func (c *Controller) startOne(p *process.Process) (line string, acted bool) {
	switch p.State() {
	case process.Stopped, process.Exited, process.Fatal:
		p.ResetRetries()
		c.start(p, slog.String("by", "shell"))
		if p.State() == process.Starting {
			line = fmt.Sprintf("%s: starting (pid %d, gen %d)", p.ID(), p.Pid(), p.Gen())
		} else {
			line = fmt.Sprintf("%s: spawn failed (%s), now %s", p.ID(), p.SpawnErr(), p.State())
		}
		acted = true
	case process.Stopping:
		p.SetStartAfterStop("shell")
		line = fmt.Sprintf("%s: will start after stop", p.ID())
		acted = true
	}
	return line, acted
}

func (c *Controller) stopOne(p *process.Process) (line string, acted bool) {
	switch p.State() {
	case process.Starting, process.Running:
		acted = true
		line = fmt.Sprintf("%s: stopping (%s, SIGKILL in %.1fs)", p.ID(), p.Spec().Stopsignal, float64(p.Spec().Stoptime))
		c.stop(p, slog.String("by", "shell"))
	case process.Backoff:
		acted = true
		line = fmt.Sprintf("%s: stopped", p.ID())
		c.stop(p, slog.String("by", "shell"))
	case process.Stopping:
		if p.StartAfterStop() != "" {
			acted = true
			p.SetStartAfterStop("")
			line = fmt.Sprintf("%s: restart cancelled", p.ID())
		}
	}
	return line, acted
}

func (c *Controller) restartOne(p *process.Process) (line string, acted bool) {
	switch p.State() {
	case process.Starting, process.Running:
		c.stop(p, slog.String("by", "shell"))
		p.SetStartAfterStop("shell")
		line = fmt.Sprintf("%s: restarting (%s, SIGKILL in %.1fs)", p.ID(), p.Spec().Stopsignal, float64(p.Spec().Stoptime))
		acted = true
	case process.Backoff:
		c.stop(p, slog.String("by", "shell"))
		return c.startOne(p)
	default:
		return c.startOne(p)
	}
	return line, acted
}
