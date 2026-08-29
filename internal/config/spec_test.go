package config_test

import (
	"reflect"
	"testing"

	"taskmaster/internal/config"

	yaml "gopkg.in/yaml.v3"
)

func TestExitCodes_UnmarshalYAML(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		want    config.ExitCodes
		wantErr bool
	}{
		{name: "スカラーは単一要素のリストになる", yaml: "0", want: config.ExitCodes{0}},
		{name: "複数要素のリスト", yaml: "[0, 2]", want: config.ExitCodes{0, 2}},
		{name: "マッピング", yaml: "{a: 1}", wantErr: true},
		{name: "int 変換不可能", yaml: "abc", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got config.ExitCodes
			err := yaml.Unmarshal([]byte(tt.yaml), &got)

			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got = %#v, want = %#v", got, tt.want)
			}
		})
	}

}
