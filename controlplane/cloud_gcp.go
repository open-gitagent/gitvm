package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
)

// GCPProvider provisions GCE instances using the gcloud CLI.
type GCPProvider struct {
	ProjectID   string
	Zone        string
	Network     string
	Subnet      string
	MachineImage string // e.g., "projects/ubuntu-os-cloud/global/images/family/ubuntu-2204-lts"
}

func (p *GCPProvider) Name() string { return "gcp" }

func (p *GCPProvider) ProvisionNode(ctx context.Context, opts ProvisionOpts) (*ProvisionResult, error) {
	zone := opts.Region
	if zone == "" {
		zone = p.Zone
	}

	machineType := opts.InstanceType
	isFirecracker := opts.Runtime != "docker"
	if machineType == "" {
		if isFirecracker {
			machineType = "n2-standard-16" // 16 vCPU, 64 GB, nested virt capable
		} else {
			machineType = "e2-standard-4" // 4 vCPU, 16 GB — Docker only
		}
	}

	args := []string{
		"compute", "instances", "create", opts.Name,
		"--project", p.ProjectID,
		"--zone", zone,
		"--machine-type", machineType,
		"--image", p.MachineImage,
		"--metadata", "startup-script=" + opts.UserData,
		"--labels", "gitvm=node",
		"--format", "json",
	}
	// Firecracker requires nested virtualization
	if isFirecracker {
		args = append(args, "--min-cpu-platform", "Intel Cascade Lake")
		args = append(args, "--enable-nested-virtualization")
	}
	if p.Network != "" {
		args = append(args, "--network", p.Network)
	}
	if p.Subnet != "" {
		args = append(args, "--subnet", p.Subnet)
	}

	cmd := exec.CommandContext(ctx, "gcloud", args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gcloud compute instances create: %w", err)
	}

	var instances []struct {
		Name              string `json:"name"`
		NetworkInterfaces []struct {
			NetworkIP    string `json:"networkIP"`
			AccessConfigs []struct {
				NatIP string `json:"natIP"`
			} `json:"accessConfigs"`
		} `json:"networkInterfaces"`
	}
	if err := json.Unmarshal(out, &instances); err != nil {
		return nil, fmt.Errorf("parse gcloud response: %w", err)
	}
	if len(instances) == 0 {
		return nil, fmt.Errorf("no instances created")
	}

	inst := instances[0]
	publicIP := ""
	privateIP := ""
	if len(inst.NetworkInterfaces) > 0 {
		privateIP = inst.NetworkInterfaces[0].NetworkIP
		if len(inst.NetworkInterfaces[0].AccessConfigs) > 0 {
			publicIP = inst.NetworkInterfaces[0].AccessConfigs[0].NatIP
		}
	}

	return &ProvisionResult{
		ProviderID:   inst.Name,
		PublicIP:     publicIP,
		PrivateIP:    privateIP,
		Region:       zone,
		InstanceType: machineType,
	}, nil
}

func (p *GCPProvider) TerminateNode(ctx context.Context, providerID string) error {
	cmd := exec.CommandContext(ctx, "gcloud", "compute", "instances", "delete", providerID,
		"--project", p.ProjectID,
		"--zone", p.Zone,
		"--quiet",
	)
	return cmd.Run()
}

func (p *GCPProvider) ListNodes(ctx context.Context) ([]ProvisionResult, error) {
	cmd := exec.CommandContext(ctx, "gcloud", "compute", "instances", "list",
		"--project", p.ProjectID,
		"--filter", "labels.gitvm=node AND status=RUNNING",
		"--format", "json",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gcloud list: %w", err)
	}

	var instances []struct {
		Name              string `json:"name"`
		MachineType       string `json:"machineType"`
		Zone              string `json:"zone"`
		NetworkInterfaces []struct {
			NetworkIP    string `json:"networkIP"`
			AccessConfigs []struct {
				NatIP string `json:"natIP"`
			} `json:"accessConfigs"`
		} `json:"networkInterfaces"`
	}
	if err := json.Unmarshal(out, &instances); err != nil {
		return nil, err
	}

	var results []ProvisionResult
	for _, inst := range instances {
		r := ProvisionResult{ProviderID: inst.Name, InstanceType: inst.MachineType, Region: inst.Zone}
		if len(inst.NetworkInterfaces) > 0 {
			r.PrivateIP = inst.NetworkInterfaces[0].NetworkIP
			if len(inst.NetworkInterfaces[0].AccessConfigs) > 0 {
				r.PublicIP = inst.NetworkInterfaces[0].AccessConfigs[0].NatIP
			}
		}
		results = append(results, r)
	}
	return results, nil
}
