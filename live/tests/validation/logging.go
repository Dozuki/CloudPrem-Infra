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

	// Audit events flowing in the last 30 min.
	start := time.Now().Add(-30 * time.Minute).UnixMilli()
	fl, err := cw.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName:        &logGroup,
		LogStreamNamePrefix: aws.String("kube-apiserver-audit"),
		StartTime:           &start,
		Limit:               aws.Int32(1),
	})
	if err != nil {
		return err
	}
	if len(fl.Events) == 0 {
		return fmt.Errorf("no kube-apiserver-audit events in last 30m for %s", logGroup)
	}
	return nil
}
