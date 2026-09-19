package config

import (
	"fmt"
	"strings"
	"syscall"

	yaml "gopkg.in/yaml.v3"
)

type Config struct {
	Programs map[string]Program `yaml:"programs"`
}

type Autorestart int

const (
	Unexpected Autorestart = iota
	Always
	Never
)

var autorestartNames = map[string]Autorestart{
	"always": Always, "never": Never, "unexpected": Unexpected,
}

func (c *Autorestart) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: autorestart must be always, never, or unexpected", n.Line)
	}
	v, ok := autorestartNames[n.Value]
	if !ok {
		return fmt.Errorf("line %d: unknown autorestart %q", n.Line, n.Value)
	}
	*c = v
	return nil
}

type ExitCodes []int

func (c *ExitCodes) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.SequenceNode {
		xs := make([]int, 0, len(n.Content))
		for _, e := range n.Content {
			if e.Kind != yaml.ScalarNode {
				return fmt.Errorf("line %d: exitcodes: list elements must be ints", e.Line)
			}
			var x int
			if err := e.Decode(&x); err != nil {
				return fmt.Errorf("line %d: exitcodes: %q is not an int", e.Line, e.Value)
			}
			xs = append(xs, x)
		}
		*c = xs
		return nil
	}
	// SequenceNode 以外
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: exitcodes must be an int or a list of ints", n.Line)
	}
	var x int
	if err := n.Decode(&x); err != nil {
		return fmt.Errorf("line %d: exitcodes: %q is not an int", n.Line, n.Value)
	}
	*c = ExitCodes{x}
	return nil
}

type Stopsignal syscall.Signal

var availableSignal = map[string]syscall.Signal{
	"TERM": syscall.SIGTERM,
	"HUP":  syscall.SIGHUP,
	"INT":  syscall.SIGINT,
	"QUIT": syscall.SIGQUIT,
	"KILL": syscall.SIGKILL,
	"USR1": syscall.SIGUSR1,
	"USR2": syscall.SIGUSR2,
}

func (c *Stopsignal) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: stopsignal must be a signal name or number", n.Line)
	}
	switch n.Tag {
	case "!!str":
		sig, ok := availableSignal[strings.TrimPrefix(n.Value, "SIG")]
		if !ok {
			return fmt.Errorf("line %d: unknown stopsignal %q", n.Line, n.Value)
		}
		*c = Stopsignal(sig)
		return nil

	case "!!int":
		var x int
		if err := n.Decode(&x); err != nil {
			return fmt.Errorf("line %d: unsupported stopsignal: %s", n.Line, n.Value)
		}
		for _, v := range availableSignal {
			if syscall.Signal(x) == v {
				*c = Stopsignal(x)
				return nil
			}
		}
		return fmt.Errorf("line %d: unsupported stopsignal: %d", n.Line, x)
	default:
		// !!bool / !!float / !!timestamp など。形はスカラーで正しいので、
		// 値のエラーとして扱う。タグ解決は利用者に見えない実装詳細なので、
		// NOPE(!!str) と true(!!bool) で文言が変わるべきではない。
		return fmt.Errorf("line %d: unknown stopsignal %q", n.Line, n.Value)
	}
}

type Program struct {
	Cmd           string            `yaml:"cmd"`
	Numprocs      int               `yaml:"numprocs"`
	Umask         *int              `yaml:"umask"`
	Workingdir    string            `yaml:"workingdir"`
	Autostart     bool              `yaml:"autostart"`
	Autorestart   Autorestart       `yaml:"autorestart"`
	Exitcodes     ExitCodes         `yaml:"exitcodes"`
	Starttretries int               `yaml:"starttretries"`
	Starttime     int               `yaml:"starttime"`
	Stopsignal    Stopsignal        `yaml:"stopsignal"`
	Stoptime      int               `yaml:"stoptime"`
	Stdout        string            `yaml:"stdout"`
	Stderr        string            `yaml:"stderr"`
	Env           map[string]string `yaml:"env"`
}

// 自前の UnmarshalYAML を書いた代償として、その範囲の未知フィールド検出が失われたため
var programFields = map[string]bool{
	"cmd": true, "numprocs": true, "umask": true, "workingdir": true,
	"autostart": true, "autorestart": true, "exitcodes": true,
	"starttretries": true, "starttime": true, "stopsignal": true,
	"stoptime": true, "stdout": true, "stderr": true, "env": true,
}

func (s *Program) UnmarshalYAML(n *yaml.Node) error {
	type plain Program
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: program must be a mapping", n.Line)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := n.Content[i]
		if !programFields[k.Value] {
			return fmt.Errorf("line %d: unknown field %q", k.Line, k.Value)
		}
	}
	p := plain{
		Numprocs:      1,
		Autostart:     true,
		Autorestart:   Unexpected,
		Exitcodes:     ExitCodes{0},
		Starttime:     1,
		Starttretries: 3,
		Stopsignal:    Stopsignal(syscall.SIGTERM),
		Stoptime:      10,
	}
	if err := n.Decode(&p); err != nil {
		return err
	}
	if p.Numprocs <= 0 {
		return fmt.Errorf("line %d: numprocs must be >= 1, got %d", n.Line, p.Numprocs)
	}
	*s = Program(p)
	return nil
}
