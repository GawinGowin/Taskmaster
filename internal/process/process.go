package process

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"taskmaster/internal/config"
)

type Process struct {
	prog   config.Program
	cmd    *exec.Cmd
	index  int
	name   string
	stdout *os.File
	stderr *os.File
	state  State
	gen    uint64
}

func New(p *config.Program, name string, index int, stdout *os.File, stderr *os.File) (*Process, error) {
	if len(p.Cmd) == 0 {
		return nil, fmt.Errorf("%s:%d: cmd must not be empty", name, index)
	}
	if stdout == nil {
		return nil, fmt.Errorf("%s:%d: stdout must not be nil", name, index)
	}
	if stderr == nil {
		return nil, fmt.Errorf("%s:%d: stderr must not be nil", name, index)
	}

	return &Process{
		prog:   *p,
		cmd:    nil, // Start() にて cmd は build する
		index:  index,
		name:   name,
		stdout: stdout,
		stderr: stderr,
		state:  Stopped,
		gen:    0,
	}, nil
}

func (p *Process) buildCmd() (cmd *exec.Cmd) {
	cmd = exec.Command(p.prog.Cmd[0], p.prog.Cmd[1:]...)
	cmd.Dir = string(p.prog.Workingdir)
	cmd.Env = envSlice(p.prog.Env)
	cmd.Stdout = p.stdout
	cmd.Stderr = p.stderr

	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}

func (p *Process) Start() error {
	p.cmd = p.buildCmd()
	if err := p.cmd.Start(); err != nil {
		return err
	}
	return nil
}

// goroutine から呼び出される。
// 当該cmd の終了状態が error に入る
func (p *Process) Wait() (*os.ProcessState, error) {
	if p.cmd == nil {
		return nil, errors.New("cmd not started")
	}
	err := p.cmd.Wait()
	return p.cmd.ProcessState, err
}

func (p *Process) Pid() int {
	if p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

func (p *Process) ID() string { return fmt.Sprintf("%s:%d", p.name, p.index) }

func (p *Process) State() State {
	return p.state
}

func (p *Process) Gen() uint64 {
	return p.gen
}

// to() 以外からは呼ばない
func (p *Process) SetState(s State) {
	p.state = s
}

// start() 以外からは呼ばない
func (p *Process) NextGen() uint64 {
	p.gen++
	return p.gen
}

func (p *Process) Spec() config.Program {
	return p.prog
}

func envSlice(m map[string]string) []string {
	if m == nil {
		return nil
	}
	out := os.Environ()
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

// temporary
func OpenRedirect(path string) (*os.File, error) {
	if path == "" {
		return os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	}
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
}
