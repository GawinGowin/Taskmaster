package process

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"taskmaster/internal/config"
)

type Process struct {
	prog  config.Program
	cmd   *exec.Cmd
	index int
	name  string
}

func New(name string, index int, p *config.Program) (*Process, error) {
	if len(p.Cmd) == 0 {
		return nil, fmt.Errorf("%s:%d: cmd must not be empty", name, index)
	}
	cmd := exec.Command(p.Cmd[0], p.Cmd[1:]...)
	cmd.Dir = string(p.Workingdir)
	cmd.Env = envSlice(p.Env)

	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	return &Process{
		prog:  *p,
		cmd:   cmd,
		index: index,
		name:  name,
	}, nil
}

func (p *Process) Start() error {
	if err := p.cmd.Start(); err != nil {
		return err
	}
	return nil
}

func (p *Process) ID() string { return fmt.Sprintf("%s:%d", p.name, p.index) }

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
