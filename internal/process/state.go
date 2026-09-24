package process

import (
	"os"
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

func (s State) Terminal() bool {
	return s == Stopped || s == Exited || s == Fatal
}

type Transition struct {
	At   time.Duration
	ID   string
	From State
	To   State
	Why  string
}

type Status struct {
	ID    string
	State State
	Gen   uint64
}

type evExited struct {
	id  string
	gen uint64
	ps  *os.ProcessState
}
type evStartTimeElapsed struct {
	id  string
	gen uint64
}
type evStopTimeout struct{}
type evBackoffElapsed struct{}
