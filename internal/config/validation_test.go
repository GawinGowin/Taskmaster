package config_test

import (
	"strings"
	"testing"
)

// このファイルは Program のバリデーション（デコードできたが値がおかしい入力）。
// 型として読めるかどうかは spec_test.go / stopsignal_test.go の担当で、
// ここは「読めてしまうが監視を始めたら壊れる値」を起動前に落とすこと。

func TestProgram_Validation(t *testing.T) {
	tests := []struct {
		name        string
		yamlFile    string
		errContains string
	}{
		{
			name:     "cmd が無ければエラー",
			yamlFile: "invalid_cmd_missing", errContains: "cmd is required",
		},
		{
			// 未指定と同じ文面。分けない判断をここで固定する。
			name:     "cmd が空文字でもエラー",
			yamlFile: "invalid_cmd_empty", errContains: "cmd is required",
		},
		{
			name:     "starttime が負ならエラー",
			yamlFile: "invalid_starttime_negative", errContains: "starttime must be >= 0, got -5",
		},
		{
			name:     "stoptime が負ならエラー",
			yamlFile: "invalid_stoptime_negative", errContains: "stoptime must be >= 0, got -1",
		},
		{
			name:     "starttretries が負ならエラー",
			yamlFile: "invalid_starttretries_negative", errContains: "starttretries must be >= 0, got -2",
		},
		{
			// 10 進で書いた人には 10 進が、8 進で書いた人には 8 進が文面に出ること。
			// int になった時点で元の表記は失われるので、両方出す以外に方法がない。
			name:     "umask が範囲外ならエラー（10 進で書いた場合）",
			yamlFile: "invalid_umask_out_of_range", errContains: "umask must be between 0o000 and 0o777, got 999 (0o1747)",
		},
		{
			name:     "umask が範囲外ならエラー（8 進で書いた場合）",
			yamlFile: "invalid_umask_octal_out_of_range", errContains: "got 512 (0o1000)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := loadFile(t, tt.yamlFile)
			logErr(t, err)
			if err == nil {
				t.Fatalf("エラーにならなかった。got = %#v", got)
			}
			if !strings.Contains(err.Error(), tt.errContains) {
				t.Errorf("エラーに %q が含まれていない: %v", tt.errContains, err)
			}
			if !strings.Contains(err.Error(), "line ") {
				t.Errorf("エラーに行番号が無い: %v", err)
			}
		})
	}
}

// TestProgram_ValidationBoundaries は「通らなければいけない値」。
// 検証を `> 0` や `< 0o777` と書き間違えると、異常系のテストは緑のままここだけ赤くなる。
func TestProgram_ValidationBoundaries(t *testing.T) {
	t.Run("0 と 0o777 は通る", func(t *testing.T) {
		cfg, err := loadFile(t, "valid_boundaries")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		p := cfg.Programs["p"]
		if p.Starttime != 0 || p.Stoptime != 0 || p.Starttretries != 0 {
			t.Errorf("0 が通っていない: %#v", p)
		}
		if p.Umask == nil {
			t.Fatal("umask = nil, want 0o777")
		}
		if *p.Umask != 0o777 {
			t.Errorf("umask = %d (%O), want 511 (0o777)", *p.Umask, *p.Umask)
		}
	})

	// Umask が *int なのは「未指定」と「0 と書かれた」を区別するため。
	// nil のまま範囲検査に入ると nil ポインタ参照で panic する（実際に踏んだ）。
	t.Run("umask 未指定は nil のまま通る", func(t *testing.T) {
		cfg, err := loadFile(t, "edge_umask_absent")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if u := cfg.Programs["p"].Umask; u != nil {
			t.Errorf("umask = %d, want nil", *u)
		}
	})

	t.Run("umask: 0 は nil ではなく 0", func(t *testing.T) {
		cfg, err := loadFile(t, "edge_umask_zero")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		u := cfg.Programs["p"].Umask
		if u == nil {
			t.Fatal("umask = nil, want 0")
		}
		if *u != 0 {
			t.Errorf("umask = %d, want 0", *u)
		}
	})

	// 先頭ゼロは YAML 1.1 の 8 進。022 も 0o022 も 18 になる。
	// この検証で捕まらない罠として、umask: 22 は 10 進の 22（= 0o26）として
	// 範囲内で通ってしまう点に注意（書き手の意図は 0o22 のはず）。
	t.Run("022 と 0o022 はどちらも 18", func(t *testing.T) {
		cfg, err := loadFile(t, "edge_umask_octal")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		for _, name := range []string{"leading_zero", "explicit_octal"} {
			u := cfg.Programs[name].Umask
			if u == nil {
				t.Fatalf("%s: umask = nil", name)
			}
			if *u != 0o022 {
				t.Errorf("%s: umask = %d (%O), want 18 (0o22)", name, *u, *u)
			}
		}
	})
}

// TestConfig_EmptyPrograms は「program が 0 件の設定を許す」ことの固定。
// 全部コメントアウトして監視を止めたい、という使い方があるため。
// 空ファイル（0 バイト / --- だけ）はエラーのままで、そちらとは別扱い。
func TestConfig_EmptyPrograms(t *testing.T) {
	t.Run("programs: に値を書かない（nil map）", func(t *testing.T) {
		cfg, err := loadFile(t, "edge_programs_null")
		logErr(t, err)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if cfg.Programs != nil {
			t.Errorf("Programs = %#v, want nil", cfg.Programs)
		}
	})

	// nil map と空 map は reflect.DeepEqual で別物になる。
	// reload の同値判定がこの 2 つを「変更あり」と見ないように、両方を記録しておく。
	t.Run("programs: {}（非 nil の空 map）", func(t *testing.T) {
		cfg, err := loadFile(t, "edge_programs_empty_map")
		logErr(t, err)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if cfg.Programs == nil {
			t.Fatal("Programs = nil, want 非 nil の空 map")
		}
		if len(cfg.Programs) != 0 {
			t.Errorf("len(Programs) = %d, want 0", len(cfg.Programs))
		}
	})
}
