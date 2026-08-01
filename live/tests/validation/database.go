package validation

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/databasemigrationservice"
	"github.com/aws/aws-sdk-go-v2/service/rds"
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

// AssertLocalInfileEnabled proves the BI Aurora cluster still accepts LOAD DATA LOCAL
// INFILE, which is how the DMS full load writes into it. The physical layer used to pin
// local_infile=1 in the BI CLUSTER parameter group; that pin was removed because it can
// never converge (1 is the engine default, so ModifyDBClusterParameterGroup is a silent
// no-op AWS keeps answering as Source=system/ApplyMethod=pending-reboot, and the provider
// reads that stale apply_method straight back into state, re-planning forever, see
// hashicorp/terraform-provider-aws#30802). The pin was also in the wrong place to begin
// with: local_infile is an INSTANCE-level parameter that the cluster-level API merely
// tolerates, so an entry there never had any effect on the engine.
//
// This checks the value where it actually takes effect, the writer's DB (instance)
// parameter group, and asserts the effective value rather than the presence of an
// override. Default-sourced 1 passes, an explicit 1 passes, and the only thing that fails
// is a real 0 (someone adding an override that would break the full load). Removing the
// pin therefore trades a permanently-drifting declaration for a check that catches the
// failure the pin was supposed to prevent but could not.
func AssertLocalInfileEnabled(ctx context.Context, region, clusterID string) error {
	if clusterID == "" {
		return nil // no aurora BI cluster in this config
	}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return err
	}
	rc := rds.NewFromConfig(cfg)

	instanceID, err := clusterWriterInstance(ctx, rc, clusterID)
	if err != nil {
		return err
	}
	inst, err := rc.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(instanceID)})
	if err != nil {
		return err
	}
	if len(inst.DBInstances) == 0 || len(inst.DBInstances[0].DBParameterGroups) == 0 {
		return fmt.Errorf("BI instance %s has no DB parameter group", instanceID)
	}
	pg := aws.ToString(inst.DBInstances[0].DBParameterGroups[0].DBParameterGroupName)

	value, source, found, err := dbParameter(ctx, rc, pg, "local_infile")
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("local_infile not present in BI parameter group %s", pg)
	}
	if value != "1" {
		return fmt.Errorf("BI local_infile=%q (source=%s) in %s, want 1 - the DMS full load needs LOAD DATA LOCAL INFILE", value, source, pg)
	}
	fmt.Fprintf(os.Stderr, ">> [harness %s] BI local_infile=1 (source=%s) in %s\n",
		time.Now().Format("15:04:05"), source, pg)
	return nil
}

// clusterWriterInstance resolves the cluster's writer. Serverless v2 BI clusters run a
// single writer, but a reader can be added, and only the writer takes the DMS load.
func clusterWriterInstance(ctx context.Context, rc *rds.Client, clusterID string) (string, error) {
	out, err := rc.DescribeDBClusters(ctx, &rds.DescribeDBClustersInput{DBClusterIdentifier: aws.String(clusterID)})
	if err != nil {
		return "", err
	}
	if len(out.DBClusters) == 0 {
		return "", fmt.Errorf("BI cluster %s not found", clusterID)
	}
	for _, m := range out.DBClusters[0].DBClusterMembers {
		if aws.ToBool(m.IsClusterWriter) {
			return aws.ToString(m.DBInstanceIdentifier), nil
		}
	}
	return "", fmt.Errorf("BI cluster %s has no writer instance", clusterID)
}

// dbParameter pulls one parameter out of a DB parameter group. DescribeDBParameters has no
// name filter, so every page has to be walked; the group carries a few hundred entries, so
// this is two or three calls at most.
func dbParameter(ctx context.Context, rc *rds.Client, group, name string) (value, source string, found bool, err error) {
	p := rds.NewDescribeDBParametersPaginator(rc, &rds.DescribeDBParametersInput{
		DBParameterGroupName: aws.String(group),
	})
	for p.HasMorePages() {
		page, perr := p.NextPage(ctx)
		if perr != nil {
			return "", "", false, perr
		}
		for _, param := range page.Parameters {
			if aws.ToString(param.ParameterName) == name {
				return aws.ToString(param.ParameterValue), aws.ToString(param.Source), true, nil
			}
		}
	}
	return "", "", false, nil
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
