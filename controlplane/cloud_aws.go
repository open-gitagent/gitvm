package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

// AWSProvider provisions EC2 instances using the AWS CLI.
// No AWS SDK dependency — just shells out to `aws` CLI.
// Automatically creates security group and key pair if not provided.
type AWSProvider struct {
	AccessKeyID    string
	SecretAccessKey string
	DefaultRegion  string
	SecurityGroupID string
	SubnetID       string
	KeyName        string
	AMI            string // Ubuntu AMI (auto-resolved if empty)

	bootstrapOnce sync.Once
	bootstrapErr  error
}

func (p *AWSProvider) Name() string { return "aws" }

// bootstrap ensures a security group and AMI exist, creating them if needed.
func (p *AWSProvider) bootstrap(ctx context.Context, region string) error {
	p.bootstrapOnce.Do(func() {
		p.bootstrapErr = p.doBootstrap(ctx, region)
	})
	return p.bootstrapErr
}

func (p *AWSProvider) doBootstrap(ctx context.Context, region string) error {
	// 1. Auto-create security group if not provided
	if p.SecurityGroupID == "" {
		sgID, err := p.ensureSecurityGroup(ctx, region)
		if err != nil {
			return fmt.Errorf("auto-create security group: %w", err)
		}
		p.SecurityGroupID = sgID
	}

	// 2. Auto-resolve AMI if not provided
	if p.AMI == "" {
		ami, err := p.resolveUbuntuAMI(ctx, region)
		if err != nil {
			return fmt.Errorf("auto-resolve AMI: %w", err)
		}
		p.AMI = ami
	}

	return nil
}

// ensureSecurityGroup finds or creates the "gitvm-node" security group.
func (p *AWSProvider) ensureSecurityGroup(ctx context.Context, region string) (string, error) {
	// Check if it already exists
	cmd := exec.CommandContext(ctx, "aws", "ec2", "describe-security-groups",
		"--filters", "Name=group-name,Values=gitvm-node",
		"--region", region,
		"--output", "json",
	)
	cmd.Env = p.env()
	out, err := cmd.Output()
	if err == nil {
		var resp struct {
			SecurityGroups []struct {
				GroupId string `json:"GroupId"`
			} `json:"SecurityGroups"`
		}
		if json.Unmarshal(out, &resp) == nil && len(resp.SecurityGroups) > 0 {
			return resp.SecurityGroups[0].GroupId, nil
		}
	}

	// Create it
	cmd = exec.CommandContext(ctx, "aws", "ec2", "create-security-group",
		"--group-name", "gitvm-node",
		"--description", "gitvm node - SSH, node API, and agent ports",
		"--region", region,
		"--output", "json",
	)
	cmd.Env = p.env()
	out, err = cmd.Output()
	if err != nil {
		return "", fmt.Errorf("create security group: %w", err)
	}

	var sg struct {
		GroupId string `json:"GroupId"`
	}
	if err := json.Unmarshal(out, &sg); err != nil {
		return "", err
	}

	// Add inbound rules: SSH (22), node API (9090)
	for _, port := range []string{"22", "9090"} {
		cmd = exec.CommandContext(ctx, "aws", "ec2", "authorize-security-group-ingress",
			"--group-id", sg.GroupId,
			"--protocol", "tcp",
			"--port", port,
			"--cidr", "0.0.0.0/0",
			"--region", region,
		)
		cmd.Env = p.env()
		cmd.Run() // ignore error if rule already exists
	}

	return sg.GroupId, nil
}

// resolveUbuntuAMI finds the latest Ubuntu 24.04 LTS AMI.
func (p *AWSProvider) resolveUbuntuAMI(ctx context.Context, region string) (string, error) {
	cmd := exec.CommandContext(ctx, "aws", "ec2", "describe-images",
		"--region", region,
		"--owners", "099720109477", // Canonical
		"--filters",
		"Name=name,Values=ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-*",
		"Name=state,Values=available",
		"--query", "sort_by(Images, &CreationDate)[-1].ImageId",
		"--output", "text",
	)
	cmd.Env = p.env()
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolve AMI: %w", err)
	}
	ami := strings.TrimSpace(string(out))
	if ami == "" || ami == "None" {
		return "", fmt.Errorf("no Ubuntu 24.04 AMI found in %s", region)
	}
	return ami, nil
}

func (p *AWSProvider) ProvisionNode(ctx context.Context, opts ProvisionOpts) (*ProvisionResult, error) {
	region := opts.Region
	if region == "" {
		region = p.DefaultRegion
	}

	// Auto-create security group and resolve AMI if needed
	if err := p.bootstrap(ctx, region); err != nil {
		return nil, fmt.Errorf("bootstrap: %w", err)
	}

	instanceType := opts.InstanceType
	if instanceType == "" {
		switch opts.Runtime {
		case "docker":
			instanceType = "t3.medium" // 2 vCPU, 4 GB — no KVM needed
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
		"--associate-public-ip-address",
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
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("aws ec2 run-instances: %w: %s", err, string(out))
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

	// Public IP may not be assigned yet — wait for it
	if inst.PublicIpAddress == "" {
		ip, err := p.waitForPublicIP(ctx, inst.InstanceId, region)
		if err == nil {
			inst.PublicIpAddress = ip
		}
	}

	return &ProvisionResult{
		ProviderID:   inst.InstanceId,
		PublicIP:     inst.PublicIpAddress,
		PrivateIP:    inst.PrivateIpAddress,
		Region:       region,
		InstanceType: instanceType,
	}, nil
}

// waitForPublicIP polls until the instance has a public IP (up to ~60s).
func (p *AWSProvider) waitForPublicIP(ctx context.Context, instanceID, region string) (string, error) {
	// First wait for instance to be running
	waitCmd := exec.CommandContext(ctx, "aws", "ec2", "wait", "instance-running",
		"--instance-ids", instanceID,
		"--region", region,
	)
	waitCmd.Env = p.env()
	waitCmd.Run()

	// Then get the public IP
	cmd := exec.CommandContext(ctx, "aws", "ec2", "describe-instances",
		"--instance-ids", instanceID,
		"--region", region,
		"--query", "Reservations[0].Instances[0].PublicIpAddress",
		"--output", "text",
	)
	cmd.Env = p.env()
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	ip := strings.TrimSpace(string(out))
	if ip == "" || ip == "None" {
		return "", fmt.Errorf("no public IP assigned")
	}
	return ip, nil
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
	return []string{
		"AWS_ACCESS_KEY_ID=" + p.AccessKeyID,
		"AWS_SECRET_ACCESS_KEY=" + p.SecretAccessKey,
		"AWS_DEFAULT_REGION=" + p.DefaultRegion,
	}
}
