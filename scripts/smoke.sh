#!/usr/bin/env bash
# Post-deploy smoke check for the WhatsApp bridge and webhook UI.
#
# Why this exists: "Built" and "Up/healthy" both lie after a redeploy that started a stale
# container instead of replacing it, which has happened twice in this project
# (AUDIO_DOWNLOAD_PLAN.md §2.10, §2.15). Every check below reads the RUNNING system.
#
# Usage:
#   scripts/smoke.sh                       # expects the schema version the repo is at
#   EXPECT_SCHEMA=3 scripts/smoke.sh
#   BRIDGE=http://host:8080 UI=http://host:8089 scripts/smoke.sh
#
# Exits non-zero on the first failed check. Needs only curl and python3.

set -uo pipefail

BRIDGE="${BRIDGE:-http://localhost:8080}"
UI="${UI:-http://localhost:8089}"
# Bump this when a migration ships; it is the deploy's proof of life.
EXPECT_SCHEMA="${EXPECT_SCHEMA:-3}"

pass=0
fail=0

ok()   { printf '  \033[32m✓\033[0m %s\n' "$1"; pass=$((pass + 1)); }
bad()  { printf '  \033[31m✗\033[0m %s\n' "$1"; fail=$((fail + 1)); }
note() { printf '\n\033[1m%s\033[0m\n' "$1"; }

# get <url> sets $STATUS and $BODY. Deliberately not a command substitution: that runs in a
# subshell, so an assignment to STATUS inside it would never reach the caller.
STATUS=000
BODY=""
get() {
  local out
  out="$(curl -sS -m 10 -w $'\n%{http_code}' "$1" 2>/dev/null)" || { STATUS=000; BODY=""; return 1; }
  STATUS="${out##*$'\n'}"
  BODY="${out%$'\n'*}"
}

# json <expression> reads stdin as JSON and prints the expression, or FAIL
json() {
  python3 -c "
import json, sys
try:
    d = json.load(sys.stdin)
    print($1)
except Exception:
    print('FAIL')
" 2>/dev/null
}

note "Bridge — $BRIDGE"

get "$BRIDGE/api/health"; health="$BODY"
if [ "$STATUS" = "200" ]; then
  ok "/api/health answers"

  schema="$(printf '%s' "$health" | json "d['schema_version']")"
  if [ "$schema" = "$EXPECT_SCHEMA" ]; then
    ok "schema version is $schema (the deploy took effect)"
  else
    bad "schema version is '$schema', expected $EXPECT_SCHEMA — a stale container is probably still running"
  fi

  connected="$(printf '%s' "$health" | json "str(d['whatsapp_connected']).lower()")"
  [ "$connected" = "true" ] && ok "connected to WhatsApp" || bad "not connected to WhatsApp"

  pool="$(printf '%s' "$health" | json "str(d['auto_download'].get('enabled')).lower()")"
  if [ "$pool" = "true" ]; then
    depth="$(printf '%s' "$health" | json "d['auto_download']['queue_depth']")"
    ok "auto-download pool running (queue depth $depth)"
  else
    ok "auto-download disabled (AUTO_DOWNLOAD=off)"
  fi
else
  bad "/api/health returned $STATUS"
fi

get "$BRIDGE/api/webhooks"; webhooks="$BODY"
if [ "$STATUS" = "200" ]; then
  ok "/api/webhooks answers"
  # Every listener must carry the schema-v2 fields, or the bridge predates them.
  missing="$(printf '%s' "$webhooks" | json "sum(1 for c in (d.get('data') or []) if 'auto_download' not in c or 'auto_download_voice_only' not in c)")"
  if [ "$missing" = "0" ]; then
    ok "every listener carries the auto-download fields"
  else
    bad "$missing listener(s) missing auto_download fields — stale bridge binary"
  fi
else
  bad "/api/webhooks returned $STATUS"
fi

for path in "/api/chats?limit=1" "/api/downloads?limit=1" "/api/webhook-logs"; do
  get "$BRIDGE$path"
  [ "$STATUS" = "200" ] && ok "$path answers" || bad "$path returned $STATUS"
done

note "UI — $UI"

get "$UI/index.html"; index="$BODY"
if [ "$STATUS" = "200" ]; then
  ok "index.html served"

  # Substring matching in the shell, not `printf | grep -q`: grep exits on its first match,
  # printf takes SIGPIPE, and with `pipefail` the pipeline reports failure — so an early
  # match looks like a miss. That bug made this script report a stale UI that was fine.
  for marker in autoDownload autoDownloadVoiceOnly voiceOnlyGroup downloadsView; do
    if [[ "$index" == *"$marker"* ]]; then
      ok "index.html has $marker"
    else
      bad "index.html missing $marker — the UI container was not rebuilt"
    fi
  done

  # The script tag is cache-busted; follow the exact URL the browser would load, so a
  # stale container or a forgotten version bump both show up here.
  script_src="$(grep -o 'script\.js?v=[0-9]*' <<< "$index" | head -1)"
  if [ -n "$script_src" ]; then
    ok "index.html requests $script_src"
    get "$UI/$script_src"; script="$BODY"
    if [ "$STATUS" = "200" ]; then
      for fn in syncAutoDownloadFields attachChatPicker renderChatOptions createDownloadRow; do
        if [[ "$script" == *"$fn"* ]]; then
          ok "$script_src defines $fn"
        else
          bad "$script_src missing $fn — the browser would load stale JavaScript"
        fi
      done
    else
      bad "$script_src returned $STATUS"
    fi
  else
    bad "index.html has no versioned script tag"
  fi
else
  bad "index.html returned $STATUS"
fi

# The UI test harness must never be served. Check the body, not the status: nginx's SPA
# fallback (try_files … /index.html) answers any unknown path with index.html and a 200,
# so a status code cannot tell "not served" from "served".
get "$UI/test/package.json"
if [[ "$BODY" == *whatsapp-webhook-ui-tests* ]]; then
  bad "the UI test harness is being served — check .dockerignore"
else
  ok "the UI test harness is not served"
fi

note "$pass passed, $fail failed"
[ "$fail" -eq 0 ] || exit 1
