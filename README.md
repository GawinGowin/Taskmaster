# Taskmaster

設定ファイルに書かれたプログラムを子プロセスとして起動し、生かし続ける監視デーモン
（supervisord 相当）。フォアグラウンドに留まり、制御シェルから状態を見たり start / stop したりできる。

> [!NOTE]
> **現状は起動・再起動・停止とイベントログまで実装済み。** 設定を読み、`autostart` の program を起動して STARTING → RUNNING → EXITED の遷移を追い、
> `autorestart` / `exitcodes` で上げ直す。`starttime` 内に死んだ起動失敗は BACKOFF を挟んで再試行し、`startretries` を超えると FATAL。
> SIGINT / SIGTERM を受けると全 program を `stopsignal` → `stoptime` 経過で SIGKILL の順に止めてから終了する（どちらもプロセスグループ宛）。
> 遷移・起動・SIGKILL・受けたシグナルは[イベントログ](#イベントログ)のファイルに 1 行ずつ追記する。
> 制御シェル・SIGHUP での再読み込み（現状は受けてログに残すだけ）・`umask` の適用は未実装。

## ビルドと実行

```sh
make build                                  # _output/bin/taskmasterd
./_output/bin/taskmasterd -c <config.yaml>  # 起動し、SIGINT / SIGTERM で全プロセスを止めるまで前面で待つ（何が起きたかは taskmasterd.log に出る）
```

| フラグ | 意味 |
|---|---|
| `-c <path>` | 設定ファイル（既定 `taskmasterd.yaml`） |
| `-l <path>` | イベントログのファイル（既定 `taskmasterd.log`、カレントディレクトリ）。追記で開き、開けなければ子を 1 つも起動せずに終了する |
| `-V` | バージョンを表示して終了 |

`make test` でテスト（`-race`）、`make vet` で `go vet`。

動作確認用の設定は `configs/` にある。

- `configs/taskmasterd.yaml` — `scripts/test.sh` を被験プロセスにした 11 program。`${TM_ROOT}` を使うので、
  `mise` を通さないときは `TM_ROOT=$PWD ./_output/bin/taskmasterd -c configs/taskmasterd.yaml`
- `configs/minimal.yaml` — `./logs/` に書く最小例

## イベントログ

taskmasterd が「何が起きたか」を `-l` のファイルに 1 行ずつ追記する。標準出力には出さないので、別の端末で `tail -f` して見る。
形式は `log/slog` の Text で、`msg` は固定の語、データは全部 `key=value`。

```
time=2026-10-23T02:26:09.826+09:00 level=INFO msg=signal signal=2 action=shutdown
time=2026-10-23T02:26:09.826+09:00 level=INFO msg=transition id=stubborn:0 from=RUNNING to=STOPPING gen=1 pid=434706 stopsignal=15 stoptime=5 by=shutdown
time=2026-10-23T02:26:14.827+09:00 level=WARN msg=kill id=stubborn:0 gen=1 pid=434706 signal=9 stoptime=5
time=2026-10-23T02:26:14.827+09:00 level=INFO msg=transition id=stubborn:0 from=STOPPING to=STOPPED gen=1 pid=434706 signal=9
```

```sh
tail -f taskmasterd.log                                      # 別端末で流し見る
grep -E 'level=(WARN|ERROR)' taskmasterd.log                 # うまくいっていない行だけ
tac taskmasterd.log | sed '/msg=startup/q' | tac             # 今回の起動ぶんだけ（追記なので前回分も残る）
```

> [!IMPORTANT]
> **イベントログは、設定の `stdout` / `stderr`（子の出力のリダイレクト先）とは別物。**
> 子の出力ファイルは子が書き、taskmasterd は 1 バイトも書かない。逆にイベントログに子の出力は入らない。
> 2 つをつなぐ鍵は pid（`msg=spawn` の行が pid と `name:index` の対応を持つ）。

`msg` の語彙・属性・レベルの付け方は wiki の[イベントログ](https://github.com/GawinGowin/Taskmaster/wiki/Event-Log)にある。

## プロセスツリーを見る

taskmasterd が起動した子と、その子が作った孫を木の形で見る。

```sh
pstree -pc $(pgrep -x taskmasterd)                  # taskmasterd の配下だけ。-c が無いと同じ枝がまとめて表示され、1 本ずつ数えられない
ps -eo pid,ppid,pgid,sid,stat,cmd --forest          # 列を選べる。pgid / sid でプロセスグループの境界を確かめる
ps -ef --forest                                     # 全体を罫線つきで（ps auxf も同じ）
ps -o pid,ppid,pgid,cmd $(pgrep -g <pgid>)          # 子 1 本の一族（停止シグナルが届く範囲）
grep -E 'PPid|Umask' /proc/<pid>/status             # 1 本の親と umask。子の自己申告に頼らずに確かめる
```

> [!NOTE]
> **セッションやプロセスグループでは、taskmasterd の配下全体を絞り込めない。**
> taskmasterd は `setsid` しないので、`ps -s <sid>` では起動した端末のセッション全体が出る。
> 子はそれぞれ `Setpgid` で自分のプロセスグループのリーダーになるので、`pgrep -g <pgid>` で見えるのは子 1 本とその孫だけ
> （停止シグナルをグループ宛に送るための作り。孫まで一族ごと止まる）。
> 配下全体を見るなら、taskmasterd の pid を起点に `pstree` を使う。

> [!WARNING]
> `ps -g` はプロセスグループでは絞れない（procps ではセッションか実効グループ名で選ぶ）。プロセスグループで選ぶのは `pgrep -g`。

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
