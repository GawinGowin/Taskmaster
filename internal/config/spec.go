package config

import (
	"fmt"

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
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, ok := autorestartNames[s]
	if !ok {
		return fmt.Errorf("line %d: unknown autorestart %q", n.Line, s)
	}
	*c = v
	return nil
}

type ExitCodes []int

func (c *ExitCodes) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.SequenceNode {
		var xs []int
		if err := n.Decode(&xs); err != nil {
			return fmt.Errorf("line %d: exitcodes: %w", n.Line, err)
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
		return fmt.Errorf("line %d: exitcodes: %w", n.Line, err)
	}
	*c = ExitCodes{x}
	return nil
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
	Stopsignal    string            `yaml:"stopsignal"`
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
		Stopsignal:    "TERM",
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
