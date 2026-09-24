package process

import (
	"fmt"
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

type Transition struct {
	At   time.Duration
	ID   string
	From State
	To   State
	Why  string
}

func (t Transition) String() string {
	return fmt.Sprintf("%6.2fs  %-12s %-8s → %-8s  %s",
		t.At.Seconds(), t.ID, t.From, t.To, t.Why)
}

type Status struct {
	ID    string
	State State
	Gen   uint64
}
