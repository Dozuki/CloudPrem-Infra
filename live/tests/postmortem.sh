#!/usr/bin/env bash
# postmortem.sh RUN_ID RUN_LOG — opt-in (RUN_POSTMORTEM=1) LLM root-cause analysis of a
# FAILED harness run. Non-gating. No-ops cleanly if the `claude` CLI isn't installed.
set -uo pipefail
RUN_ID="${1:?usage: postmortem.sh RUN_ID RUN_LOG}"
RUN_LOG="${2:?usage: postmortem.sh RUN_ID RUN_LOG}"
cd "$(dirname "$0")"

if ! command -v claude >/dev/null 2>&1; then
  echo ">> post-mortem: 'claude' CLI not found — skipping (pull the artifacts and run it manually)" >&2
  exit 0
fi

ADIR="$PWD/.artifacts/$RUN_ID"
mkdir -p "$ADIR"
OUT="$ADIR/postmortem.txt"
manifest="$( cd "$PWD/.artifacts" 2>/dev/null && ls -R "$RUN_ID" "$RUN_ID"-* 2>/dev/null | head -200 )"
logtail="$( tail -300 "$RUN_LOG" 2>/dev/null )"
prompt="You are debugging a FAILED CloudPrem upgrade-test harness run. From the artifact file manifest and the run-log tail below, identify the single most likely ROOT CAUSE and one concrete next step. Be concise (under 200 words).

=== artifact manifest ===
$manifest

=== run log (tail) ===
$logtail"

echo ">> post-mortem: analyzing failure with claude (non-gating) ..." >&2
if printf '%s' "$prompt" | claude -p >"$OUT" 2>/dev/null && [ -s "$OUT" ]; then
  echo "================ POST-MORTEM (claude) ================" >&2
  cat "$OUT" >&2
  echo "=====================================================" >&2
  echo ">> post-mortem saved to $OUT" >&2
else
  echo ">> post-mortem: claude invocation produced no output — skipping (non-fatal)" >&2
fi
exit 0
