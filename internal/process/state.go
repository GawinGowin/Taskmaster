package process

import (
	"bytes"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"
)

type State int

const (
	Stopped State = iota
	Starting
	Running
	Backoff
	Stopping
	Exited
	Fatal
)

func (s State) String() string {
	return [...]string{"STOPPED", "STARTING", "RUNNING", "BACKOFF", "STOPPING", "EXITED", "FATAL"}[s]
}

func (s State) IsTerminal() bool {
	return s == Stopped || s == Exited || s == Fatal
}

type Status struct {
	ID       string
	State    State
	Gen      uint64
	Pid      int
	Started  time.Time
	Stopped  time.Time
	SpawnErr string
}

var tableHeader = []string{"NAME", "STATE", "PID", "UPTIME", "INFO"}

// Table は見出し行つきの status の表を返す。列幅は全行から決める。
// now は経過時間をテストで固定するために受け取る。
func Table(sts []Status, now time.Time) []string {
	var buf bytes.Buffer
	w := tabwriter.NewWriter(&buf, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, strings.Join(tableHeader, "\t"))
	for _, st := range sts {
		fmt.Fprintln(w, strings.Join(st.cells(now), "\t"))
	}
	w.Flush()

	// INFO が空の行は末尾が空白で埋まるので削る
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return lines
}

// 子の終了後も Pid は最後の値が残るので、PID と UPTIME は State で出し分ける。
func (st Status) cells(now time.Time) []string {
	pid, uptime := "-", "-"
	switch st.State {
	case Starting, Running, Stopping:
		pid = fmt.Sprint(st.Pid)
		uptime = humanDuration(now.Sub(st.Started))
	}
	return []string{st.ID, st.State.String(), pid, uptime, st.info(now)}
}

func (st Status) info(now time.Time) string {
	switch st.State {
	case Backoff, Fatal:
		if st.SpawnErr == "" {
			return "unknown error"
		}
		return st.SpawnErr
	case Stopped, Exited:
		if st.Started.IsZero() {
			return "not started"
		}
		// 起動に失敗し続けて BACKOFF から STOPPED になると Stopped は空
		if st.Stopped.IsZero() {
			return ""
		}
		verb := "stopped"
		if st.State == Exited {
			verb = "exited"
		}
		return verb + " " + humanDuration(now.Sub(st.Stopped)) + " ago"
	}
	return ""
}

// humanDuration は kubectl の AGE 列と同じ書き方にする（apimachinery の HumanDuration）。
// kubectl と違い、負は "<invalid>" ではなく 0s にする。
func humanDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	seconds := int(d / time.Second)
	if seconds < 60*2 {
		return fmt.Sprintf("%ds", seconds)
	}
	minutes := int(d / time.Minute)
	if minutes < 10 {
		if s := seconds % 60; s != 0 {
			return fmt.Sprintf("%dm%ds", minutes, s)
		}
		return fmt.Sprintf("%dm", minutes)
	}
	if minutes < 60*3 {
		return fmt.Sprintf("%dm", minutes)
	}
	hours := int(d / time.Hour)
	if hours < 8 {
		if m := minutes % 60; m != 0 {
			return fmt.Sprintf("%dh%dm", hours, m)
		}
		return fmt.Sprintf("%dh", hours)
	}
	if hours < 48 {
		return fmt.Sprintf("%dh", hours)
	}
	days := hours / 24
	if hours < 24*8 {
		if h := hours % 24; h != 0 {
			return fmt.Sprintf("%dd%dh", days, h)
		}
		return fmt.Sprintf("%dd", days)
	}
	if hours < 24*365*2 {
		return fmt.Sprintf("%dd", days)
	}
	years := days / 365
	if hours < 24*365*8 {
		if dy := days % 365; dy != 0 {
			return fmt.Sprintf("%dy%dd", years, dy)
		}
		return fmt.Sprintf("%dy", years)
	}
	return fmt.Sprintf("%dy", years)
}
