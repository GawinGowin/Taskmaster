package config

import (
	"errors"
	"fmt"
	"io"
	"os"

	yaml "gopkg.in/yaml.v3"
)

func LoadFrom(r io.Reader, name string) (*Config, error) {
	var cfg Config

	d := yaml.NewDecoder(r)
	d.KnownFields(true)
	if err := d.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s: %s", name, "config file is empty")
		}
		return nil, fmt.Errorf("%s: %w", name, err)
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
