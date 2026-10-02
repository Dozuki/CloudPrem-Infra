package harness

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
)

// The Resource Groups Tagging API is an index, and it keeps entries for resources that
// are already deleted. Measured 2026-10-02: the DDVtest reaper report carried 26-33
// harness stacks as eligible orphans, every sampled ARN (instance, volume, ENI, NAT
// gateway, VPC endpoint, flow log) described as NotFound, and the runs' physical state
// held zero resources. Counting those entries made every torn-down run an orphan
// forever. So each ARN the tag query returns is checked against its owning service
// before it is allowed to count.
//
// The verifier only ever answers "this one is provably gone". Anything it cannot
// prove (a type with no describe call here, a region with no client) stays counted,
// the same fail-loud posture as deniedResourceTypes. Any describe error that is not
// a plain not-found aborts the whole check so the caller can fail closed to Unknown.

// ExistenceVerifier reports which of the given ARNs are verified deleted. An ARN it
// does not return in the map is treated as live by the caller.
type ExistenceVerifier interface {
	Gone(ctx context.Context, arns []string) (map[string]bool, error)
}

// EC2DescribeAPI is the read-only EC2 surface the verifier needs, one client per region.
type EC2DescribeAPI interface {
	DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
	DescribeVolumes(context.Context, *ec2.DescribeVolumesInput, ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error)
	DescribeNetworkInterfaces(context.Context, *ec2.DescribeNetworkInterfacesInput, ...func(*ec2.Options)) (*ec2.DescribeNetworkInterfacesOutput, error)
	DescribeNatGateways(context.Context, *ec2.DescribeNatGatewaysInput, ...func(*ec2.Options)) (*ec2.DescribeNatGatewaysOutput, error)
	DescribeVpcEndpoints(context.Context, *ec2.DescribeVpcEndpointsInput, ...func(*ec2.Options)) (*ec2.DescribeVpcEndpointsOutput, error)
	DescribeVpcs(context.Context, *ec2.DescribeVpcsInput, ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error)
	DescribeSubnets(context.Context, *ec2.DescribeSubnetsInput, ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error)
	DescribeInternetGateways(context.Context, *ec2.DescribeInternetGatewaysInput, ...func(*ec2.Options)) (*ec2.DescribeInternetGatewaysOutput, error)
	DescribeRouteTables(context.Context, *ec2.DescribeRouteTablesInput, ...func(*ec2.Options)) (*ec2.DescribeRouteTablesOutput, error)
	DescribeSecurityGroups(context.Context, *ec2.DescribeSecurityGroupsInput, ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error)
	DescribeAddresses(context.Context, *ec2.DescribeAddressesInput, ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error)
	DescribeFlowLogs(context.Context, *ec2.DescribeFlowLogsInput, ...func(*ec2.Options)) (*ec2.DescribeFlowLogsOutput, error)
	DescribeDhcpOptions(context.Context, *ec2.DescribeDhcpOptionsInput, ...func(*ec2.Options)) (*ec2.DescribeDhcpOptionsOutput, error)
	DescribeNetworkAcls(context.Context, *ec2.DescribeNetworkAclsInput, ...func(*ec2.Options)) (*ec2.DescribeNetworkAclsOutput, error)
	DescribeLaunchTemplates(context.Context, *ec2.DescribeLaunchTemplatesInput, ...func(*ec2.Options)) (*ec2.DescribeLaunchTemplatesOutput, error)
}

// EC2ExistenceVerifier checks the EC2 resource family. EC2 is where the measured
// ghosts live, and one read-only client covers every type below.
type EC2ExistenceVerifier struct {
	EC2 map[string]EC2DescribeAPI // keyed by region
}

// IncludeManagedResources is set on every describe that takes it (instances, volumes,
// ENIs, launch templates). With the account's managed-resource visibility set to
// hidden, EC2 omits AWS-managed resources (EKS Auto Mode nodes, their volumes and
// ENIs) from those calls unless asked, and an omission here reads as "deleted".

// existenceBatch caps the ids sent in one filter. EC2 documents a 200-value ceiling
// on some filters; 100 stays clear of it everywhere.
const existenceBatch = 100

// ec2PresentFunc returns the subset of ids that exist and are not in a terminal
// state. Filter-based describes never error on an id that is gone (they just omit it),
// so an absent id is the "gone" signal; a terminal state is the other one.
type ec2PresentFunc func(ctx context.Context, c EC2DescribeAPI, ids []string) (map[string]bool, error)

func idFilter(name string, ids []string) []ec2types.Filter {
	return []ec2types.Filter{{Name: aws.String(name), Values: ids}}
}

// ec2Present maps an arnResourceType "ec2:<type>" to its describe call. A type not on
// this map is never verified and always counts as live.
var ec2Present = map[string]ec2PresentFunc{
	"ec2:instance": func(ctx context.Context, c EC2DescribeAPI, ids []string) (map[string]bool, error) {
		out := map[string]bool{}
		in := &ec2.DescribeInstancesInput{Filters: idFilter("instance-id", ids), IncludeManagedResources: aws.Bool(true)}
		for {
			page, err := c.DescribeInstances(ctx, in)
			if err != nil {
				return nil, err
			}
			for _, r := range page.Reservations {
				for _, i := range r.Instances {
					// terminated is final. shutting-down still counts: it has not
					// finished going away, and a stuck one is worth seeing.
					if i.State != nil && i.State.Name == ec2types.InstanceStateNameTerminated {
						continue
					}
					out[aws.ToString(i.InstanceId)] = true
				}
			}
			if aws.ToString(page.NextToken) == "" {
				return out, nil
			}
			in.NextToken = page.NextToken
		}
	},
	"ec2:volume": func(ctx context.Context, c EC2DescribeAPI, ids []string) (map[string]bool, error) {
		out := map[string]bool{}
		in := &ec2.DescribeVolumesInput{Filters: idFilter("volume-id", ids), IncludeManagedResources: aws.Bool(true)}
		for {
			page, err := c.DescribeVolumes(ctx, in)
			if err != nil {
				return nil, err
			}
			for _, v := range page.Volumes {
				if v.State == ec2types.VolumeStateDeleted {
					continue
				}
				out[aws.ToString(v.VolumeId)] = true
			}
			if aws.ToString(page.NextToken) == "" {
				return out, nil
			}
			in.NextToken = page.NextToken
		}
	},
	"ec2:network-interface": func(ctx context.Context, c EC2DescribeAPI, ids []string) (map[string]bool, error) {
		out := map[string]bool{}
		in := &ec2.DescribeNetworkInterfacesInput{Filters: idFilter("network-interface-id", ids), IncludeManagedResources: aws.Bool(true)}
		for {
			page, err := c.DescribeNetworkInterfaces(ctx, in)
			if err != nil {
				return nil, err
			}
			for _, n := range page.NetworkInterfaces {
				out[aws.ToString(n.NetworkInterfaceId)] = true
			}
			if aws.ToString(page.NextToken) == "" {
				return out, nil
			}
			in.NextToken = page.NextToken
		}
	},
	"ec2:natgateway": func(ctx context.Context, c EC2DescribeAPI, ids []string) (map[string]bool, error) {
		out := map[string]bool{}
		in := &ec2.DescribeNatGatewaysInput{Filter: idFilter("nat-gateway-id", ids)}
		for {
			page, err := c.DescribeNatGateways(ctx, in)
			if err != nil {
				return nil, err
			}
			for _, n := range page.NatGateways {
				// deleted (and failed, which never came up) are final; deleting is not.
				if n.State == ec2types.NatGatewayStateDeleted || n.State == ec2types.NatGatewayStateFailed {
					continue
				}
				out[aws.ToString(n.NatGatewayId)] = true
			}
			if aws.ToString(page.NextToken) == "" {
				return out, nil
			}
			in.NextToken = page.NextToken
		}
	},
	"ec2:vpc-endpoint": func(ctx context.Context, c EC2DescribeAPI, ids []string) (map[string]bool, error) {
		out := map[string]bool{}
		in := &ec2.DescribeVpcEndpointsInput{Filters: idFilter("vpc-endpoint-id", ids)}
		for {
			page, err := c.DescribeVpcEndpoints(ctx, in)
			if err != nil {
				return nil, err
			}
			for _, e := range page.VpcEndpoints {
				if strings.EqualFold(string(e.State), string(ec2types.StateDeleted)) {
					continue
				}
				out[aws.ToString(e.VpcEndpointId)] = true
			}
			if aws.ToString(page.NextToken) == "" {
				return out, nil
			}
			in.NextToken = page.NextToken
		}
	},
	"ec2:vpc": func(ctx context.Context, c EC2DescribeAPI, ids []string) (map[string]bool, error) {
		out := map[string]bool{}
		in := &ec2.DescribeVpcsInput{Filters: idFilter("vpc-id", ids)}
		for {
			page, err := c.DescribeVpcs(ctx, in)
			if err != nil {
				return nil, err
			}
			for _, v := range page.Vpcs {
				out[aws.ToString(v.VpcId)] = true
			}
			if aws.ToString(page.NextToken) == "" {
				return out, nil
			}
			in.NextToken = page.NextToken
		}
	},
	"ec2:subnet": func(ctx context.Context, c EC2DescribeAPI, ids []string) (map[string]bool, error) {
		out := map[string]bool{}
		in := &ec2.DescribeSubnetsInput{Filters: idFilter("subnet-id", ids)}
		for {
			page, err := c.DescribeSubnets(ctx, in)
			if err != nil {
				return nil, err
			}
			for _, s := range page.Subnets {
				out[aws.ToString(s.SubnetId)] = true
			}
			if aws.ToString(page.NextToken) == "" {
				return out, nil
			}
			in.NextToken = page.NextToken
		}
	},
	"ec2:internet-gateway": func(ctx context.Context, c EC2DescribeAPI, ids []string) (map[string]bool, error) {
		out := map[string]bool{}
		in := &ec2.DescribeInternetGatewaysInput{Filters: idFilter("internet-gateway-id", ids)}
		for {
			page, err := c.DescribeInternetGateways(ctx, in)
			if err != nil {
				return nil, err
			}
			for _, g := range page.InternetGateways {
				out[aws.ToString(g.InternetGatewayId)] = true
			}
			if aws.ToString(page.NextToken) == "" {
				return out, nil
			}
			in.NextToken = page.NextToken
		}
	},
	"ec2:route-table": func(ctx context.Context, c EC2DescribeAPI, ids []string) (map[string]bool, error) {
		out := map[string]bool{}
		in := &ec2.DescribeRouteTablesInput{Filters: idFilter("route-table-id", ids)}
		for {
			page, err := c.DescribeRouteTables(ctx, in)
			if err != nil {
				return nil, err
			}
			for _, r := range page.RouteTables {
				out[aws.ToString(r.RouteTableId)] = true
			}
			if aws.ToString(page.NextToken) == "" {
				return out, nil
			}
			in.NextToken = page.NextToken
		}
	},
	"ec2:security-group": func(ctx context.Context, c EC2DescribeAPI, ids []string) (map[string]bool, error) {
		out := map[string]bool{}
		in := &ec2.DescribeSecurityGroupsInput{Filters: idFilter("group-id", ids)}
		for {
			page, err := c.DescribeSecurityGroups(ctx, in)
			if err != nil {
				return nil, err
			}
			for _, g := range page.SecurityGroups {
				out[aws.ToString(g.GroupId)] = true
			}
			if aws.ToString(page.NextToken) == "" {
				return out, nil
			}
			in.NextToken = page.NextToken
		}
	},
	"ec2:elastic-ip": func(ctx context.Context, c EC2DescribeAPI, ids []string) (map[string]bool, error) {
		// DescribeAddresses does not paginate.
		page, err := c.DescribeAddresses(ctx, &ec2.DescribeAddressesInput{Filters: idFilter("allocation-id", ids)})
		if err != nil {
			return nil, err
		}
		out := map[string]bool{}
		for _, a := range page.Addresses {
			out[aws.ToString(a.AllocationId)] = true
		}
		return out, nil
	},
	"ec2:vpc-flow-log": func(ctx context.Context, c EC2DescribeAPI, ids []string) (map[string]bool, error) {
		out := map[string]bool{}
		in := &ec2.DescribeFlowLogsInput{Filter: idFilter("flow-log-id", ids)}
		for {
			page, err := c.DescribeFlowLogs(ctx, in)
			if err != nil {
				return nil, err
			}
			for _, f := range page.FlowLogs {
				out[aws.ToString(f.FlowLogId)] = true
			}
			if aws.ToString(page.NextToken) == "" {
				return out, nil
			}
			in.NextToken = page.NextToken
		}
	},
	"ec2:dhcp-options": func(ctx context.Context, c EC2DescribeAPI, ids []string) (map[string]bool, error) {
		out := map[string]bool{}
		in := &ec2.DescribeDhcpOptionsInput{Filters: idFilter("dhcp-options-id", ids)}
		for {
			page, err := c.DescribeDhcpOptions(ctx, in)
			if err != nil {
				return nil, err
			}
			for _, d := range page.DhcpOptions {
				out[aws.ToString(d.DhcpOptionsId)] = true
			}
			if aws.ToString(page.NextToken) == "" {
				return out, nil
			}
			in.NextToken = page.NextToken
		}
	},
	"ec2:network-acl": func(ctx context.Context, c EC2DescribeAPI, ids []string) (map[string]bool, error) {
		out := map[string]bool{}
		in := &ec2.DescribeNetworkAclsInput{Filters: idFilter("network-acl-id", ids)}
		for {
			page, err := c.DescribeNetworkAcls(ctx, in)
			if err != nil {
				return nil, err
			}
			for _, a := range page.NetworkAcls {
				out[aws.ToString(a.NetworkAclId)] = true
			}
			if aws.ToString(page.NextToken) == "" {
				return out, nil
			}
			in.NextToken = page.NextToken
		}
	},
	"ec2:launch-template": func(ctx context.Context, c EC2DescribeAPI, ids []string) (map[string]bool, error) {
		// DescribeLaunchTemplates has no id filter, and a LaunchTemplateIds list fails
		// the whole call if any one id is missing. One call per id, so a not-found
		// answers for exactly that id.
		out := map[string]bool{}
		for _, id := range ids {
			page, err := c.DescribeLaunchTemplates(ctx, &ec2.DescribeLaunchTemplatesInput{LaunchTemplateIds: []string{id}, IncludeManagedResources: aws.Bool(true)})
			if err != nil {
				if isNotFoundCode(err, "InvalidLaunchTemplateId.NotFound") {
					continue
				}
				return nil, err
			}
			for _, lt := range page.LaunchTemplates {
				out[aws.ToString(lt.LaunchTemplateId)] = true
			}
		}
		return out, nil
	},
}

func isNotFoundCode(err error, code string) bool {
	var ae smithy.APIError
	return errors.As(err, &ae) && ae.ErrorCode() == code
}

// Gone groups the ARNs by (region, type), runs one batched describe per group, and
// returns the ARNs whose id did not come back as present. Unsupported types and
// regions with no client are skipped, which the caller reads as live.
func (v *EC2ExistenceVerifier) Gone(ctx context.Context, arns []string) (map[string]bool, error) {
	type key struct{ region, rtype string }
	groups := map[key]map[string][]string{} // id -> ARNs carrying it
	for _, a := range arns {
		service, rtype := arnResourceType(a)
		if service != "ec2" || ec2Present[rtype] == nil {
			continue
		}
		region := arnRegion(a)
		if c, ok := v.EC2[region]; !ok || c == nil {
			continue
		}
		id := arnResourceID(a)
		if id == "" {
			continue
		}
		k := key{region, rtype}
		if groups[k] == nil {
			groups[k] = map[string][]string{}
		}
		groups[k][id] = append(groups[k][id], a)
	}
	// Deterministic order, so a failure names the same group every run.
	keys := make([]key, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].region != keys[j].region {
			return keys[i].region < keys[j].region
		}
		return keys[i].rtype < keys[j].rtype
	})
	gone := map[string]bool{}
	for _, k := range keys {
		ids := make([]string, 0, len(groups[k]))
		for id := range groups[k] {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for start := 0; start < len(ids); start += existenceBatch {
			end := min(start+existenceBatch, len(ids))
			chunk := ids[start:end]
			present, err := ec2Present[k.rtype](ctx, v.EC2[k.region], chunk)
			if err != nil {
				return nil, fmt.Errorf("describe %s in %s: %w", k.rtype, k.region, err)
			}
			for _, id := range chunk {
				if present[id] {
					continue
				}
				for _, a := range groups[k][id] {
					gone[a] = true
				}
			}
		}
	}
	return gone, nil
}

// errExistenceInconclusive marks a verification failure so callers can word it apart
// from a failed tag query.
var errExistenceInconclusive = errors.New("existence check inconclusive")

// withoutGhosts rebuilds r from only the ARNs v could not prove deleted, and returns
// how many entries it dropped. A nil v returns r unchanged (verification not wired).
func withoutGhosts(ctx context.Context, v ExistenceVerifier, r taggedResources) (taggedResources, int, error) {
	if v == nil || len(r.arns) == 0 {
		return r, 0, nil
	}
	gone, err := v.Gone(ctx, r.arns)
	if err != nil {
		return taggedResources{}, 0, fmt.Errorf("%w: %v", errExistenceInconclusive, err)
	}
	var live taggedResources
	ghosts := 0
	for _, a := range r.arns {
		if gone[a] {
			ghosts++
			continue
		}
		live.add(a)
	}
	return live, ghosts, nil
}

// countLiveTagged is countTaggedDetailed followed by the existence check: the tagged
// resources that still exist, plus how many index entries were verified deleted.
func countLiveTagged(ctx context.Context, d JanitorDeps, regions []string, customer string) (taggedResources, int, error) {
	r, err := countTaggedDetailed(ctx, d.Tags, regions, customer)
	if err != nil {
		return taggedResources{}, 0, err
	}
	return withoutGhosts(ctx, d.Exists, r)
}
