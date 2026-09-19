package config

import (
	"io"
	"os"
	"fmt"

	yaml "gopkg.in/yaml.v3"
)

func LoadFrom(r io.Reader, name string) (*Config, error) {
	var m Config

	d := yaml.NewDecoder(r)
	d.KnownFields(true)
	if err := d.Decode(&m); err != nil {
		return nil,  fmt.Errorf("%s: %w", name, err)
	}
	return &m, nil
}

func Load(path string) (*Config, error) {
	r, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return LoadFrom(r, path)
}
