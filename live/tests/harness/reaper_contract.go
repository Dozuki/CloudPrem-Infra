package harness

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const engineReportSchemaVersion = 1

var (
	engineReportAccountPattern = regexp.MustCompile(`^[0-9]{12}$`)
	engineReportRegionPattern  = regexp.MustCompile(`^[a-z0-9-]+$`)
)

// EngineReport is the shared Resource Reaper producer contract. The legacy janitor
// Report remains unchanged because its Argo artifact and notification tests consume it.
type EngineReport struct {
	SchemaVersion int                 `json:"schema_version"`
	Engine        string              `json:"engine"`
	ScanID        string              `json:"scan_id"`
	ObservedAt    string              `json:"observed_at"`
	Account       string              `json:"account"`
	Status        string              `json:"status"`
	CleanupUnits  []EngineCleanupUnit `json:"cleanup_units"`
	Errors        []string            `json:"errors"`
}

type EngineCleanupUnit struct {
	FindingID            string                `json:"finding_id"`
	Version              string                `json:"version"`
	Kind                 string                `json:"kind"`
	Identity             string                `json:"identity"`
	Regions              []string              `json:"regions"`
	Decision             string                `json:"decision"`
	Reason               string                `json:"reason"`
	ExpiresAt            *string               `json:"expires_at"`
	EstimatedMonthlyCost *float64              `json:"estimated_monthly_cost"`
	Evidence             HarnessEngineEvidence `json:"evidence"`
	Capabilities         []string              `json:"capabilities"`
}

type HarnessEngineEvidence struct {
	StateBucket           string `json:"state_bucket"`
	StatePrefix           string `json:"state_prefix"`
	RunID                 string `json:"run_id"`
	ConfigName            string `json:"config_name"`
	ResourceCount         int    `json:"resource_count"`
	WorkflowPhase         string `json:"workflow_phase"`
	KeepOnFailure         bool   `json:"keep_on_failure"`
	LockPresent           bool   `json:"lock_present"`
	State                 string `json:"state"`
	SweepResult           string `json:"sweep_result,omitempty"`
	ResidueIndexLagCaveat string `json:"residueIndexLagCaveat,omitempty"`
}

func hashString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// CanonicalHarnessIdentity matches report_contract.canonical_harness_identity.
func CanonicalHarnessIdentity(stateBucket, statePrefix string) string {
	return stateBucket + "/" + strings.Trim(statePrefix, "/")
}

// FindingID is stable for one engine/account/state-prefix identity.
func FindingID(engine, account, identity string) string {
	return hashString(engine + "\n" + account + "\n" + identity)
}

// FindingVersion invalidates an action when any cleanup-relevant source field changes.
func FindingVersion(candidate Candidate, decision string) string {
	regions := candidateRegions(candidate)
	fields := []string{
		"v1",
		CanonicalHarnessIdentity(candidate.Bucket, candidate.Prefix),
		candidate.RunID,
		strings.Join(regions, ","),
		decision,
		candidate.DeleteAfter,
		strconv.Itoa(candidate.Resources),
		candidate.WorkflowPhase,
		strconv.FormatBool(candidate.KeepOnFailure),
		strconv.FormatBool(candidate.LockAge != ""),
	}
	return hashString(strings.Join(fields, "\x00"))
}

func candidateRegions(candidate Candidate) []string {
	seen := map[string]bool{}
	for _, region := range []string{candidate.Region, candidate.DRRegion} {
		if region != "" {
			seen[region] = true
		}
	}
	regions := make([]string, 0, len(seen))
	for region := range seen {
		regions = append(regions, region)
	}
	sort.Strings(regions)
	return regions
}

func decisionForCandidate(candidate Candidate) (string, []string, error) {
	if candidate.SweepResult == sweepResultDestroyed {
		return "already_gone", []string{"explain"}, nil
	}
	switch candidate.State {
	case StateActive, StateKept:
		return "protected", []string{"explain", "hold"}, nil
	case StatePending:
		return "held", []string{"explain", "hold"}, nil
	case StateClean:
		return "already_gone", []string{"explain"}, nil
	case StateOrphan:
		return "eligible", []string{"explain", "hold", "sweep"}, nil
	case StateBlocked, StateNeedsReview, StateUnknown, StateResidue:
		return "needs_review", []string{"explain", "hold"}, nil
	default:
		return "", nil, fmt.Errorf("unsupported candidate state %q", candidate.State)
	}
}

func rejectNUL(location string, values ...string) error {
	for _, value := range values {
		if strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("%s must not contain NUL", location)
		}
	}
	return nil
}

func validateContractTimestamp(value, location string) (time.Time, error) {
	if err := rejectNUL(location, value); err != nil {
		return time.Time{}, err
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be an RFC3339 timestamp: %w", location, err)
	}
	return parsed, nil
}

func regionFromStateBucket(bucket, account string) string {
	const marker = "dozuki-terraform-state-"
	suffix := "-" + account
	if !strings.HasSuffix(bucket, suffix) {
		return ""
	}
	withoutAccount := strings.TrimSuffix(bucket, suffix)
	index := strings.LastIndex(withoutAccount, marker)
	if index < 0 {
		return ""
	}
	return withoutAccount[index+len(marker):]
}

func normalizeCandidateRegions(candidate Candidate, account string) (Candidate, error) {
	if candidate.Region == "" && candidate.DRRegion == "" {
		candidate.Region = regionFromStateBucket(candidate.Bucket, account)
	}
	regions := candidateRegions(candidate)
	if len(regions) == 0 {
		return candidate, fmt.Errorf("candidate region is required")
	}
	for _, region := range regions {
		if err := rejectNUL("candidate region", region); err != nil {
			return candidate, err
		}
		if !engineReportRegionPattern.MatchString(region) {
			return candidate, fmt.Errorf("candidate region %q is invalid", region)
		}
	}
	return candidate, nil
}

// ToEngineReport maps one completed legacy janitor report into Engine Report v1.
func ToEngineReport(report Report) (EngineReport, error) {
	if report.SchemaVersion != JanitorReportSchemaVersion {
		return EngineReport{}, fmt.Errorf("unsupported janitor schema_version %d", report.SchemaVersion)
	}
	if report.Mode != "report" && report.Mode != "sweep" {
		return EngineReport{}, fmt.Errorf("unsupported janitor mode %q", report.Mode)
	}
	if !engineReportAccountPattern.MatchString(report.Account) {
		return EngineReport{}, fmt.Errorf("account must be a 12-digit string")
	}
	observed, err := validateContractTimestamp(report.At, "observed_at")
	if err != nil {
		return EngineReport{}, err
	}
	if err := rejectNUL("report", report.Account, report.Mode); err != nil {
		return EngineReport{}, err
	}

	legacyJSON, err := json.Marshal(report)
	if err != nil {
		return EngineReport{}, fmt.Errorf("marshal janitor report for scan_id: %w", err)
	}
	scanDigest := sha256.Sum256(legacyJSON)
	scanID := "harness-" + observed.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(scanDigest[:6])

	result := EngineReport{
		SchemaVersion: engineReportSchemaVersion,
		Engine:        "harness",
		ScanID:        scanID,
		ObservedAt:    report.At,
		Account:       report.Account,
		Status:        "ok",
		CleanupUnits:  make([]EngineCleanupUnit, 0, len(report.Candidates)),
		Errors:        []string{},
	}
	if report.Failed > 0 || report.Residue > 0 || report.Inconclusive > 0 {
		result.Status = "degraded"
	}

	seen := map[string]bool{}
	for index, source := range report.Candidates {
		candidate, regionErr := normalizeCandidateRegions(source, report.Account)
		if regionErr != nil {
			return EngineReport{}, fmt.Errorf("candidate %d: %w", index, regionErr)
		}
		if err := rejectNUL(
			fmt.Sprintf("candidate %d", index),
			candidate.Bucket,
			candidate.Prefix,
			candidate.RunID,
			candidate.ConfigName,
			candidate.Identifier,
			candidate.DeleteAfter,
			string(candidate.State),
			candidate.Reason,
			candidate.WorkflowPhase,
			candidate.LockAge,
			candidate.SweepResult,
		); err != nil {
			return EngineReport{}, err
		}
		cleanPrefix := strings.Trim(candidate.Prefix, "/")
		if candidate.Bucket == "" || cleanPrefix == "" {
			return EngineReport{}, fmt.Errorf("candidate %d state_bucket and state_prefix must be non-empty", index)
		}
		if candidate.Resources < 0 {
			return EngineReport{}, fmt.Errorf("candidate %d resource_count must be non-negative", index)
		}
		decision, capabilities, mapErr := decisionForCandidate(candidate)
		if mapErr != nil {
			return EngineReport{}, fmt.Errorf("candidate %d: %w", index, mapErr)
		}
		if candidate.State == StateUnknown || candidate.State == StateResidue {
			result.Status = "degraded"
		}
		var expiresAt *string
		if candidate.DeleteAfter != "" {
			if _, err := validateContractTimestamp(candidate.DeleteAfter, "expires_at"); err != nil {
				return EngineReport{}, fmt.Errorf("candidate %d: %w", index, err)
			}
			expiry := candidate.DeleteAfter
			expiresAt = &expiry
		}
		identity := CanonicalHarnessIdentity(candidate.Bucket, candidate.Prefix)
		findingID := FindingID("harness", report.Account, identity)
		if seen[findingID] {
			return EngineReport{}, fmt.Errorf("duplicate finding_id %s", findingID)
		}
		seen[findingID] = true
		configName := candidate.ConfigName
		if configName == "" {
			configName = "unknown"
		}
		evidence := HarnessEngineEvidence{
			StateBucket:   candidate.Bucket,
			StatePrefix:   candidate.Prefix,
			RunID:         candidate.RunID,
			ConfigName:    configName,
			ResourceCount: candidate.Resources,
			WorkflowPhase: candidate.WorkflowPhase,
			KeepOnFailure: candidate.KeepOnFailure,
			LockPresent:   candidate.LockAge != "",
			State:         string(candidate.State),
			SweepResult:   candidate.SweepResult,
		}
		if candidate.State == StateResidue {
			evidence.ResidueIndexLagCaveat = residueIndexLagCaveat
		}
		result.CleanupUnits = append(result.CleanupUnits, EngineCleanupUnit{
			FindingID:            findingID,
			Version:              FindingVersion(candidate, decision),
			Kind:                 "harness-stack",
			Identity:             identity,
			Regions:              candidateRegions(candidate),
			Decision:             decision,
			Reason:               candidate.Reason,
			ExpiresAt:            expiresAt,
			EstimatedMonthlyCost: nil,
			Evidence:             evidence,
			Capabilities:         capabilities,
		})
	}
	return result, nil
}

// EngineReportWriter is the create-only S3 surface used by the shadow producer.
type EngineReportWriter interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

// WriteEngineReport uploads one immutable report under the shared ingestion prefix.
func WriteEngineReport(ctx context.Context, writer EngineReportWriter, bucket string, report EngineReport) (string, error) {
	if writer == nil {
		return "", fmt.Errorf("engine report writer is required")
	}
	if bucket == "" {
		return "", fmt.Errorf("engine report bucket is required")
	}
	if err := rejectNUL("engine report bucket", bucket); err != nil {
		return "", err
	}
	observed, err := validateContractTimestamp(report.ObservedAt, "observed_at")
	if err != nil {
		return "", err
	}
	if report.Engine != "harness" || report.ScanID == "" {
		return "", fmt.Errorf("engine report identity is invalid")
	}
	body, err := json.Marshal(report)
	if err != nil {
		return "", fmt.Errorf("marshal engine report: %w", err)
	}
	key := fmt.Sprintf(
		"engine-reports/harness/%s/%s.json",
		observed.UTC().Format("2006-01-02"),
		report.ScanID,
	)
	_, err = writer.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String("application/json"),
		IfNoneMatch: aws.String("*"),
	})
	if err != nil {
		return "", fmt.Errorf("write immutable engine report s3://%s/%s: %w", bucket, key, err)
	}
	return key, nil
}
