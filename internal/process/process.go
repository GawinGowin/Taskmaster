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
	cmd := exec.Command(p.Cmd[0], p.Cmd[1:]...)
	cmd.Dir = string(p.Workingdir)
	cmd.Env = envSlice(p.Env)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

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

// goroutine から呼び出される。
// _ = cmd.Wait() のように呼び出す。
// 当該cmd の終了状態が error に入るためこれを失敗扱いしない。
func (p *Process) Wait() (*os.ProcessState, error) {
	err := p.cmd.Wait()
	return p.cmd.ProcessState, err
}

func (p *Process) Pid() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
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

// temporary
func OpenRedirect(path string) (*os.File, error) {
	if path == "" {
		return os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	}
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
}
