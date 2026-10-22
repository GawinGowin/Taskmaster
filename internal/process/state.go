package process

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
	ID    string
	State State
	Gen   uint64
}
