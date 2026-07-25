#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"

CTX="" NS="dozuki" TARGET="" VUS="50" DURATION="5m" LABEL="run"
K6_IMAGE="grafana/k6:latest" INSECURE="true" PROM_URL="" EMAIL="" PASSWORD="" DO_SEED="false"
while [ $# -gt 0 ]; do case "$1" in
  --kube-context) CTX="$2"; shift 2;; --namespace) NS="$2"; shift 2;;
  --target) TARGET="$2"; shift 2;; --vus) VUS="$2"; shift 2;;
  --duration) DURATION="$2"; shift 2;; --label) LABEL="$2"; shift 2;;
  --k6-image) K6_IMAGE="$2"; shift 2;; --insecure) INSECURE="$2"; shift 2;;
  --prom-url) PROM_URL="$2"; shift 2;; --admin-email) EMAIL="$2"; shift 2;;
  --admin-password) PASSWORD="$2"; shift 2;; --seed) DO_SEED="true"; shift;;
  *) echo "unknown arg: $1" >&2; exit 1;; esac; done
[ -n "$CTX" ] && [ -n "$TARGET" ] && [ -n "$EMAIL" ] && [ -n "$PASSWORD" ] || {
  echo "required: --kube-context --target --admin-email --admin-password" >&2; exit 1; }
K6_OUT=""; [ -n "$PROM_URL" ] && K6_OUT="experimental-prometheus-rw"

KC=(kubectl --context "$CTX" -n "$NS")

if [ "$DO_SEED" = "true" ]; then
  echo "[seed] running locally against $TARGET"
  K6_TARGET="$TARGET" ADMIN_EMAIL="$EMAIL" ADMIN_PASSWORD="$PASSWORD" \
    k6 run --log-format=raw seed/seed.js 2>&1 | tee /dev/stderr \
    | awk -F'POOL_JSON:' '/POOL_JSON:/{print $2}' | tail -1 > pool.json
  jq -e '.guides | length > 0' pool.json >/dev/null || {
    echo "seed produced an empty pool (see output above)"; exit 1; }
fi
[ -f pool.json ] || { echo "pool.json missing — run with --seed first (or seed manually)"; exit 1; }
POOL_JSON="$(jq -c . pool.json)"

# scripts ConfigMap (scenario + auth helper)
"${KC[@]}" create configmap "loadtest-${LABEL}-scripts" \
  --from-file=journeys.js=scenarios/journeys.js --from-file=auth.js=scenarios/auth.js \
  --dry-run=client -o yaml | "${KC[@]}" apply -f -

export LABEL NAMESPACE="$NS" K6_IMAGE TARGET VUS DURATION INSECURE ADMIN_EMAIL="$EMAIL" \
  ADMIN_PASSWORD="$PASSWORD" POOL_JSON PROM_URL K6_OUT
"${KC[@]}" delete job "loadtest-${LABEL}" --ignore-not-found
envsubst < k6-job.yaml | "${KC[@]}" apply -f -

echo "[run] waiting for loadtest-${LABEL} to complete..."
"${KC[@]}" wait --for=condition=complete "job/loadtest-${LABEL}" --timeout=3600s &
WAIT=$!
"${KC[@]}" wait --for=condition=failed "job/loadtest-${LABEL}" --timeout=3600s && { echo "job failed"; } &
wait $WAIT || true
"${KC[@]}" logs "job/loadtest-${LABEL}" --tail=-1 | tee "results/${LABEL}.log"
echo "[run] summary log saved to results/${LABEL}.log (k6 in-pod wrote summary-${LABEL}.json in the pod;"
echo "      the textual summary + p95/err are in the log above; Prometheus has the full series if --prom-url set)"
