package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	yaml "gopkg.in/yaml.v3"
)

func validateProgramNames(c *Config) error {
	names := make([]string, 0, len(c.Programs))
	for k := range c.Programs {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, n := range names {
		if len(n) == 0 {
			return fmt.Errorf("program name must not be empty: \"%s\"", n)
		}
		switch n[0] {
		case '.', '-':
			return fmt.Errorf("program name %q: must not start with %q", n, rune(n[0]))
		}
		for i, b := range []byte(n) {
			if !(b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' ||
				b == '_' || b == '.' || b == '-') {
				return fmt.Errorf("program name %q: character %q at position %d is not allowed (use letters, digits, '_', '.' or '-')", n, b, i+1)
			}
		}
	}
	return nil
}

func LoadFrom(r io.Reader, name string) (_ *Config, err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("%s: %w", name, err)
		}
	}()

	var cfg *Config
	d := yaml.NewDecoder(r)
	d.KnownFields(true) // 未知のフィールドでエラー
	err = d.Decode(&cfg)
	if err != nil {
		if errors.Is(err, io.EOF) {
			err = errors.New("config file is empty")
		}
		return nil, err
	}
	if cfg == nil {
		return nil, errors.New("config file is empty")
	}
	var extra yaml.Node // "---" 区切りの複数 yaml の存在確認のため
	if err = d.Decode(&extra); !errors.Is(err, io.EOF) {
		if extra.Line > 0 {
			return nil, fmt.Errorf("line %d: config file has multiple YAML documents", extra.Line)
		}
		return nil, errors.New("config file has multiple YAML documents")
	}
	err = validateProgramNames(cfg)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

func Load(path string) (*Config, error) {
	r, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return LoadFrom(r, path)
}
