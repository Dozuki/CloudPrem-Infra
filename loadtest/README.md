# Dozuki MPC Load-Test Harness

Cloud-neutral k6 load test for any running Dozuki stack (AWS or Azure). Targets a stack by URL + admin
creds; runs the load generator as an in-cluster Kubernetes Job.

## Prerequisites
- `kubectl` access to the target cluster (context), `k6` locally (for seeding), `jq`.
- Admin credentials for the target stack (email + password).

## 1. Seed a dataset (once per env)
```bash
K6_TARGET="https://<stack-fqdn>" ADMIN_EMAIL=... ADMIN_PASSWORD=... \
  SEED_GUIDES=300 SEED_COURSES=30 SEED_USERS=50 \
  k6 run seed/seed.js     # writes loadtest/pool.json (created IDs)
```

## 2. Run a load test (in-cluster Job)
```bash
./run.sh \
  --kube-context <ctx> --namespace dozuki \
  --target https://<in-cluster-or-public-url> \
  --vus 50 --duration 5m --label baseline \
  --admin-email ... --admin-password ...
# -> writes results/summary-baseline.json (and remote-writes to Prometheus if --prom-url given)
```

## 3. A/B compare (e.g. in-cluster memcached vs ElastiCache)
1. Seed once.
2. `./run.sh ... --label A` against config A.
3. Flip the config (e.g. `memcached_in_cluster` true→false + redeploy), wait for ready.
4. `./run.sh ... --label B` against config B.
5. `./results/compare.sh results/summary-A.json results/summary-B.json`

The memcached win shows as guide/search **p95 under load** + a DB-load (CPU/connections) delta in
Grafana over the run window — not internal cache-op counts (app exposes no Prometheus app-metrics).

## Cloud-neutral
Every cloud-specific is a flag/env (`--kube-context`, `--target`, `--k6-image`, `INSECURE`, `--prom-url`).
The upload scenario uses the app upload API, so it's identical on AWS S3 or Azure SeaweedFS. No cluster
names or hosts are hardcoded.
