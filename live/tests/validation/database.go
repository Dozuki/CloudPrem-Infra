package validation

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/databasemigrationservice"
)

// A freshly started DMS task does not reach "running" instantly. It is created, sits at
// "ready", and only goes "starting" -> "running" once the dms-start job has kicked it and
// DMS has provisioned it onto the replication instance. Sampling the status once, right
// after provisioning, is a race the harness loses whenever that handoff takes a few
// seconds longer than usual — observed as
//
//	DMS task ...RZQ6J4 status="starting", want running/load-complete
//
// on a run that was otherwise entirely healthy. "starting" is a normal transient, not a
// defect, and a single sample cannot tell the two apart.
//
// So wait for a verdict rather than photographing a transition: poll until the task
// reaches a state that answers the question, and fail immediately on the states that will
// not improve instead of burning the whole budget to reach the same conclusion.
const (
	dmsWaitTimeout = 15 * time.Minute
	dmsPollEvery   = 15 * time.Second
)

// dmsTerminalBad are the states no amount of waiting recovers from. "stopped" is
// deliberately NOT here: a full-load-only task passes through it legitimately, so treating
// it as fatal would fail runs that are fine. It is simply waited out instead.
func dmsTerminalBad(status string) bool {
	switch status {
	case "failed", "failed-move", "deleting":
		return true
	}
	return false
}

func dmsReady(status string) bool {
	return status == "running" || status == "load-complete"
}

// AssertDMSRunning waits for the DMS replication task to reach running/load-complete
// (BI/full configs). The timeout bounds only the wait for a verdict — it is a backstop for
// a task that never progresses at all, not an estimate of how long "starting" should take.
func AssertDMSRunning(ctx context.Context, region, taskARN string) error {
	if taskARN == "" {
		return nil // DMS not enabled for this config
	}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return err
	}
	c := databasemigrationservice.NewFromConfig(cfg)

	started := time.Now()
	deadline := started.Add(dmsWaitTimeout)
	last := ""

	for {
		status, failure, found, derr := describeDMSTask(ctx, c, taskARN)
		if derr != nil {
			return derr
		}
		if !found {
			return fmt.Errorf("DMS task %s not found", taskARN)
		}

		if dmsReady(status) {
			if last != "" {
				fmt.Fprintf(os.Stderr, ">> [harness %s] DMS task reached %q after %s\n",
					time.Now().Format("15:04:05"), status, time.Since(started).Round(time.Second))
			}
			return nil
		}
		if dmsTerminalBad(status) {
			if failure != "" {
				return fmt.Errorf("DMS task %s failed (status=%q): %s", taskARN, status, failure)
			}
			return fmt.Errorf("DMS task %s reached terminal status %q", taskARN, status)
		}

		if status != last {
			fmt.Fprintf(os.Stderr, ">> [harness %s] DMS task status %q — waiting for running/load-complete\n",
				time.Now().Format("15:04:05"), status)
			last = status
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("DMS task %s stuck at status=%q after %s, want running/load-complete",
				taskARN, status, dmsWaitTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(dmsPollEvery):
		}
	}
}

func describeDMSTask(ctx context.Context, c *databasemigrationservice.Client, taskARN string) (status, failure string, found bool, err error) {
	out, err := c.DescribeReplicationTasks(ctx, &databasemigrationservice.DescribeReplicationTasksInput{})
	if err != nil {
		return "", "", false, err
	}
	for _, t := range out.ReplicationTasks {
		if t.ReplicationTaskArn == nil || *t.ReplicationTaskArn != taskARN {
			continue
		}
		if t.Status != nil {
			status = *t.Status
		}
		if t.LastFailureMessage != nil {
			failure = *t.LastFailureMessage
		}
		return status, failure, true, nil
	}
	return "", "", false, nil
}
