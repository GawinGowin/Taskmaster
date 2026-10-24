# Taskmaster

設定ファイルに書かれたプログラムを子プロセスとして起動し、生かし続ける監視デーモン
（supervisord 相当）。フォアグラウンドに留まり、制御シェルから状態を見たり start / stop したりできる。

> [!NOTE]
> **現状は起動・再起動・停止とイベントログまで実装済み。** 設定を読み、`autostart` の program を起動して STARTING → RUNNING → EXITED の遷移を追い、
> `autorestart` / `exitcodes` で上げ直す。`starttime` 内に死んだ起動失敗は BACKOFF を挟んで再試行し、`startretries` を超えると FATAL。
> SIGINT / SIGTERM を受けると全 program を `stopsignal` → `stoptime` 経過で SIGKILL の順に止めてから終了する（どちらもプロセスグループ宛）。
> 遷移・起動・SIGKILL・受けたシグナルは[イベントログ](#イベントログ)のファイルに 1 行ずつ追記する。`umask` は子にだけ効く。
> 制御シェル・SIGHUP での再読み込み（現状は受けてログに残すだけ）は未実装。

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

taskmasterd が起動した子と、その子が作った孫を木の形で見る。基本形は次の 1 行。

```sh
pstree -pgT $(pgrep -x taskmasterd)
```

```
taskmasterd(631136,631136)-+-bash(631147,631147)---sleep(758419,631147)
                           |-bash(631149,631149)---sleep(758445,631149)
                           |-bash(631159,631159)
```

括弧の中は `(pid,pgid)`。木の段がそのまま役割になる。

| 段 | 何か | 見分け方 |
|---|---|---|
| 根 | taskmasterd 本体 | `pgrep -x taskmasterd` の pid |
| 1 段目 | program のプロセス。`numprocs` 分が 1 本ずつ並ぶ（`healthy:0` `healthy:1` …） | **pid と pgid が同じ**（`Setpgid` で自分のグループのリーダーになっている） |
| 2 段目以降 | 孫。program が自分で作ったもので、taskmasterd は直接は知らない | pgid が 1 段目の pid と同じ = その一族。停止シグナルはグループ宛なので孫まで届く |
| `{taskmasterd}` | Go ランタイムのスレッド（プロセスではない） | `-T` を外すと子と並んで出る |

| オプション | 効果 |
|---|---|
| `-p` | pid を出す |
| `-c` | 同じ形の枝をまとめない。`-p` / `-g` を付ければ自動で効く。pid なしで見る（`pstree -T`）と `numprocs` 分が `4*[bash---sleep]` のように畳まれるので、そのときは足す |
| `-g` | pgid を pid の隣に出す。グループの境界（停止シグナルが届く範囲）が見える |
| `-T` | スレッドを隠す |
| `-a` | 引数も出す（木は縦長の書式になる）。どの設定・どの `-l` で動いているか、子の `cmd` が分かる |
| `-H <pid>` | その pid までの枝を太字で強調する。イベントログで見た pid を木の中で探すとき |
| `-C age` | 起動してからの経過で色分けする（緑 1 分未満・黄 1 時間未満・赤それ以上）。再起動を繰り返している子が緑で目立つ |

> [!NOTE]
> **木には `name:index`（`healthy:0`）は出ない。** taskmasterd は子に名前も環境変数も足さないので、
> `cmd` が同じ program どうしは木の上では区別できない。pid から `name:index` を引くのはイベントログの仕事で、
> `grep -w pid=<pid> taskmasterd.log` で `msg=spawn id=healthy:0 gen=1 pid=<pid>` の行に当たる。
> 設定の `env` で program ごとに違う値を渡しているなら、`ps e -o pid,args -p <pid>` でも見分けられる
> （`scripts/test.sh` は `TM_NAME=healthy` などを渡している）。

`ps` で見るとき:

```sh
ps -o pid,pgid,etime,stat,args --ppid $(pgrep -x taskmasterd)   # 1 段目だけ。etime が短い子は最近（再）起動した
ps -o pid,pgid,etime,args --forest -s $(ps -o sid= -p $(pgrep -x taskmasterd))   # 端末のセッションごと木で
ps -o pid,ppid,pgid,args $(pgrep -g <pgid>)                     # 子 1 本の一族（停止シグナルが届く範囲）
grep -E 'PPid|Umask' /proc/<pid>/status                         # 1 本の親と umask。子の自己申告に頼らずに確かめる
```

> [!NOTE]
> **セッションやプロセスグループでは、taskmasterd の配下だけを絞り込めない。**
> taskmasterd は `setsid` しないので、`ps -s <sid>` では起動した端末のセッション全体が出る。
> 子はそれぞれ自分のプロセスグループを持つので、`pgrep -g <pgid>` で見えるのは子 1 本とその孫だけ。
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
| `umask` | 継承 | 8 進で書ける（`022` は 8 進の 22 として読む）。子にだけ効き、taskmasterd 自身の umask は変えない（下記） |
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

### `umask` — 子にだけ効かせる

`umask` を書いた program は、taskmasterd が**自分自身をもう一度 exec した「踏み台」**を経由して起動する。
踏み台は umask を設定してから本来のコマンドを exec するだけで、pid・作業ディレクトリ・出力先・プロセスグループはそのまま引き継がれる
（`ps` の子の親は taskmasterd のまま。中間プロセスは残らない）。`umask` を書かなければ踏み台を通らず、taskmasterd の umask を継承する。

コマンドを `PATH` から探すのは踏み台ではなく taskmasterd 側なので、見つからないときは `umask` の有無にかかわらず起動前に失敗する
（イベントログの `err=` に `executable file not found in $PATH`）。踏み台が exec に失敗したときは、`env(1)` と同じ終了コードで終わる。

| `exitcode=` | 意味 |
|---|---|
| `125` | 踏み台自身の失敗（umask を読めなかった） |
| `126` | 見つかったが起動できない（実行権限が無い、ディレクトリ、実行形式でない） |
| `127` | 探したあとでファイルが消えた、または shebang のインタプリタが無い |

理由は子の `stderr` に `error: __spawn: exec <パス>: <理由>` の 1 行で残る（`stderr` 未指定なら捨てられる）。
