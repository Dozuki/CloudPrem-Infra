package validation

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// The in-VPC probe: the only way to read the promoted cluster.
//
// The DR VPC is air-gapped by design - private subnets, no NAT/IGW, a security group with
// no ingress until failover - so nothing the harness can reach can run a query there.
// A VPC-attached Lambda threads that needle: its code delivery, Invoke call and response
// all travel AWS's control plane, and only its outbound traffic uses the ENI it gets in
// the DR subnets - where the one thing it needs to reach (the database) lives. The
// air-gap is tested without being weakened: no NAT, no IGW, no endpoints are added.
//
// Everything here is ephemeral and run-scoped: an IAM role, a scratch security group, one
// ingress rule punched into the DB's SG (scoped SG-to-SG, mirroring what the failover
// runbook itself would do), and the function. Credentials ride the INVOKE PAYLOAD, not
// function env vars - payloads are not persisted in function configuration.
//
// The cleanup discipline is the hard part and it is not optional: Lambda's Hyperplane
// ENIs release lazily after function deletion, and a lingering ENI blocks the DR subnet
// and SG destroys, wedging the whole stack teardown. deleteProbe therefore waits for the
// ENIs to detach and deletes them itself, and the caller overlaps that wait with the
// drill's own slow instance deletion so the wall-clock cost mostly disappears.

const (
	probeRuntimeTimeout = 30
	// Lambda releases Hyperplane ENIs lazily after function deletion - observed ~13-18
	// minutes on a real run, documented worst case ~20. The first budget (12m) was
	// tighter than reality and failed a run whose data verification had SUCCEEDED,
	// leaving the scratch SG behind for manual rescue. Most of this wait overlaps the
	// drill's ~12m instance deletion, so the marginal wall-clock cost is small.
	probeENIWait     = 25 * time.Minute
	probeCreateRetry = 90 * time.Second // IAM role propagation to Lambda is eventually consistent
)

// DRProbeResult is what the Lambda reports back from the promoted cluster.
type DRProbeResult struct {
	Count      int    `json:"count"`
	MaxWroteAt string `json:"max_wrote_at"`
	Error      string `json:"error,omitempty"`
}

type probeInfra struct {
	region    string
	fnName    string
	roleName  string
	roleArn   string
	sgID      string
	dbSGID    string
	subnetIDs []string
	ec2c      *ec2.Client
	iamc      *iam.Client
	lambdac   *lambda.Client
}

// ProbePromotedCluster stands up the ephemeral Lambda next to the promoted cluster,
// invokes it with the writer endpoint + credentials, and returns what it saw. The caller
// must call the returned cleanup exactly once (it is safe to call after errors); cleanup
// blocks until the probe's network footprint is actually gone, because teardown depends
// on that.
func ProbePromotedCluster(ctx context.Context, drRegion, clusterID, endpoint, user, password, runID string) (res DRProbeResult, cleanup func() error, err error) {
	cleanup = func() error { return nil }
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(drRegion))
	if err != nil {
		return res, cleanup, err
	}
	p := &probeInfra{
		region:  drRegion,
		fnName:  "harness-dr-probe-" + shortRunID(runID),
		ec2c:    ec2.NewFromConfig(cfg),
		iamc:    iam.NewFromConfig(cfg),
		lambdac: lambda.NewFromConfig(cfg),
	}
	p.roleName = p.fnName
	cleanup = func() error { return p.delete(context.Background()) }

	// Where does the probe live? Exactly where the DB does: same VPC, same subnets, so
	// there is no routing question to get wrong.
	rc := rds.NewFromConfig(cfg)
	dbc, err := rc.DescribeDBClusters(ctx, &rds.DescribeDBClustersInput{DBClusterIdentifier: aws.String(clusterID)})
	if err != nil || len(dbc.DBClusters) == 0 {
		return res, cleanup, fmt.Errorf("dr-probe: describe cluster: %w", err)
	}
	cl := dbc.DBClusters[0]
	if len(cl.VpcSecurityGroups) == 0 {
		return res, cleanup, fmt.Errorf("dr-probe: cluster has no VPC security groups")
	}
	p.dbSGID = aws.ToString(cl.VpcSecurityGroups[0].VpcSecurityGroupId)
	sng, err := rc.DescribeDBSubnetGroups(ctx, &rds.DescribeDBSubnetGroupsInput{DBSubnetGroupName: cl.DBSubnetGroup})
	if err != nil || len(sng.DBSubnetGroups) == 0 {
		return res, cleanup, fmt.Errorf("dr-probe: describe subnet group: %w", err)
	}
	vpcID := aws.ToString(sng.DBSubnetGroups[0].VpcId)
	for _, sn := range sng.DBSubnetGroups[0].Subnets {
		p.subnetIDs = append(p.subnetIDs, aws.ToString(sn.SubnetIdentifier))
	}

	logStep("dr-probe: building probe (vpc %s, %d subnets, db sg %s)", vpcID, len(p.subnetIDs), p.dbSGID)

	// Scratch SG for the function + one SG-to-SG 3306 rule on the DB's SG - the same
	// shape of rule the failover runbook adds for the real app, so the drill also
	// exercises that the SG is amendable.
	sg, err := p.ec2c.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String(p.fnName),
		Description: aws.String("ephemeral harness DR probe (deleted by the drill)"),
		VpcId:       aws.String(vpcID),
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeSecurityGroup,
			Tags:         []ec2types.Tag{{Key: aws.String("harness-run"), Value: aws.String(runID)}},
		}},
	})
	if err != nil {
		return res, cleanup, fmt.Errorf("dr-probe: create sg: %w", err)
	}
	p.sgID = aws.ToString(sg.GroupId)
	if _, err := p.ec2c.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: aws.String(p.dbSGID),
		IpPermissions: []ec2types.IpPermission{{
			IpProtocol:       aws.String("tcp"),
			FromPort:         aws.Int32(3306),
			ToPort:           aws.Int32(3306),
			UserIdGroupPairs: []ec2types.UserIdGroupPair{{GroupId: aws.String(p.sgID)}},
		}},
	}); err != nil {
		return res, cleanup, fmt.Errorf("dr-probe: authorize db ingress: %w", err)
	}

	// Role: VPC execution only (ENI management + logs). Nothing else - the credentials
	// arrive in the payload, so the function needs no secrets access.
	trust := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`
	role, err := p.iamc.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(p.roleName),
		AssumeRolePolicyDocument: aws.String(trust),
	})
	if err != nil {
		return res, cleanup, fmt.Errorf("dr-probe: create role: %w", err)
	}
	p.roleArn = aws.ToString(role.Role.Arn)
	if _, err := p.iamc.AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{
		RoleName:  aws.String(p.roleName),
		PolicyArn: aws.String("arn:aws:iam::aws:policy/service-role/AWSLambdaVPCAccessExecutionRole"),
	}); err != nil {
		return res, cleanup, fmt.Errorf("dr-probe: attach policy: %w", err)
	}

	zipBytes, err := buildProbeZip(drRegion)
	if err != nil {
		return res, cleanup, fmt.Errorf("dr-probe: package: %w", err)
	}
	logStep("dr-probe: creating function %s (%d KB)", p.fnName, len(zipBytes)/1024)

	// IAM propagation to Lambda is eventually consistent; retry the assume-role error.
	createDeadline := time.Now().Add(probeCreateRetry)
	for {
		_, err = p.lambdac.CreateFunction(ctx, &lambda.CreateFunctionInput{
			FunctionName: aws.String(p.fnName),
			Runtime:      lambdatypes.RuntimePython312,
			Handler:      aws.String("handler.handler"),
			Role:         aws.String(p.roleArn),
			Timeout:      aws.Int32(probeRuntimeTimeout),
			MemorySize:   aws.Int32(256),
			Code:         &lambdatypes.FunctionCode{ZipFile: zipBytes},
			VpcConfig: &lambdatypes.VpcConfig{
				SubnetIds:        p.subnetIDs,
				SecurityGroupIds: []string{p.sgID},
			},
		})
		if err == nil {
			break
		}
		if strings.Contains(err.Error(), "cannot be assumed") && time.Now().Before(createDeadline) {
			time.Sleep(5 * time.Second)
			continue
		}
		return res, cleanup, fmt.Errorf("dr-probe: create function: %w", err)
	}
	if err := p.waitFunctionActive(ctx, 5*time.Minute); err != nil {
		return res, cleanup, err
	}

	payload, _ := json.Marshal(map[string]string{
		"host": endpoint, "user": user, "password": password, "run_id": runID,
	})
	inv, err := p.lambdac.Invoke(ctx, &lambda.InvokeInput{
		FunctionName: aws.String(p.fnName),
		Payload:      payload,
	})
	if err != nil {
		return res, cleanup, fmt.Errorf("dr-probe: invoke: %w", err)
	}
	if inv.FunctionError != nil {
		return res, cleanup, fmt.Errorf("dr-probe: function error: %s: %s", aws.ToString(inv.FunctionError), truncate(string(inv.Payload), 400))
	}
	if err := json.Unmarshal(inv.Payload, &res); err != nil {
		return res, cleanup, fmt.Errorf("dr-probe: unparseable result %q: %w", truncate(string(inv.Payload), 200), err)
	}
	if res.Error != "" {
		return res, cleanup, fmt.Errorf("dr-probe: query failed on the promoted cluster: %s", res.Error)
	}
	return res, cleanup, nil
}

func (p *probeInfra) waitFunctionActive(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		out, err := p.lambdac.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(p.fnName)})
		if err == nil && out.Configuration != nil && out.Configuration.State == lambdatypes.StateActive {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("dr-probe: function never became Active")
		}
		time.Sleep(5 * time.Second)
	}
}

// delete removes everything the probe created, in dependency order, and WAITS for the
// Hyperplane ENIs to release - a leftover ENI blocks the DR subnet/SG destroy and wedges
// the whole stack teardown, which is a worse outcome than a slow cleanup.
func (p *probeInfra) delete(ctx context.Context) error {
	var errs []string
	if _, err := p.lambdac.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(p.fnName)}); err != nil &&
		!strings.Contains(err.Error(), "ResourceNotFound") {
		errs = append(errs, fmt.Sprintf("delete function: %v", err))
	}
	if p.roleArn != "" {
		if _, err := p.iamc.DetachRolePolicy(ctx, &iam.DetachRolePolicyInput{
			RoleName:  aws.String(p.roleName),
			PolicyArn: aws.String("arn:aws:iam::aws:policy/service-role/AWSLambdaVPCAccessExecutionRole"),
		}); err != nil && !strings.Contains(err.Error(), "NoSuchEntity") {
			errs = append(errs, fmt.Sprintf("detach policy: %v", err))
		}
		if _, err := p.iamc.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(p.roleName)}); err != nil &&
			!strings.Contains(err.Error(), "NoSuchEntity") {
			errs = append(errs, fmt.Sprintf("delete role: %v", err))
		}
	}
	// ENIs: find every interface bound to the scratch SG, wait for detach, delete.
	if p.sgID != "" {
		if err := p.reapENIs(ctx); err != nil {
			errs = append(errs, err.Error())
		}
		if p.dbSGID != "" {
			if _, err := p.ec2c.RevokeSecurityGroupIngress(ctx, &ec2.RevokeSecurityGroupIngressInput{
				GroupId: aws.String(p.dbSGID),
				IpPermissions: []ec2types.IpPermission{{
					IpProtocol:       aws.String("tcp"),
					FromPort:         aws.Int32(3306),
					ToPort:           aws.Int32(3306),
					UserIdGroupPairs: []ec2types.UserIdGroupPair{{GroupId: aws.String(p.sgID)}},
				}},
			}); err != nil && !strings.Contains(err.Error(), "NotFound") {
				errs = append(errs, fmt.Sprintf("revoke db ingress: %v", err))
			}
		}
		if _, err := p.ec2c.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{GroupId: aws.String(p.sgID)}); err != nil &&
			!strings.Contains(err.Error(), "NotFound") {
			errs = append(errs, fmt.Sprintf("delete sg %s: %v", p.sgID, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("dr-probe cleanup incomplete (TEARDOWN MAY WEDGE - clean these by hand): %s", strings.Join(errs, "; "))
	}
	logStep("dr-probe: cleanup complete (function, role, ENIs, sg rule, sg)")
	return nil
}

func (p *probeInfra) reapENIs(ctx context.Context) error {
	deadline := time.Now().Add(probeENIWait)
	for {
		out, err := p.ec2c.DescribeNetworkInterfaces(ctx, &ec2.DescribeNetworkInterfacesInput{
			Filters: []ec2types.Filter{{Name: aws.String("group-id"), Values: []string{p.sgID}}},
		})
		if err != nil {
			return fmt.Errorf("describe probe ENIs: %w", err)
		}
		if len(out.NetworkInterfaces) == 0 {
			return nil
		}
		remaining := 0
		for _, eni := range out.NetworkInterfaces {
			if eni.Status == ec2types.NetworkInterfaceStatusAvailable {
				_, _ = p.ec2c.DeleteNetworkInterface(ctx, &ec2.DeleteNetworkInterfaceInput{
					NetworkInterfaceId: eni.NetworkInterfaceId,
				})
				continue
			}
			remaining++
		}
		if remaining == 0 {
			continue // available ones just deleted; loop once more to confirm empty
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%d probe ENI(s) still attached after %s (Lambda releases them lazily)", remaining, probeENIWait)
		}
		logStep("dr-probe: waiting for %d Lambda ENI(s) to release", remaining)
		time.Sleep(30 * time.Second)
	}
}

// buildProbeZip packages the handler with a vendored PyMySQL (pure python, no compiled
// deps) via pip install --target, plus the region's RDS CA bundle so the function
// VERIFIES the server certificate chain. The bundle is fetched at BUILD time by the
// harness host (which has internet); the air-gapped Lambda only reads the baked-in file.
// Requires python3+pip on the harness host - the hosts that run the harness have both.
func buildProbeZip(drRegion string) ([]byte, error) {
	tmp, err := os.MkdirTemp("", "dr-probe-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if out, err := exec.Command("python3", "-m", "pip", "install", "--quiet", "--target", tmp, "pymysql").CombinedOutput(); err != nil {
		return nil, fmt.Errorf("pip install pymysql: %w (%s)", err, firstLine(string(out)))
	}
	caURL := fmt.Sprintf("https://truststore.pki.rds.amazonaws.com/%s/%s-bundle.pem", drRegion, drRegion)
	ca, err := fetchURL(caURL)
	if err != nil {
		// No insecure fallback: a probe that cannot verify the server should fail to
		// build, not connect blind.
		return nil, fmt.Errorf("fetch RDS CA bundle %s: %w", caURL, err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "rds-ca.pem"), ca, 0o644); err != nil {
		return nil, err
	}
	handler := `import json, os, ssl
import pymysql

def handler(event, context):
    try:
        # VERIFY_CA semantics: the chain is validated against the baked-in RDS CA
        # bundle (MITM defense), but hostname checking stays off - RDS certificates
        # carry the INSTANCE endpoint SAN while this connects via the CLUSTER
        # endpoint, so check_hostname would reject legitimate certs.
        sctx = ssl.create_default_context(
            cafile=os.path.join(os.path.dirname(__file__), "rds-ca.pem"))
        sctx.check_hostname = False
        sctx.verify_mode = ssl.CERT_REQUIRED
        conn = pymysql.connect(host=event["host"], user=event["user"],
                               password=event["password"], database="harness_dr",
                               connect_timeout=10, ssl=sctx)
        with conn.cursor() as cur:
            cur.execute("SELECT COUNT(*), MAX(wrote_at) FROM heartbeat WHERE run_id=%s",
                        (event["run_id"],))
            n, mx = cur.fetchone()
        conn.close()
        return {"count": int(n), "max_wrote_at": str(mx) if mx else ""}
    except Exception as e:
        return {"count": -1, "max_wrote_at": "", "error": f"{type(e).__name__}: {e}"}
`
	if err := os.WriteFile(filepath.Join(tmp, "handler.py"), []byte(handler), 0o644); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	err = filepath.Walk(tmp, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(tmp, path)
		w, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// DBCredentials fetches username/password from the stack's primary DB secret (Secrets
// Manager, primary region). The users replicate with the cluster, so the primary's
// credentials are valid on the promoted secondary.
func DBCredentials(ctx context.Context, primaryRegion, secretARN string) (user, password string, err error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(primaryRegion))
	if err != nil {
		return "", "", err
	}
	out, err := secretsmanager.NewFromConfig(cfg).GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(secretARN),
	})
	if err != nil {
		return "", "", fmt.Errorf("dr-probe: get db secret: %w", err)
	}
	return parseDBSecret(aws.ToString(out.SecretString))
}

// parseDBSecret tolerates the two key spellings RDS-style secrets use.
func parseDBSecret(raw string) (user, password string, err error) {
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return "", "", fmt.Errorf("db secret is not JSON: %w", err)
	}
	str := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := m[k].(string); ok && v != "" {
				return v
			}
		}
		return ""
	}
	user, password = str("username", "user"), str("password")
	if user == "" || password == "" {
		return "", "", fmt.Errorf("db secret missing username/password keys (has: %s)", strings.Join(mapKeys(m), ", "))
	}
	return user, password, nil
}

func fetchURL(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(string(b), "BEGIN CERTIFICATE") {
		return nil, fmt.Errorf("response is not a PEM bundle")
	}
	return b, nil
}

func mapKeys(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
