#!/usr/bin/env bash
# capture-cluster.sh — dump live-cluster diagnostics for a harness run BEFORE the
# deferred teardown destroys the cluster. Called by the Go harness (run.go) on failure;
# the data (pod states, events, failed-pod logs, gateway listener status, rendered
# configmaps) is gone once the EKS cluster is deleted, so it has to be grabbed here.
#
# Best-effort: every step swallows errors; this script never fails its caller. It does
# NOT capture Secret *data* (only names+types) or raw TF state — no secrets land here.
#
# Usage: capture-cluster.sh <outdir> <cluster> <region> <aws_profile> [namespace] [release]
set -uo pipefail
OUT="${1:?outdir}"; CLUSTER="${2:?cluster}"; REGION="${3:?region}"; PROFILE="${4:?profile}"
NS="${5:-dozuki}"; RELEASE="${6:-dozuki}"
mkdir -p "$OUT"

KCDIR="$(mktemp -d)"; KC="$KCDIR/config"
trap 'rm -rf "$KCDIR"' EXIT

if ! aws eks update-kubeconfig --name "$CLUSTER" --region "$REGION" --profile "$PROFILE" --kubeconfig "$KC" >/dev/null 2>&1; then
  echo "EKS cluster '$CLUSTER' not reachable (already gone or never created) — no cluster dump." > "$OUT/cluster-unavailable.txt"
  exit 0
fi
export KUBECONFIG="$KC"

kubectl get pods -A -o wide                       > "$OUT/pods.txt"          2>&1
kubectl get events -A --sort-by=.lastTimestamp    > "$OUT/events.txt"        2>&1
kubectl get all,jobs,pvc -n "$NS" -o wide         > "$OUT/ns-resources.txt"  2>&1
kubectl get gateway -A -o yaml                     > "$OUT/gateways.txt"      2>&1   # listener status (cert refs etc.)
kubectl get secret -n "$NS"                        > "$OUT/secrets.txt"       2>&1   # NAMES + TYPES only (no data)
kubectl describe pods -n "$NS"                     > "$OUT/describe-pods.txt" 2>&1
kubectl get cm -n "$NS" -o yaml                    > "$OUT/configmaps.txt"    2>&1   # rendered app config (memcached.json etc.)

# Per-pod logs (current + previous, all containers) for the app namespace.
mkdir -p "$OUT/logs"
for p in $(kubectl get pods -n "$NS" -o name 2>/dev/null); do
  n="${p#pod/}"
  kubectl logs "$p" -n "$NS" --all-containers --tail=400              > "$OUT/logs/$n.txt"      2>&1
  kubectl logs "$p" -n "$NS" --all-containers --previous --tail=400   > "$OUT/logs/$n.prev.txt" 2>&1 || rm -f "$OUT/logs/$n.prev.txt"
done

# Helm release state (non-secret: revisions + status).
{ helm history "$RELEASE" -n "$NS"; echo; helm status "$RELEASE" -n "$NS"; } > "$OUT/helm.txt" 2>&1 || true

echo ">> capture-cluster: wrote $(find "$OUT" -type f | wc -l | tr -d ' ') files to $OUT"
