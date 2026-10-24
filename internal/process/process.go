package process

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"taskmaster/internal/config"
)

const SpawnMarker = "__spawn"

// 今動いているバイナリそのものを指す。os.Executable() などでパスに直すと、
// 実行中にバイナリが置き換えられたとき新しい方が起動してしまう。
var spawnHelper = "/proc/self/exe"

type Process struct {
	prog    config.Program
	proc    *os.Process // ループ専用。exec.Cmd は watcher だけが持つ
	index   int
	name    string
	stdout  *Sink // nil なら破棄
	stderr  *Sink // nil なら破棄
	state   State
	gen     uint64
	retries int
}

func New(p *config.Program, name string, index int, stdout *Sink, stderr *Sink) (*Process, error) {
	if len(p.Cmd) == 0 {
		return nil, fmt.Errorf("%s:%d: cmd must not be empty", name, index)
	}

	return &Process{
		prog:   *p,
		proc:   nil, // Start() にて設定する
		index:  index,
		name:   name,
		stdout: stdout,
		stderr: stderr,
		state:  Stopped,
		gen:    0,
	}, nil
}

func IsSpawned(args []string) bool {
	return len(args) >= 6 && args[1] == SpawnMarker && args[4] == "--"
}

func RunSpawn(args []string) (int, error) {
	mask, err := strconv.ParseUint(args[2], 8, 32)
	if err != nil {
		return 125, fmt.Errorf("%s: %w", SpawnMarker, err)
	}
	syscall.Umask(int(mask))
	path := args[3]
	argv := args[5:]
	env := os.Environ()
	err = syscall.Exec(path, argv, env)
	code := 126
	if err == syscall.ENOENT {
		code = 127
	}
	return code, fmt.Errorf("%s: %w", SpawnMarker, &os.PathError{Op: "exec", Path: path, Err: err})
}

func (p *Process) buildCmd() (cmd *exec.Cmd) {
	cmd = exec.Command(p.prog.Cmd[0], p.prog.Cmd[1:]...)

	// umask 指定時は自分自身を踏み台に再 exec する: <argv0> __spawn <8進umask> <解決済みパス> -- <元の argv...>
	// cmd を作り直さないのは、LookPath の失敗（cmd.Err）を残して fork 前に失敗させるため。
	m := p.prog.Umask
	if m != nil {
		path := cmd.Path
		args := cmd.Args
		cmd.Path = spawnHelper
		cmd.Args = []string{
			os.Args[0], SpawnMarker, strconv.FormatInt(int64(*m), 8), path, "--",
		}
		cmd.Args = append(cmd.Args, args...)
	}
	cmd.Dir = string(p.prog.Workingdir)
	cmd.Env = envSlice(p.prog.Env)

	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}

// 戻り値の wait は watcher ゴルーチンから 1 回だけ呼ぶ。
// exec.Cmd は wait の中にだけ閉じ込め、p には *os.Process しか残さない。
// cmd.Wait() が書き込む cmd.ProcessState をループ側から読めないようにするため。
func (p *Process) Start() (wait func() (*os.ProcessState, error), err error) {
	cmd := p.buildCmd()

	var w1, r1 *os.File
	var w2, r2 *os.File
	if p.stdout != nil {
		r1, w1, err = os.Pipe()

		if err != nil {
			return nil, fmt.Errorf("stdout pipe: %w", err)
		}

		cmd.Stdout = w1
	}
	if p.stderr != nil {
		r2, w2, err = os.Pipe()

		if err != nil {
			w1.Close()
			r1.Close()
			return nil, fmt.Errorf("stderr pipe: %w", err)
		}

		cmd.Stderr = w2
	}
	err = cmd.Start()
	w1.Close()
	w2.Close()
	if err != nil {
		r1.Close()
		r2.Close()
		p.proc = nil
		return nil, err
	}

	p.proc = cmd.Process
	if r1 != nil {
		go pump(r1, p.stdout)
	}
	if r2 != nil {
		go pump(r2, p.stderr)
	}

	return func() (*os.ProcessState, error) {
		err := cmd.Wait()
		return cmd.ProcessState, err
	}, nil
}

func (p *Process) Pid() int {
	if p.proc == nil {
		return 0
	}
	return p.proc.Pid
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

// start() からのみ呼ばれる
func (p *Process) NextGen() uint64 {
	p.gen++
	return p.gen
}

func (p *Process) Retries() int {
	return p.retries
}

func (p *Process) NextRetries() int {
	p.retries++
	return p.retries
}

func (p *Process) ResetRetries() int {
	p.retries = 0
	return p.retries
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

func OpenSink(path string) (*Sink, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	sink, err := NewSink(f)
	if err != nil {
		return nil, err
	}
	return sink, nil
}
