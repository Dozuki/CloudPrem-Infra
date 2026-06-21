package validation

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/databasemigrationservice"
)

// AssertDMSRunning verifies the DMS replication task is running (BI/full configs).
func AssertDMSRunning(ctx context.Context, region, taskARN string) error {
	if taskARN == "" {
		return nil // DMS not enabled for this config
	}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return err
	}
	c := databasemigrationservice.NewFromConfig(cfg)
	out, err := c.DescribeReplicationTasks(ctx, &databasemigrationservice.DescribeReplicationTasksInput{})
	if err != nil {
		return err
	}
	for _, t := range out.ReplicationTasks {
		if t.ReplicationTaskArn != nil && *t.ReplicationTaskArn == taskARN {
			st := ""
			if t.Status != nil {
				st = *t.Status
			}
			if st != "running" && st != "load-complete" {
				return fmt.Errorf("DMS task %s status=%q, want running/load-complete", taskARN, st)
			}
			return nil
		}
	}
	return fmt.Errorf("DMS task %s not found", taskARN)
}
