#!/usr/bin/env bash
# Acceptance test for dispatchq (Phases 2-3).
#
# Drives the real dispatchq / dispatchqd binaries against a fake `claude` (no tokens,
# deterministic) and checks each Phase-2 requirement, printing PASS/FAIL with the
# requirement id. Exits non-zero if any check fails.
#
# Usage:  ./scripts/acceptance-phase2.sh
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

export DISPATCHQ_HOME="$WORK/home"
export CQ_FAKE_ARGS="$WORK/args.log"
export CQ_FAKE_STATE="$WORK/calls"
REPO_A="$WORK/repoA"; REPO_B="$WORK/repoB"
mkdir -p "$REPO_A" "$REPO_B"

pass=0; fail=0
check() { # check "<id> description" <expr...>
  local desc="$1"; shift
  if "$@"; then printf '  PASS  %s\n' "$desc"; pass=$((pass + 1))
  else printf '  FAIL  %s\n' "$desc"; fail=$((fail + 1)); fi
}
contains() { grep -q -- "$2" "$1"; }
num_check() { # num_check "desc" <actual> <op> <expected>
  local desc="$1"
  if [ "$2" "$3" "$4" ]; then printf '  PASS  %s\n' "$desc"; pass=$((pass + 1))
  else printf '  FAIL  %s (got %s, expected %s %s)\n' "$desc" "$2" "$3" "$4"; fail=$((fail + 1)); fi
}
lines() { grep -c -- "$2" "$1" 2>/dev/null; :; }
# precedes <file> <earlier> <later>: both patterns occur, and the *last* match of
# <earlier> still comes before the *first* match of <later> — i.e. the file does
# the one thing before the other, whatever the exact wording of either line.
precedes() {
  local a b
  a=$(grep -n -- "$2" "$1" | tail -1 | cut -d: -f1)
  b=$(grep -n -- "$3" "$1" | head -1 | cut -d: -f1)
  [ -n "$a" ] && [ -n "$b" ] && [ "$a" -lt "$b" ]
}

echo "== building binaries =="
go build -o "$WORK/dispatchq"  "$ROOT/cmd/dispatchq"  || exit 2
go build -o "$WORK/dispatchqd" "$ROOT/cmd/dispatchqd" || exit 2
CQ="$WORK/dispatchq"; CQD="$WORK/dispatchqd"

# Fake claude: logs its args, then behaves per $CQ_FAKE_MODE.
FAKE="$WORK/fakebin"; mkdir -p "$FAKE"
cat > "$FAKE/claude" <<'EOF'
#!/bin/sh
# The provider health check asks whether Claude Code is logged in; it always is.
if [ "$1" = auth ]; then echo '{"loggedIn":true,"authMethod":"claude.ai"}'; exit 0; fi
echo "ARGS: $*" >> "$CQ_FAKE_ARGS"
case "${CQ_FAKE_MODE:-ok}" in
  ok) echo '{"type":"result","is_error":false,"result":"ok","session_id":"s-ok"}' ;;
  rl) echo "{\"type\":\"system\",\"subtype\":\"api_retry\",\"error_status\":429,\"error\":\"rate_limit\",\"retry_delay_ms\":${CQ_FAKE_RETRY_MS:-3600000},\"session_id\":\"s-rl\"}"; exit 1 ;;
  auth) echo '{"type":"result","is_error":true,"error":"authentication_failed"}'; exit 1 ;;
  fail) echo '{"type":"result","is_error":true,"result":"boom"}'; exit 2 ;;
  resume-once)
    n=$(cat "$CQ_FAKE_STATE" 2>/dev/null || echo 0); n=$((n + 1)); echo "$n" > "$CQ_FAKE_STATE"
    if [ "$n" -eq 1 ]; then
      echo '{"type":"system","subtype":"api_retry","error_status":429,"retry_delay_ms":2000,"session_id":"s-r"}'; exit 1
    else
      echo '{"type":"result","is_error":false,"result":"ok","session_id":"s-r"}'
    fi ;;
esac
EOF
chmod +x "$FAKE/claude"

# Fake system tools (launchctl/sudo/pmset) log their args instead of touching
# the real system, so Phase 3 install/wake can be checked safely.
for tool in launchctl sudo pmset osascript pkill pkgutil; do
  cat > "$FAKE/$tool" <<EOF
#!/bin/sh
echo "\$*" >> "$WORK/$tool.log"
exit 0
EOF
  chmod +x "$FAKE/$tool"
done
export PATH="$FAKE:$PATH"
# Force dispatchq to use the fake claude (the daemon otherwise auto-detects the
# real one at a common install path, bypassing PATH).
export DISPATCHQ_CLAUDE_BIN="$FAKE/claude"

echo
echo "== Task definition & management =="
"$CQ" add --id a --name "Build A" --prompt "pa" --dir "$REPO_A" --trigger asap >/dev/null
"$CQ" add --id b --name "Build B" --prompt "pb" --dir "$REPO_B" --trigger asap --parallel >/dev/null
check "FA-01 stores prompt/dir/trigger/parallel"     contains "$DISPATCHQ_HOME/config.toml" "$REPO_A"
check "FA-03 multiple tasks, different contexts"     bash -c '[ "$('"$CQ"' list | grep -c Build)" -eq 2 ]'
"$CQ" add --id bad --prompt "x" --dir "$REPO_A" --trigger cron --cron "not-a-cron" >/dev/null 2>&1
check "FA-04/18 invalid cron rejected"               bash -c '! '"$CQ"' list | grep -q "^.*bad"'
"$CQ" disable a >/dev/null
check "FA-17 enable/pause without delete"             bash -c ''"$CQ"' list | grep " a " | grep -q false'
"$CQ" enable a >/dev/null
"$CQ" move b 0 >/dev/null
check "FA-11 manual priority reorder (b to top)"      bash -c '[ "$('"$CQ"' list | awk "NR==2{print \$2}")" = "b" ]'

echo
echo "== Execution (run-now) & result visibility =="
"$CQ" run-now a >/dev/null
check "FA-16 run-now executes"                        contains "$CQ_FAKE_ARGS" "ARGS:"
"$CQ" status | grep -q success
num_check "FA-15 status shows success"                "$?" -eq 0
check "FA-15 per-run log file written"                bash -c 'ls "$DISPATCHQ_HOME"/runs/*.log >/dev/null 2>&1'

echo
echo "== Model & permission flags (per-task / global) =="
: > "$CQ_FAKE_ARGS"
"$CQ" add --id sk --prompt "p" --dir "$REPO_A" --trigger asap --skip-permissions --model claude-haiku-4-5-20251001 >/dev/null
"$CQ" run-now sk >/dev/null
check "FA-30 per-task model override passed"          contains "$CQ_FAKE_ARGS" "--model claude-haiku-4-5-20251001"
check "FA-31 per-task skip-permissions passed"        contains "$CQ_FAKE_ARGS" "--dangerously-skip-permissions"
check "FA-27 headless stream-json invocation"         contains "$CQ_FAKE_ARGS" "--output-format stream-json"

echo
echo "== Scheduler loop: asap / fixed earliest-start =="
export DISPATCHQ_HOME="$WORK/home2"; mkdir -p "$DISPATCHQ_HOME"
FUTURE=$(date -u -v+1H +%Y-%m-%dT%H:%M:%SZ)
PAST=$(date -u -v-1H +%Y-%m-%dT%H:%M:%SZ)
"$CQ" add --id now  --prompt "p" --dir "$REPO_A" --trigger asap >/dev/null
"$CQ" add --id past --prompt "p" --dir "$REPO_A" --trigger fixed --at "$PAST" >/dev/null
"$CQ" add --id soon --prompt "p" --dir "$REPO_A" --trigger fixed --at "$FUTURE" >/dev/null
CQ_FAKE_MODE=ok "$CQD" run --interval 300ms >/dev/null 2>&1 &
DPID=$!; sleep 2; kill -INT $DPID 2>/dev/null; wait $DPID 2>/dev/null
H="$DISPATCHQ_HOME/history.jsonl"
now_n=$(lines "$H" '"task_id":"now"');  past_n=$(lines "$H" '"task_id":"past"'); soon_n=$(lines "$H" '"task_id":"soon"')
num_check "FA-07 asap task ran"                       "${now_n:-0}"  -ge 1
num_check "FA-08 fixed(past) ran"                     "${past_n:-0}" -ge 1
num_check "FA-08 fixed(future) did NOT run"           "${soon_n:-0}" -eq 0

echo
echo "== Reactive limit gate: no spin, then resume =="
# Rate-limit: exactly one attempt while the gate is blocked.
export DISPATCHQ_HOME="$WORK/home3"; mkdir -p "$DISPATCHQ_HOME"
"$CQ" add --id blk --prompt "p" --dir "$REPO_A" --trigger asap >/dev/null
CQ_FAKE_MODE=rl "$CQD" run --interval 200ms >/dev/null 2>&1 &
DPID=$!; sleep 2; kill -INT $DPID 2>/dev/null; wait $DPID 2>/dev/null
H3="$DISPATCHQ_HOME/history.jsonl"
starts=$(grep '"task_id":"blk"' "$H3" 2>/dev/null | grep -c '"status":"running"')
num_check "FA-05/06/37 rate-limit → exactly one attempt (no spin)" "${starts:-0}" -eq 1
check "FA-35 failure/limit visible in status"         bash -c ''"$CQ"' status | grep -q rate_limited_waiting'

# Resume after reset: 1st call 429 (retry 2s), later call succeeds via --resume.
export DISPATCHQ_HOME="$WORK/home4"; mkdir -p "$DISPATCHQ_HOME"; : > "$CQ_FAKE_ARGS"; : > "$CQ_FAKE_STATE"
"$CQ" add --id res --prompt "p" --dir "$REPO_A" --trigger asap >/dev/null
CQ_FAKE_MODE=resume-once "$CQD" run --interval 400ms >/dev/null 2>&1 &
DPID=$!; sleep 5; kill -INT $DPID 2>/dev/null; wait $DPID 2>/dev/null
check "D4 second attempt used --resume"               contains "$CQ_FAKE_ARGS" "--resume"
check "D4 task succeeded after resume"                bash -c ''"$CQ"' status | grep " res " | grep -q success'

# A limit pause must read as a plan, not a hang: the run says when it resumes,
# and cancelling that resume keeps the task from starting again at the reset.
export DISPATCHQ_HOME="$WORK/home4b"; mkdir -p "$DISPATCHQ_HOME"; : > "$CQ_FAKE_ARGS"
API="http://127.0.0.1:8792"
"$CQ" add --id rlc --prompt "p" --dir "$REPO_A" --trigger asap >/dev/null
CQ_FAKE_MODE=rl CQ_FAKE_RETRY_MS=4000 "$CQD" run --interval 300ms --no-wake --addr 127.0.0.1:8792 >/dev/null 2>&1 &
DPID=$!
for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
  case "$(curl -s $API/api/runs)" in *'"resume_pending":true'*) break;; esac
  sleep 0.3
done
runs_json=$(curl -s $API/api/runs); tasks_json=$(curl -s $API/api/tasks); health_json=$(curl -s $API/api/health)
case "$runs_json"   in *'"resume_pending":true'*) r1=0;; *) r1=1;; esac
num_check "paused run is marked as scheduled to resume"  "$r1" -eq 0
case "$runs_json"   in *'"resume_at":'*) r2=0;; *) r2=1;; esac
num_check "paused run carries its resume time"           "$r2" -eq 0
case "$health_json" in *'"limited_until":"2'*) r3=0;; *) r3=1;; esac
num_check "health reports when the gate reopens"         "$r3" -eq 0
case "$tasks_json"  in *'"waiting_for_limit":true'*) r4=0;; *) r4=1;; esac
num_check "queue marks the task as rescheduled"          "$r4" -eq 0

RLC_RUN=$(printf '%s' "$runs_json" | grep -o '"run_id":"[^"]*"' | head -1 | cut -d'"' -f4)
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/api/runs/$RLC_RUN/cancel")
num_check "cancelling the scheduled resume is accepted"  "$code" -eq 204
sleep 5   # past the 4s reset: the gate reopens and nothing may start
runs_json=$(curl -s $API/api/runs); tasks_json=$(curl -s $API/api/tasks)
kill -INT $DPID 2>/dev/null; wait $DPID 2>/dev/null
case "$runs_json"  in *'"status":"canceled"'*) r5=0;; *) r5=1;; esac
num_check "the canceled resume is recorded as canceled"  "$r5" -eq 0
case "$tasks_json" in *'"id":"rlc"'*) r6=1;; *) r6=0;; esac
num_check "the one-shot task left the queue"             "$r6" -eq 0
starts=$(grep -c "^ARGS:" "$CQ_FAKE_ARGS")
num_check "the task did not start again after the reset" "${starts:-0}" -eq 1

echo
echo "== News/unread persistence =="
export DISPATCHQ_HOME="$WORK/home5"; mkdir -p "$DISPATCHQ_HOME"
"$CQ" add --id n --prompt "p" --dir "$REPO_A" --trigger asap >/dev/null
"$CQ" run-now n >/dev/null
check "FA-22 new run is unread"                       bash -c ''"$CQ"' status | grep -q "1 unread"'
"$CQ" read-all >/dev/null
check "FA-24/26 read status persists (new process)"   bash -c ''"$CQ"' status | grep -q "0 unread"'

echo
echo "== Resilience & wake (Phase 3; fake launchctl/sudo/pmset) =="
FAKEHOME="$WORK/fakehome"; mkdir -p "$FAKEHOME"
export DISPATCHQ_HOME="$WORK/home6"; mkdir -p "$DISPATCHQ_HOME"
HOME="$FAKEHOME" "$CQD" install >/dev/null 2>&1
PLIST="$FAKEHOME/Library/LaunchAgents/de.maierdaniel.dispatchq.plist"
check "NFA-03 install writes LaunchAgent plist"       test -f "$PLIST"
check "NFA-03 plist has RunAtLoad (autostart)"        contains "$PLIST" "<key>RunAtLoad</key>"
check "NFA-03 install bootstraps via launchctl"       contains "$WORK/launchctl.log" "bootstrap"

"$CQ" add --id wk --prompt "p" --dir "$REPO_A" --trigger asap >/dev/null
: > "$WORK/sudo.log"
CQ_FAKE_MODE=ok "$CQD" run --interval 300ms >/dev/null 2>&1 &
DPID=$!; sleep 2; kill -INT $DPID 2>/dev/null; wait $DPID 2>/dev/null
check "FA-32 daemon schedules a pmset wake"           contains "$WORK/sudo.log" "pmset schedule wake"

HOME="$FAKEHOME" "$CQD" uninstall >/dev/null 2>&1
check "NFA-03 uninstall removes plist"                bash -c '[ ! -f "'"$PLIST"'" ]'
check "NFA-03 uninstall boots out via launchctl"      contains "$WORK/launchctl.log" "bootout"

echo
echo "== Notifications (Phase 4; fake osascript) =="
export DISPATCHQ_HOME="$WORK/home7"; mkdir -p "$DISPATCHQ_HOME"
"$CQ" add --id failer --prompt "p" --dir "$REPO_A" --trigger asap >/dev/null
: > "$WORK/osascript.log"
CQ_FAKE_MODE=fail "$CQD" run --interval 300ms --no-wake >/dev/null 2>&1 &
DPID=$!; sleep 2; kill -INT $DPID 2>/dev/null; wait $DPID 2>/dev/null
check "FA-35/39 failure triggers a macOS notification" contains "$WORK/osascript.log" "display notification"

echo
echo "== Dashboard API (Phase 5) =="
export DISPATCHQ_HOME="$WORK/home8"; mkdir -p "$DISPATCHQ_HOME"
FUT=$(date -u -v+1H +%Y-%m-%dT%H:%M:%SZ)
"$CQ" add --id web --prompt "p" --dir "$REPO_A" --trigger fixed --at "$FUT" >/dev/null  # stays listed (not due)
"$CQ" add --id ran --prompt "p" --dir "$REPO_A" --trigger asap >/dev/null               # runs then leaves the queue
CQ_FAKE_MODE=ok "$CQD" run --interval 500ms --no-wake --addr 127.0.0.1:8791 >/dev/null 2>&1 &
DPID=$!; sleep 2
tasks_json=$(curl -s http://127.0.0.1:8791/api/tasks)
dash_html=$(curl -s http://127.0.0.1:8791/)
runs_json=$(curl -s http://127.0.0.1:8791/api/runs)
kill -INT $DPID 2>/dev/null; wait $DPID 2>/dev/null
case "$tasks_json" in *'"id":"web"'*) t1=0;; *) t1=1;; esac
num_check "FA-02 API lists tasks (lowercase JSON keys)"  "$t1" -eq 0
case "$tasks_json" in *'"id":"ran"'*) t1b=1;; *) t1b=0;; esac
num_check "finished one-shot task leaves the queue"      "$t1b" -eq 0
case "$dash_html" in *"<title>DispatchQ</title>"*) t2=0;; *) t2=1;; esac
num_check "FA-25 dashboard HTML served"                  "$t2" -eq 0
case "$runs_json" in *'"status":"success"'*) t3=0;; *) t3=1;; esac
num_check "FA-15/22 API exposes run history/status"      "$t3" -eq 0
case "$runs_json" in *'"task":'*) t4=0;; *) t4=1;; esac
num_check "runs carry a replayable task snapshot"        "$t4" -eq 0

echo
echo "== Concurrency (covered by automated tests) =="
if go test "$ROOT/internal/schedule/..." "$ROOT/internal/engine/..." -run 'Parallel|Exclusive|Select' -count=1 >/dev/null 2>&1; then
  printf '  PASS  FA-12/13/14/33 parallelism & priority (go test)\n'; pass=$((pass + 1))
else
  printf '  FAIL  FA-12/13/14/33 parallelism & priority (go test)\n'; fail=$((fail + 1))
fi

echo
echo "== Packaging & release (phase 6) =="
check "NFA-05 installer places the app in /Applications"   contains "$ROOT/scripts/build-pkg.sh" "Applications/DispatchQ.app"
check "NFA-05 installer disables bundle relocation"        contains "$ROOT/scripts/build-pkg.sh" "BundleIsRelocatable false"
check "app bundle is code-signed under its bundle id"      contains "$ROOT/scripts/build-app.sh" "identifier de.maierdaniel.dispatchq"
check "NFA-05 installer runs the postinstall scripts dir"  contains "$ROOT/scripts/build-pkg.sh" "scripts/pkg"
check "NFA-05 postinstall is valid shell"                  sh -n "$ROOT/scripts/pkg/postinstall"
check "NFA-05 postinstall sets up the LaunchAgent"         contains "$ROOT/scripts/pkg/postinstall" '"\$DAEMON" install'
# Regression guard: the postinstall must NEVER delete anything under
# /Applications — a case-insensitive-FS bug once made it rm the app it just
# installed. It may reference the path (to locate the daemon), just never rm it.
check "postinstall never removes anything under /Applications" bash -c '! grep -Eq "(rm|unlink|ditto .*--nocache)[^\\n]*/Applications" "'"$ROOT"'/scripts/pkg/postinstall"'
# ClaudeQ.app is retired by moving it to the Bin, never by deleting it, and only
# once its bundle id says it is ClaudeQ.
check "postinstall never deletes ClaudeQ.app"                 bash -c '! grep -Eq "rm -[a-z]*r[a-z]* .*LEGACY_APP" "'"$ROOT"'/scripts/pkg/postinstall"'
check "postinstall moves ClaudeQ.app to the Bin only by id"   precedes "$ROOT/scripts/pkg/postinstall" 'CFBundleIdentifier.*LEGACY_LABEL' 'mv "\$LEGACY_APP"'
# Old scripts keep calling /Applications/ClaudeQ.app/Contents/MacOS/claudeq:
# the bundle carries a claudeq link, and ClaudeQ.app becomes a link to it only
# where nothing else is. The uninstaller removes that link and nothing behind it.
check "app bundle carries ClaudeQ's CLI name"                 contains "$ROOT/scripts/build-app.sh" 'ln -sf dispatchq "\$MACOS/claudeq"'
check "postinstall links ClaudeQ.app only where nothing is"   contains "$ROOT/scripts/pkg/postinstall" '\[ ! -e "\$LEGACY_APP" \] && \[ ! -L "\$LEGACY_APP" \]'
check "uninstaller removes only a ClaudeQ.app link to DispatchQ" contains "$ROOT/scripts/uninstall/main/postinstall" 'readlink "\$LEGACY_APP"'
# The install is only done once the *new* daemon answers on its own loopback
# port; only then may the app be reopened. Both halves are matched by intent,
# not by literals: the port lives in a PORT= variable, and `open` has to run
# inside the console user's GUI session (as_user), never as root.
check "postinstall polls the daemon on its configured port"          contains "$ROOT/scripts/pkg/postinstall" '127\.0\.0\.1:\$PORT/api/'
check "postinstall defines that port in one place"                   contains "$ROOT/scripts/pkg/postinstall" '^PORT=[0-9]\{1,\}$'
check "postinstall waits for the daemon, then opens the installed app" precedes "$ROOT/scripts/pkg/postinstall" 'settled' 'as_user open "\$APP"'
check "NFA-05 preinstall is valid shell"                   sh -n "$ROOT/scripts/pkg/preinstall"
check "preinstall never removes anything under /Applications" bash -c '! grep -Eq "(rm|unlink|ditto .*--nocache)[^\\n]*/Applications" "'"$ROOT"'/scripts/pkg/preinstall"'
# The preinstall closes only the dashboard window (by exact process name, so a
# `go build` of the app is never hit); the daemon is postinstall's business.
check "preinstall never touches the daemon"                bash -c '! grep -Eq "(pkill|kill|killall)[^\\n]*dispatchqd" "'"$ROOT"'/scripts/pkg/preinstall"'
# Run the real preinstall against fake pgrep/pkill: the window "runs" until the
# fake pkill has been called once, so a TERM that works must never escalate.
rm -f "$WORK/pkill.log"
cat > "$FAKE/pgrep" <<EOF
#!/bin/sh
[ -f "$WORK/pkill.log" ] && exit 1 || exit 0
EOF
chmod +x "$FAKE/pgrep"
sh "$ROOT/scripts/pkg/preinstall" >/dev/null 2>&1
check "preinstall TERMs the window by exact process name"  contains "$WORK/pkill.log" "^-x dispatchqapp$"
check "preinstall does not force-quit a window that exits" bash -c '! grep -q -- "-9" "'"$WORK"'/pkill.log"'

# NFA-06: the uninstaller package. Build it, expand it, and run the scripts it
# really carries against the fakes, with a scratch home, app and sudoers file
# standing in for the real ones (DISPATCHQ_CONSOLE_*/DISPATCHQ_APP/DISPATCHQ_SUDOERS).
U="$WORK/uninst"; UHOME="$U/home"; ULIB="$UHOME/Library"
"$ROOT/scripts/build-uninstall-pkg.sh" "$U/Uninstall DispatchQ.pkg" >/dev/null 2>&1
/usr/sbin/pkgutil --expand "$U/Uninstall DispatchQ.pkg" "$U/x" >/dev/null 2>&1
check "NFA-06 uninstaller package builds and expands"  test -x "$U/x/uninstall.pkg/Scripts/postinstall"
installer -showChoiceChangesXML -pkg "$U/Uninstall DispatchQ.pkg" -target / >"$U/choices.xml" 2>/dev/null
check "NFA-06 data deletion is an opt-in choice"        bash -c 'tr -d "\n\t " < "'"$U"'/choices.xml" | grep -q "<integer>0</integer><key>choiceAttribute</key><string>selected</string><key>choiceIdentifier</key><string>data</string>"'
mk_app() { # mk_app <bundle> <bundle id>
  mkdir -p "$1/Contents/MacOS"
  printf '<?xml version="1.0" encoding="UTF-8"?>\n<plist version="1.0"><dict><key>CFBundleIdentifier</key><string>%s</string></dict></plist>\n' "$2" > "$1/Contents/Info.plist"
}
mk_app "$U/Applications/DispatchQ.app" de.maierdaniel.dispatchq
mk_app "$U/Elsewhere/DispatchQ.app" de.maierdaniel.dispatchq
mk_app "$U/Foreign.app" com.example.foreign
# What ClaudeQ, the name before 2.0, left behind: its app, agent, wake
# permission and the link from its data directory to DispatchQ's.
mk_app "$U/Applications/ClaudeQ.app" de.maierdaniel.claudeq
mkdir -p "$ULIB/LaunchAgents" "$ULIB/Application Support/dispatchq" "$ULIB/WebKit/de.maierdaniel.dispatchq" "$ULIB/Preferences"
printf '<?xml version="1.0" encoding="UTF-8"?>\n<plist version="1.0"><dict><key>ProgramArguments</key><array><string>%s</string><string>run</string></array></dict></plist>\n' \
  "$U/Elsewhere/DispatchQ.app/Contents/MacOS/dispatchqd" > "$ULIB/LaunchAgents/de.maierdaniel.dispatchq.plist"
touch "$ULIB/Application Support/dispatchq/config.toml" "$ULIB/Preferences/de.maierdaniel.dispatchq.plist" "$U/sudoers" "$U/claudeq" \
  "$ULIB/LaunchAgents/de.maierdaniel.claudeq.plist"
ln -s dispatchq "$ULIB/Application Support/claudeq"
: > "$WORK/launchctl.log"; : > "$WORK/pkgutil.log"; rm -f "$WORK/pkill.log"
# Both the window and the daemon "run" until a TERM for them has been sent.
cat > "$FAKE/pgrep" <<EOF
#!/bin/sh
for last; do :; done
grep -q " \$last\$" "$WORK/pkill.log" 2>/dev/null && exit 1 || exit 0
EOF
chmod +x "$FAKE/pgrep"
uenv() { env DISPATCHQ_CONSOLE_USER="$(id -un)" DISPATCHQ_CONSOLE_HOME="$UHOME" DISPATCHQ_APP="$1" DISPATCHQ_SUDOERS="$U/sudoers" sh "$2"; }
uenv "$U/Applications/DispatchQ.app" "$U/x/uninstall.pkg/Scripts/postinstall" >"$U/main.out" 2>&1
num_check "NFA-06 uninstaller exits cleanly"               "$?" -eq 0
check "NFA-06 uninstaller closes the user's window"         contains "$WORK/pkill.log" "^-x -u $(id -u) dispatchqapp$"
check "NFA-06 uninstaller stops the user's daemon"          contains "$WORK/pkill.log" "^-x -u $(id -u) dispatchqd$"
check "NFA-06 a daemon that exits on TERM is not killed"    bash -c '! grep -q -- "-9" "'"$WORK"'/pkill.log"'
check "NFA-06 the agent goes before the daemon is stopped"  precedes "$U/main.out" "removed LaunchAgent" "stopping dispatchqd"
check "NFA-06 uninstaller boots out the LaunchAgent"        contains "$WORK/launchctl.log" "^asuser $(id -u) sudo -u $(id -un) launchctl bootout gui/$(id -u)/de.maierdaniel.dispatchq$"
check "NFA-06 uninstaller removes the LaunchAgent plist"    bash -c '[ ! -e "'"$ULIB"'/LaunchAgents/de.maierdaniel.dispatchq.plist" ]'
check "NFA-06 uninstaller removes the wake permission"      bash -c '[ ! -e "'"$U"'/sudoers" ]'
check "NFA-06 uninstaller forgets the installer receipt"    contains "$WORK/pkgutil.log" "^--forget de.maierdaniel.dispatchq$"
check "NFA-06 uninstaller removes the app"                  bash -c '[ ! -e "'"$U"'/Applications/DispatchQ.app" ]'
check "NFA-06 uninstaller removes the copy the agent ran"   bash -c '[ ! -e "'"$U"'/Elsewhere/DispatchQ.app" ]'
check "NFA-06 uninstaller removes ClaudeQ's app"            bash -c '[ ! -e "'"$U"'/Applications/ClaudeQ.app" ]'
check "NFA-06 uninstaller boots out ClaudeQ's agent"        contains "$WORK/launchctl.log" "launchctl bootout gui/$(id -u)/de.maierdaniel.claudeq$"
check "NFA-06 uninstaller removes ClaudeQ's agent plist"    bash -c '[ ! -e "'"$ULIB"'/LaunchAgents/de.maierdaniel.claudeq.plist" ]'
check "NFA-06 uninstaller removes ClaudeQ's wake permission" bash -c '[ ! -e "'"$U"'/claudeq" ]'
check "NFA-06 uninstaller stops ClaudeQ's daemon"           contains "$WORK/pkill.log" "^-x -u $(id -u) claudeqd$"
check "NFA-06 uninstaller keeps the data by default"        test -f "$ULIB/Application Support/dispatchq/config.toml"
uenv "$U/Foreign.app" "$U/x/uninstall.pkg/Scripts/postinstall" >/dev/null 2>&1
check "NFA-06 uninstaller never removes another app"        test -d "$U/Foreign.app"
uenv "" "$U/x/uninstall-data.pkg/Scripts/postinstall" >"$U/data.out" 2>&1
check "NFA-06 data choice deletes the data directory"       bash -c '[ ! -e "'"$ULIB"'/Application Support/dispatchq" ]'
check "NFA-06 data choice deletes ClaudeQ's data link"      bash -c '[ ! -e "'"$ULIB"'/Application Support/claudeq" ] && [ ! -L "'"$ULIB"'/Application Support/claudeq" ]'
check "NFA-06 data choice deletes the app's Library folders" bash -c '[ ! -e "'"$ULIB"'/WebKit/de.maierdaniel.dispatchq" ] && [ ! -e "'"$ULIB"'/Preferences/de.maierdaniel.dispatchq.plist" ]'
check "NFA-06 data choice clears preferences via cfprefsd"  contains "$WORK/launchctl.log" "asuser $(id -u) sudo -u $(id -un) defaults delete de.maierdaniel.dispatchq"
DISPATCHQ_CONSOLE_USER=root sh "$U/x/uninstall-data.pkg/Scripts/postinstall" >/dev/null 2>&1
num_check "NFA-06 data choice without a console user is a no-op" "$?" -eq 0
check "release pipeline triggers on version tags"          contains "$ROOT/.github/workflows/release.yml" 'tags:'
check "release pipeline attaches the installer pkg"        contains "$ROOT/.github/workflows/release.yml" "dispatchq-.*-macos-.*.pkg"
check "release pipeline attaches the Windows x64 beta"     contains "$ROOT/.github/workflows/release.yml" "dispatchq-.*-windows-x64-beta.exe"
check "release pipeline attaches the Windows ARM64 beta"   contains "$ROOT/.github/workflows/release.yml" "dispatchq-.*-windows-arm64-beta.exe"

echo
echo "=========================================="
printf "Result: %d passed, %d failed\n" "$pass" "$fail"
echo "=========================================="
[ "$fail" -eq 0 ]
