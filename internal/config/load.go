package config

import (
	"errors"
	"fmt"
	"io"
	"os"

	yaml "gopkg.in/yaml.v3"
)

func LoadFrom(r io.Reader, name string) (_ *Config, err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("%s: %w", name, err)
		}
	}()

	var cfg Config
	d := yaml.NewDecoder(r)
	d.KnownFields(true)
	err = d.Decode(&cfg)
	if err != nil {
		if errors.Is(err, io.EOF) {
			err = errors.New("config file is empty")
		}
		return nil, err
	}
	var extra yaml.Node // "---" 区切りの複数 yaml の存在確認のため
	if err = d.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("line %d: config file has multiple YAML documents", extra.Line)
	}
	return &cfg, nil
}

func Load(path string) (*Config, error) {
	r, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return LoadFrom(r, path)
}
