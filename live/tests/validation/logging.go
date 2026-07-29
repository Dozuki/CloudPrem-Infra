package validation

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/eks"
)

var wantLogTypes = map[string]bool{"api": true, "audit": true, "authenticator": true, "controllerManager": true, "scheduler": true}

// LoggingEnabled reports whether the EKS cluster has any control-plane log types
// enabled. Used to gate AssertControlPlaneLogging so older refs (no logging) skip
// rather than fail. Best-effort: any error → false (treated as "capability absent").
func LoggingEnabled(ctx context.Context, region, clusterName string) bool {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return false
	}
	ek := eks.NewFromConfig(cfg)
	cl, err := ek.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: &clusterName})
	if err != nil {
		return false
	}
	if cl.Cluster == nil || cl.Cluster.Logging == nil {
		return false
	}
	for _, lc := range cl.Cluster.Logging.ClusterLogging {
		if lc.Enabled != nil && *lc.Enabled && len(lc.Types) > 0 {
			return true
		}
	}
	return false
}

// AssertControlPlaneLogging verifies all 5 control-plane log types are enabled,
// the log group exists at 90-day retention, and audit events are flowing.
func AssertControlPlaneLogging(ctx context.Context, region, clusterName string) error {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return err
	}
	ek := eks.NewFromConfig(cfg)
	cl, err := ek.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: &clusterName})
	if err != nil {
		return err
	}
	enabled := map[string]bool{}
	if cl.Cluster != nil && cl.Cluster.Logging != nil {
		for _, lc := range cl.Cluster.Logging.ClusterLogging {
			if lc.Enabled != nil && *lc.Enabled {
				for _, t := range lc.Types {
					enabled[string(t)] = true
				}
			}
		}
	}
	for t := range wantLogTypes {
		if !enabled[t] {
			return fmt.Errorf("control-plane log type %q not enabled", t)
		}
	}

	logGroup := fmt.Sprintf("/aws/eks/%s/cluster", clusterName)
	cw := cloudwatchlogs.NewFromConfig(cfg)
	lg, err := cw.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{LogGroupNamePrefix: &logGroup})
	if err != nil {
		return err
	}
	if len(lg.LogGroups) == 0 {
		return fmt.Errorf("log group %s missing", logGroup)
	}
	if r := lg.LogGroups[0].RetentionInDays; r == nil || *r != 90 {
		return fmt.Errorf("log group retention = %v, want 90", r)
	}

	return assertAuditEventsFlowing(ctx, cw, logGroup)
}

// auditWaitBudget bounds how long we wait for the first audit events to reach
// CloudWatch. A fresh EKS control plane does not deliver instantly, and this check runs
// only ~15 minutes after the cluster exists.
const auditWaitBudget = 5 * time.Minute

// assertAuditEventsFlowing checks that kube-apiserver-audit events landed in the last 30
// minutes.
//
// Two things make the obvious one-shot version wrong, and cycle 46 lost a 53-minute run
// to them:
//
//   - An empty PAGE is not an empty RESULT. FilterLogEvents scans a bounded amount per
//     call across every matching stream, so it can legitimately return zero events
//     together with a nextToken. The previous code passed Limit=1, took the first page,
//     and reported "no audit events" whenever that page happened to come back empty.
//     Paging is not optional here - it is the difference between "none exist" and "none
//     in the slice I looked at".
//   - Delivery lag. The events show up a few minutes after the control plane starts, so
//     a hard failure on the first look tests AWS's delivery latency, not the deploy.
//
// So: page to exhaustion, and if genuinely nothing is there yet, retry within a budget.
func assertAuditEventsFlowing(ctx context.Context, cw *cloudwatchlogs.Client, logGroup string) error {
	deadline := time.Now().Add(auditWaitBudget)
	for attempt := 1; ; attempt++ {
		found, err := anyAuditEvent(ctx, cw, logGroup)
		if err != nil {
			return err
		}
		if found {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("no kube-apiserver-audit events in last 30m for %s (waited %s across %d attempts)",
				logGroup, auditWaitBudget, attempt)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(30 * time.Second):
		}
	}
}

// anyAuditEvent pages FilterLogEvents to exhaustion, returning true at the first event.
func anyAuditEvent(ctx context.Context, cw *cloudwatchlogs.Client, logGroup string) (bool, error) {
	start := time.Now().Add(-30 * time.Minute).UnixMilli()
	in := &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName:        &logGroup,
		LogStreamNamePrefix: aws.String("kube-apiserver-audit"),
		StartTime:           &start,
	}
	for {
		fl, err := cw.FilterLogEvents(ctx, in)
		if err != nil {
			return false, err
		}
		if len(fl.Events) > 0 {
			return true, nil
		}
		if fl.NextToken == nil || *fl.NextToken == "" {
			return false, nil
		}
		in.NextToken = fl.NextToken
	}
}
