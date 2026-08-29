#!/usr/bin/env bash
#
# test.sh - taskmaster の動作確認用ダミーデーモン
#
# 何もせずログを吐き続けるだけのプロセス。環境変数でふるまいを変えられるので、
# 1 本で taskmaster の各機能を試せる。
#
#   TM_NAME          ログに出す識別名                     (既定: test)
#   TM_START_DELAY   "ready" を出すまでの秒数             (既定: 0)   -> starttime/startsecs
#   TM_INTERVAL      heartbeat の間隔(秒)                 (既定: 1)
#   TM_TICKS         heartbeat 何回で正常終了するか       (既定: 0=無限)
#   TM_EXIT_CODE     正常終了時の終了コード               (既定: 0)   -> exitcodes/autorestart
#   TM_FAIL_AT       この tick で異常終了する             (既定: 0=しない)
#   TM_FAIL_CODE     TM_FAIL_AT での終了コード            (既定: 42)
#   TM_STOP_DELAY    停止シグナル受信後の後始末の秒数     (既定: 0)   -> stopwaitsecs
#   TM_IGNORE_STOP   1 なら停止シグナルを無視する         (既定: 0)   -> stopwaitsecs 後の SIGKILL
#   TM_STDERR_EVERY  N tick ごとに stderr にも書く        (既定: 0=書かない)
#   TM_SPAM          1 tick あたりに追加で吐く行数        (既定: 0)   -> 大量出力
#   TM_PRINT_ENV     起動時に表示する環境変数の正規表現   (既定: ^(TM_|TASKMASTER_))
#
# 使用例:
#   ./scripts/test.sh                                    # 素直に生き続ける
#   TM_START_DELAY=10 ./scripts/test.sh                  # 起動が遅いプロセス
#   TM_TICKS=3 TM_EXIT_CODE=1 ./scripts/test.sh          # すぐ死ぬプロセス
#   TM_IGNORE_STOP=1 ./scripts/test.sh                   # 停止シグナルを無視するプロセス
#   TM_SPAM=200 ./scripts/test.sh                        # ログを溢れさせるプロセス

set -uo pipefail

# --- 設定値の読み込み（数値でなければ既定値にフォールバック） ---------------
num() { # num <値> <既定値>
	if [[ ${1-} =~ ^[0-9]+$ ]]; then printf '%s' "$1"; else printf '%s' "$2"; fi
}

NAME=${TM_NAME:-test}
START_DELAY=$(num "${TM_START_DELAY-}" 0)
INTERVAL=$(num "${TM_INTERVAL-}" 1)
TICKS=$(num "${TM_TICKS-}" 0)
EXIT_CODE=$(num "${TM_EXIT_CODE-}" 0)
FAIL_AT=$(num "${TM_FAIL_AT-}" 0)
FAIL_CODE=$(num "${TM_FAIL_CODE-}" 42)
STOP_DELAY=$(num "${TM_STOP_DELAY-}" 0)
IGNORE_STOP=$(num "${TM_IGNORE_STOP-}" 0)
STDERR_EVERY=$(num "${TM_STDERR_EVERY-}" 0)
SPAM=$(num "${TM_SPAM-}" 0)
PRINT_ENV=${TM_PRINT_ENV:-'^(TM_|TASKMASTER_)'}

# --- ログ -------------------------------------------------------------------
log() { printf '%s [%s:%d] %s\n' "$(date +%Y-%m-%dT%H:%M:%S%z)" "$NAME" "$$" "$*"; }
elog() { log "$@" >&2; }

# --- シグナル ---------------------------------------------------------------
# sleep は子プロセスとして走らせ wait で待つ。こうしないと bash は sleep が
# 終わるまで trap を実行せず、停止シグナルへの反応が INTERVAL 分遅れる。
child=

nap() { # nap <秒>
	local secs=$1
	((secs == 0)) && return 0
	sleep "$secs" &
	child=$!
	wait "$child" 2>/dev/null
	child=
}

on_stop() { # on_stop <シグナル名> <シグナル番号>
	local sig=$1 signo=$2
	if ((IGNORE_STOP)); then
		elog "SIG${sig} を受信したが TM_IGNORE_STOP=1 なので無視する（KILL されるまで生き続ける）"
		return
	fi
	log "SIG${sig} を受信 -> graceful shutdown を開始（後始末に ${STOP_DELAY}s かかる想定）"
	[[ -n $child ]] && kill "$child" 2>/dev/null
	nap "$STOP_DELAY"
	log "graceful shutdown 完了 exit=$((128 + signo))"
	exit $((128 + signo))
}

on_hup() {
	log "SIGHUP を受信（設定リロード相当の処理をしたことにして継続する）"
}

on_exit() {
	[[ -n $child ]] && kill "$child" 2>/dev/null
	return 0
}

trap 'on_stop TERM 15' TERM
trap 'on_stop INT 2' INT
trap 'on_stop QUIT 3' QUIT
trap 'on_stop USR1 10' USR1
trap on_hup HUP
trap on_exit EXIT

# --- 起動 -------------------------------------------------------------------
log "起動 pid=$$ ppid=$PPID uid=$(id -u) gid=$(id -g) umask=$(umask)"
log "cwd=$PWD argv=[$0${*:+ $*}]"
while IFS='=' read -r k v; do
	log "env ${k}=${v}"
done < <(env | grep -E "$PRINT_ENV" | sort)

if ((START_DELAY > 0)); then
	log "初期化中... ${START_DELAY}s（この間に死ぬと startsecs を満たさない）"
	nap "$START_DELAY"
fi
log "ready（ここから正常稼働）interval=${INTERVAL}s ticks=$( ((TICKS > 0)) && echo "$TICKS" || echo "無限" )"

# --- メインループ -----------------------------------------------------------
tick=0
while :; do
	tick=$((tick + 1))
	log "tick=${tick}"

	if ((STDERR_EVERY > 0 && tick % STDERR_EVERY == 0)); then
		elog "tick=${tick} これは stderr への出力"
	fi

	for ((i = 1; i <= SPAM; i++)); do
		log "tick=${tick} spam=${i}/${SPAM} $(head -c 64 /dev/urandom | base64 | tr -d '\n')"
	done

	if ((FAIL_AT > 0 && tick >= FAIL_AT)); then
		elog "tick=${tick} で異常終了する exit=${FAIL_CODE}"
		exit "$FAIL_CODE"
	fi

	if ((TICKS > 0 && tick >= TICKS)); then
		log "${TICKS} 回の tick を終えたので終了する exit=${EXIT_CODE}"
		exit "$EXIT_CODE"
	fi

	nap "$INTERVAL"
done
