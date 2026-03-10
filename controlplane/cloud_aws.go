package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// AWSProvider provisions EC2 instances using the AWS CLI.
// No AWS SDK dependency — just shells out to `aws` CLI.
type AWSProvider struct {
	AccessKeyID     string
	SecretAccessKey  string
	DefaultRegion   string
	SecurityGroupID string
	SubnetID        string
	KeyName         string
	AMI             string // Amazon Linux 2 or Ubuntu with KVM support
}

func (p *AWSProvider) Name() string { return "aws" }

func (p *AWSProvider) ProvisionNode(ctx context.Context, opts ProvisionOpts) (*ProvisionResult, error) {
	region := opts.Region
	if region == "" {
		region = p.DefaultRegion
	}

	instanceType := opts.InstanceType
	if instanceType == "" {
		switch opts.Runtime {
		case "docker":
			instanceType = "t3.xlarge" // 4 vCPU, 16 GB — no KVM needed
		default: // "firecracker" or empty
			instanceType = "c8i.xlarge" // 4 vCPU, 8 GB, nested virt (KVM)
		}
	}

	tags := fmt.Sprintf("ResourceType=instance,Tags=[{Key=Name,Value=%s},{Key=gitvm,Value=node}]", opts.Name)

	args := []string{
		"ec2", "run-instances",
		"--region", region,
		"--instance-type", instanceType,
		"--image-id", p.AMI,
		"--count", "1",
		"--tag-specifications", tags,
		"--user-data", opts.UserData,
		"--output", "json",
	}
	if p.SecurityGroupID != "" {
		args = append(args, "--security-group-ids", p.SecurityGroupID)
	}
	if p.SubnetID != "" {
		args = append(args, "--subnet-id", p.SubnetID)
	}
	if p.KeyName != "" {
		args = append(args, "--key-name", p.KeyName)
	}

	cmd := exec.CommandContext(ctx, "aws", args...)
	cmd.Env = p.env()
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("aws ec2 run-instances: %w", err)
	}

	var result struct {
		Instances []struct {
			InstanceId       string `json:"InstanceId"`
			PublicIpAddress  string `json:"PublicIpAddress"`
			PrivateIpAddress string `json:"PrivateIpAddress"`
		} `json:"Instances"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("parse aws response: %w", err)
	}
	if len(result.Instances) == 0 {
		return nil, fmt.Errorf("no instances created")
	}

	inst := result.Instances[0]
	return &ProvisionResult{
		ProviderID:   inst.InstanceId,
		PublicIP:     inst.PublicIpAddress,
		PrivateIP:    inst.PrivateIpAddress,
		Region:       region,
		InstanceType: instanceType,
	}, nil
}

func (p *AWSProvider) TerminateNode(ctx context.Context, providerID string) error {
	cmd := exec.CommandContext(ctx, "aws", "ec2", "terminate-instances",
		"--instance-ids", providerID,
		"--region", p.DefaultRegion,
	)
	cmd.Env = p.env()
	return cmd.Run()
}

func (p *AWSProvider) ListNodes(ctx context.Context) ([]ProvisionResult, error) {
	cmd := exec.CommandContext(ctx, "aws", "ec2", "describe-instances",
		"--filters", "Name=tag:gitvm,Values=node", "Name=instance-state-name,Values=running",
		"--region", p.DefaultRegion,
		"--output", "json",
	)
	cmd.Env = p.env()
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("aws describe-instances: %w", err)
	}

	var resp struct {
		Reservations []struct {
			Instances []struct {
				InstanceId       string `json:"InstanceId"`
				PublicIpAddress  string `json:"PublicIpAddress"`
				PrivateIpAddress string `json:"PrivateIpAddress"`
				InstanceType     string `json:"InstanceType"`
				Placement        struct {
					AvailabilityZone string `json:"AvailabilityZone"`
				} `json:"Placement"`
			} `json:"Instances"`
		} `json:"Reservations"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, err
	}

	var results []ProvisionResult
	for _, r := range resp.Reservations {
		for _, i := range r.Instances {
			results = append(results, ProvisionResult{
				ProviderID:   i.InstanceId,
				PublicIP:     i.PublicIpAddress,
				PrivateIP:    i.PrivateIpAddress,
				Region:       strings.TrimRight(i.Placement.AvailabilityZone, "abcdef"),
				InstanceType: i.InstanceType,
			})
		}
	}
	return results, nil
}

func (p *AWSProvider) env() []string {
	env := []string{
		"AWS_ACCESS_KEY_ID=" + p.AccessKeyID,
		"AWS_SECRET_ACCESS_KEY=" + p.SecretAccessKey,
		"AWS_DEFAULT_REGION=" + p.DefaultRegion,
	}
	return env
}
