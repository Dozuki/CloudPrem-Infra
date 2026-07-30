package validation

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/databasemigrationservice"
)

// A freshly started DMS task does not reach "running" instantly. It is created, sits at
// "ready", and only goes "starting" -> "running" once the dms-start job has kicked it and
// DMS has provisioned it. A serverless replication is the same shape of wait: it sits at
// "created" until the same Job starts it, then initializes. Sampling the status once, right
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
// Serverless gets a much larger budget than the 15m a provisioned task needs. Nothing starts
// a replication config until the logical layer's dms-start Job does, that Job first waits for
// the app deployment to become available (gated behind db-migrations on a fresh stack), and
// only then does DMS provision capacity and run its own Testing Connection phase. Greenfield
// runs have taken ~40 minutes end to end, all of it healthy, which the 15m budget scored as a
// stuck replication. The number is still only a backstop for something that never progresses.
const (
	dmsWaitTimeout           = 15 * time.Minute
	dmsServerlessWaitTimeout = 60 * time.Minute
	dmsPollEvery             = 15 * time.Second
)

// dmsTimeoutFor picks the budget off the ARN shape, same discriminator describeDMSTask uses.
func dmsTimeoutFor(taskARN string) time.Duration {
	if strings.Contains(taskARN, ":replication-config:") {
		return dmsServerlessWaitTimeout
	}
	return dmsWaitTimeout
}

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

	budget := dmsTimeoutFor(taskARN)
	started := time.Now()
	deadline := started.Add(budget)
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
				taskARN, status, budget)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(dmsPollEvery):
		}
	}
}

// describeDMSTask resolves the status of whatever the physical layer's dms_task_arn output
// points at. Since the BI replication moved to DMS Serverless that is a replication-config
// ARN, which DescribeReplicationTasks does not know about and will never match - it would
// report "not found" and fail the run. The aurora migration is still a provisioned task, so
// both shapes have to work; the ARN discriminates, same as the restart lambda does.
func describeDMSTask(ctx context.Context, c *databasemigrationservice.Client, taskARN string) (status, failure string, found bool, err error) {
	if strings.Contains(taskARN, ":replication-config:") {
		return describeDMSReplication(ctx, c, taskARN)
	}
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

// describeDMSReplication is the serverless counterpart. Status strings overlap with the
// provisioned ones the callers already switch on ("running", "failed", ...), so only the
// lookup differs; failure detail arrives as a list rather than a single field.
//
// A config that has never been started has NO Replication row at all (the config is created
// with start_replication=false and only the logical dms-start Job starts it, after
// db-migrations). That is a normal pre-start state, not an absence: report it as "created"
// with found=true so the caller's poll loop waits it out. found=false is reserved for a
// config ARN DescribeReplicationConfigs does not know either.
func describeDMSReplication(ctx context.Context, c *databasemigrationservice.Client, configARN string) (status, failure string, found bool, err error) {
	out, err := c.DescribeReplications(ctx, &databasemigrationservice.DescribeReplicationsInput{})
	if err != nil {
		return "", "", false, err
	}
	for _, r := range out.Replications {
		if r.ReplicationConfigArn == nil || *r.ReplicationConfigArn != configARN {
			continue
		}
		if r.Status != nil {
			status = *r.Status
		}
		if len(r.FailureMessages) > 0 {
			failure = strings.Join(r.FailureMessages, "; ")
		}
		return status, failure, true, nil
	}
	cfgs, err := c.DescribeReplicationConfigs(ctx, &databasemigrationservice.DescribeReplicationConfigsInput{})
	if err != nil {
		return "", "", false, err
	}
	for _, rc := range cfgs.ReplicationConfigs {
		if rc.ReplicationConfigArn != nil && *rc.ReplicationConfigArn == configARN {
			return "created", "", true, nil
		}
	}
	return "", "", false, nil
}
