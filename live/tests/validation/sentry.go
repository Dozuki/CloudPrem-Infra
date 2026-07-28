package validation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// Harness stacks inherit the REAL Sentry DSN: the chart's ExternalSecret reads it from the
// shared Vault path dozuki/global/sentry, the same one production customers use. So every
// transient boot-order error on an ephemeral smoke stack lands in the production Sentry
// project as an alert someone has to triage.
//
// Silenced here, in the harness, rather than with a product knob: muting Sentry is only
// wanted during automated testing, and a chart/CPI flag for it would be one more
// customer-visible surface. The mechanism is a hostAliases entry on the PHP app-tier
// deployments mapping the DSN's ingest host to 127.0.0.1 - the SDK's send then fails with
// an instant connection-refused (no DNS wait, no timeout), so error paths pay nothing.
//
// Why a runtime patch is safe here, each verified rather than assumed:
//   - the chart renders hostAliases only from .Values.hostAliases, which CPI never sets,
//     so nothing fights over the field;
//   - the HelmRelease deliberately omits driftDetection (a completed migration Job must
//     not be rerun), so helm-controller does not revert out-of-band changes;
//   - helm upgrades three-way-merge, preserving fields the chart does not manage, so the
//     alias survives the upgrade scenario's second apply (and validate re-runs this
//     idempotently afterwards anyway).
//
// A side benefit no chart change could give: this works on ANY deployed ref, including
// the old baselines upgrade runs provision, which predate every current values surface.

// sentryEmittingDeployments are the app-image (PHP) deployments that report to Sentry.
// web-nextjs has its own JS-side wiring the harness never configures a DSN for, and the
// remaining workloads (beanstalkd, memcached, exporters) are not Sentry clients.
func sentryEmittingDeployments(release string) []string {
	return []string{
		release + "-app-deployment",
		release + "-crond-deployment",
		release + "-queueworkerd-deployment",
		release + "-searchd-deployment",
	}
}

// SilenceSentry discovers the Sentry ingest host from the deployed stack's own rendered
// config and blackholes it on the PHP deployments via hostAliases. Idempotent; a stack
// with no DSN configured is a no-op. The caller runs it BEFORE the cluster-health wait so
// the resulting rollout is absorbed by a wait that is already happening.
func SilenceSentry(kubeconfig, namespace, release string) error {
	cs, err := clientFor(kubeconfig)
	if err != nil {
		return err
	}
	ctx := context.Background()

	host, err := sentryHost(ctx, cs, namespace, release)
	if err != nil {
		return err
	}
	if host == "" {
		fmt.Fprintf(os.Stderr, ">> [harness %s] sentry: no DSN in the deployed config — nothing to silence\n",
			time.Now().Format("15:04:05"))
		return nil
	}

	patch, err := json.Marshal(map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"hostAliases": []corev1.HostAlias{{IP: "127.0.0.1", Hostnames: []string{host}}},
				},
			},
		},
	})
	if err != nil {
		return err
	}

	var patched []string
	for _, d := range sentryEmittingDeployments(release) {
		if _, err := cs.AppsV1().Deployments(namespace).Patch(ctx, d, types.StrategicMergePatchType, patch, metav1.PatchOptions{}); err != nil {
			// Absent deployments are fine (old refs may not render all of them);
			// anything else is a real failure - a partial silence is worse than a
			// loud one, because it looks done.
			if apierrors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("silence sentry: patch %s: %w", d, err)
		}
		patched = append(patched, d)
	}
	fmt.Fprintf(os.Stderr, ">> [harness %s] sentry: blackholed %s on %d deployments (%s)\n",
		time.Now().Format("15:04:05"), host, len(patched), strings.Join(patched, ", "))
	return nil
}

// sentryHost extracts the DSN host from whichever object carries sentry.json on this
// stack: the ESO-rendered secret (vault path) or the plain ConfigMap (non-vault). Reading
// the DEPLOYED object rather than Vault keeps this working without Vault access and on
// any ref, and means the harness never handles the DSN's secret key - only its hostname.
func sentryHost(ctx context.Context, cs kubernetes.Interface, namespace, release string) (string, error) {
	var raw []byte
	if sec, err := cs.CoreV1().Secrets(namespace).Get(ctx, "dozuki-infra-credentials", metav1.GetOptions{}); err == nil {
		raw = sec.Data["sentry.json"]
	}
	if len(raw) == 0 {
		if cm, err := cs.CoreV1().ConfigMaps(namespace).Get(ctx, "dozuki-resources-configmap", metav1.GetOptions{}); err == nil {
			raw = []byte(cm.Data["sentry.json"])
		}
	}
	if len(raw) == 0 {
		return "", nil
	}
	var parsed struct {
		DsnPhp string `json:"dsnPhp"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("sentry.json in the deployed config did not parse: %w", err)
	}
	return dsnHost(parsed.DsnPhp)
}

// dsnHost pulls the hostname out of a Sentry DSN ("https://key@o123.ingest.sentry.io/9").
// An empty or placeholder DSN yields "", meaning nothing to silence; a malformed one is an
// error so the harness cannot silently believe it silenced something it did not.
func dsnHost(dsn string) (string, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return "", nil
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Hostname() == "" {
		return "", fmt.Errorf("sentry DSN %q has no parseable host", dsn)
	}
	return u.Hostname(), nil
}
