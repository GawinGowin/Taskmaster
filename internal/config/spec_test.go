package config_test

import (
	"os"
	"reflect"
	"syscall"
	"taskmaster/internal/config"
	"testing"

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

// intPtr は Umask (*int) の期待値を書くためのヘルパ。&0 と書けないため。
func intPtr(v int) *int { return &v }

func TestProgram_UnmarshalYAML(t *testing.T) {
	// def はデフォルト値を返す。mutate に差分だけ書く（nil ならデフォルトのまま）。
	def := func(mutate func(*config.Program)) config.Program {
		p := config.Program{
			Cmd:           "/bin/true",
			Numprocs:      1,
			Umask:         nil,
			Workingdir:    "",
			Autostart:     true,
			Autorestart:   config.Unexpected,
			Exitcodes:     config.ExitCodes{0},
			Starttretries: 3,
			Starttime:     1,
			Stopsignal:    config.Stopsignal(syscall.SIGTERM),
			Stoptime:      10,
			Stdout:        "",
			Stderr:        "",
			Env:           nil,
		}
		if mutate != nil {
			mutate(&p)
		}
		return p
	}

	tests := []struct {
		name     string
		yamlFile string // testdata/<yamlFile>.yaml
		program  string // Config.Programs から取り出すキー
		want     config.Program
		wantErr  bool
	}{
		// 正常系
		{
			name:     "cmd だけならデフォルト値が入る",
			yamlFile: "valid_minimal", program: "minimal",
			want: def(nil),
		},
		{
			name:     "全フィールド明示でデフォルトが全て上書きされる",
			yamlFile: "valid_full", program: "full",
			want: config.Program{
				Cmd:           "/usr/bin/env sleep 60",
				Numprocs:      4,
				Umask:         intPtr(63), // 0o077
				Workingdir:    "/var/tmp",
				Autostart:     false,
				Autorestart:   config.Always,
				Exitcodes:     config.ExitCodes{0, 2, 3},
				Starttretries: 5,
				Starttime:     7,
				Stopsignal:    config.Stopsignal(syscall.SIGUSR1),
				Stoptime:      20,
				Stdout:        "/tmp/full.stdout",
				Stderr:        "/tmp/full.stderr",
				Env:           map[string]string{"TM_INTERVAL": "2", "TM_NAME": "full"},
			},
		},
		{
			name:     "autostart: false が既定の true を上書きする",
			yamlFile: "valid_autostart_false", program: "p",
			want: def(func(p *config.Program) { p.Autostart = false }),
		},
		{
			name:     "autorestart: always",
			yamlFile: "valid_autorestart_always", program: "p",
			want: def(func(p *config.Program) { p.Autorestart = config.Always }),
		},
		{
			name:     "autorestart: never",
			yamlFile: "valid_autorestart_never", program: "p",
			want: def(func(p *config.Program) { p.Autorestart = config.Never }),
		},
		{
			name:     "autorestart: unexpected（ゼロ値と区別できない点に注意）",
			yamlFile: "valid_autorestart_unexpected", program: "p",
			want: def(nil),
		},
		{
			name:     "exitcodes がスカラーなら単一要素のリストになる",
			yamlFile: "valid_exitcodes_scalar", program: "p",
			want: def(func(p *config.Program) { p.Exitcodes = config.ExitCodes{2} }),
		},
		{
			name:     "exitcodes がリストならそのまま入る",
			yamlFile: "valid_exitcodes_list", program: "p",
			want: def(func(p *config.Program) { p.Exitcodes = config.ExitCodes{0, 2, 70} }),
		},
		{
			name:     "exitcodes: [] は nil でなく空スライスとして通る（仕様は未決）",
			yamlFile: "edge_exitcodes_empty_list", program: "p",
			want: def(func(p *config.Program) { p.Exitcodes = config.ExitCodes{} }),
		},
		{
			name:     "umask 未指定なら nil",
			yamlFile: "edge_umask_absent", program: "p",
			want: def(nil),
		},
		{
			name:     "umask: 0 は nil ではなく 0 を指すポインタ",
			yamlFile: "edge_umask_zero", program: "p",
			want: def(func(p *config.Program) { p.Umask = intPtr(0) }),
		},
		{
			name:     "env は数値や真偽値に見えても string になる",
			yamlFile: "valid_env", program: "p",
			want: def(func(p *config.Program) {
				p.Env = map[string]string{
					"PLAIN": "hello", "NUMERIC": "42", "BOOLISH": "true",
					"QUOTED": "0755", "EMPTY": "",
				}
			}),
		},

		// 異常系
		{name: "numprocs: 0 はエラー", yamlFile: "invalid_numprocs_zero", program: "p", wantErr: true},
		{name: "numprocs が負ならエラー", yamlFile: "invalid_numprocs_negative", program: "p", wantErr: true},
		{name: "未知の autorestart はエラー", yamlFile: "invalid_autorestart_unknown", program: "p", wantErr: true},
		{name: "autorestart は大文字小文字を区別する", yamlFile: "invalid_autorestart_case", program: "p", wantErr: true},
		{name: "autorestart が文字列でなければエラー", yamlFile: "invalid_autorestart_not_string", program: "p", wantErr: true},
		{name: "exitcodes がマッピングならエラー", yamlFile: "invalid_exitcodes_mapping", program: "p", wantErr: true},
		{name: "exitcodes が int にできない文字列ならエラー", yamlFile: "invalid_exitcodes_string", program: "p", wantErr: true},
		{name: "exitcodes がリストだが要素が int でないならエラー", yamlFile: "invalid_exitcodes_list_of_strings", program: "p", wantErr: true},
		{name: "program がマッピングでなければエラー", yamlFile: "invalid_program_not_mapping", program: "p", wantErr: true},
		{name: "YAML 構文エラー", yamlFile: "invalid_syntax", program: "p", wantErr: true},
		{name: "タブインデントはエラー", yamlFile: "invalid_tab_indent", program: "p", wantErr: true},
		{name: "program 内の未知キーはエラー", yamlFile: "edge_unknown_field_in_program", program: "p", want: def(nil), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := os.Open(testdataPath(t, tt.yamlFile))
			if err != nil {
				t.Fatalf("testdata %s: %v", tt.yamlFile, err)
			}
			defer f.Close()

			var cfg config.Config
			d := yaml.NewDecoder(f)
			d.KnownFields(true)
			err = d.Decode(&cfg)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			got, ok := cfg.Programs[tt.program]
			if !ok {
				t.Fatalf("program %q not found in %s.yaml", tt.program, tt.yamlFile)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got = %#v, want = %#v", got, tt.want)
			}
		})
	}
}
