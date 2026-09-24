# Taskmaster

設定ファイルに書かれたプログラムを子プロセスとして起動し、生かし続ける監視デーモン
（supervisord 相当）。フォアグラウンドに留まり、制御シェルから状態を見たり start / stop したりできる。

> [!NOTE]
> **現状は起動・再起動・停止まで実装済み。** 設定を読み、`autostart` の program を起動して STARTING → RUNNING → EXITED の遷移を追い、
> `autorestart` / `exitcodes` で上げ直す。`starttime` 内に死んだ起動失敗は BACKOFF を挟んで再試行し、`startretries` を超えると FATAL。
> SIGINT / SIGTERM を受けると全 program を `stopsignal` → `stoptime` 経過で SIGKILL の順に止めてから終了する（どちらもプロセスグループ宛）。
> 制御シェル・SIGHUP での再読み込み（現状は受けて無視）・イベントログのファイル出力・`umask` の適用は未実装。

## ビルドと実行

```sh
make build                                  # _output/bin/taskmasterd
./_output/bin/taskmasterd -c <config.yaml>  # 起動し、SIGINT / SIGTERM で全プロセスを止めるまで前面で待つ（遷移を stdout に表示）
```

| フラグ | 意味 |
|---|---|
| `-c <path>` | 設定ファイル（既定 `taskmasterd.yaml`） |
| `-V` | バージョンを表示して終了 |

`make test` でテスト（`-race`）、`make vet` で `go vet`。

動作確認用の設定は `configs/` にある。

- `configs/taskmasterd.yaml` — `scripts/test.sh` を被験プロセスにした 11 program。`${TM_ROOT}` を使うので、
  `mise` を通さないときは `TM_ROOT=$PWD ./_output/bin/taskmasterd -c configs/taskmasterd.yaml`
- `configs/minimal.yaml` — `./logs/` に書く最小例

## 設定ファイル

YAML で書く。1 ファイルにつき 1 つの YAML ドキュメント（`---` で区切った複数ドキュメントは受け付けない）。

```yaml
programs:
  nginx:
    cmd: "/usr/local/bin/nginx -c /etc/nginx/test.conf"
    numprocs: 1
    autostart: true
    autorestart: unexpected
    exitcodes: [0]
    starttime: 5
    startretries: 3
    stopsignal: TERM
    stoptime: 10
    stdout: /tmp/nginx.stdout
    stderr: /tmp/nginx.stderr
    workingdir: /tmp
    umask: 022
    env:
      STARTED_BY: taskmaster
```

### 項目

| キー | 既定値 | 意味 |
|---|---|---|
| `cmd` | （必須） | 起動コマンド。文字列またはリスト（下記） |
| `numprocs` | `1` | 起動しておくプロセス数（1 以上） |
| `autostart` | `true` | taskmaster の起動時に立ち上げるか |
| `autorestart` | `unexpected` | `always` / `never` / `unexpected` |
| `exitcodes` | `[0]` | 期待される終了コード。スカラーでもリストでもよい |
| `starttime` | `1` | 起動成功とみなすまでの稼働秒数 |
| `startretries` | `3` | 起動失敗時の再試行回数 |
| `stopsignal` | `TERM` | 正常停止に使うシグナル。`TERM` `HUP` `INT` `QUIT` `KILL` `USR1` `USR2`（`SIG` 接頭辞と番号も可） |
| `stoptime` | `10` | `SIGKILL` までの待ち秒数 |
| `stdout` / `stderr` | 破棄 | リダイレクト先。**未指定・空なら `/dev/null` に捨てる** |
| `workingdir` | 継承 | 作業ディレクトリ |
| `umask` | 継承 | 8 進で書ける（`022` は 8 進の 22 として読む）。**現状はパースと範囲検証のみで、子には未適用** |
| `env` | なし | 子プロセスに足す環境変数。taskmaster の環境に追加する（同名キーは設定が勝つ） |

未知のキーはエラーになる。設定ミスは起動前に行番号つきで落とす方針。
program 名（`programs:` のキー）に使えるのは英数字と `_` `.` `-` だけで、先頭に `.` や `-` は使えない。

### `cmd` — 文字列とリスト

```yaml
cmd: "/bin/echo hello world"        # → ["/bin/echo", "hello", "world"]
cmd: ["/bin/echo", "hello world"]   # → ["/bin/echo", "hello world"]（2 要素）
```

- **シェルを介さない。** パイプやリダイレクトが要るなら `cmd: ["sh", "-c", "..."]` と明示的に書く
- 文字列形式は**空白で割るだけ**で、**引用符は解釈しない**。`'` `"` `\` が含まれているとエラーになるので、
  空白を含む 1 個の引数を渡したいときはリスト形式で書く

### 環境変数の展開

`cmd` / `workingdir` / `stdout` / `stderr` の値で `${VAR}` が展開される（`env` の値は展開しない）。

```yaml
cmd: "${APP_ROOT}/bin/server"       # 展開される
cmd: ["sh", "-c", "echo $$HOME"]    # $$ はリテラルの $。子のシェルが展開する
```

- **未定義の変数はエラー**。空文字に化けて意図しないパスを起動するより、起動前に落とす
- 文字列形式では**展開してから空白で分割する**（シェルと同じ順）。展開結果に空白が含まれると
  引数が分かれるので、1 個の引数として渡したいならリスト形式にする
- リスト形式では要素ごとに展開し、**展開結果を分割しない**
