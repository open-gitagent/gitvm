package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
)

// AzureProvider provisions Azure VMs using the az CLI.
type AzureProvider struct {
	SubscriptionID string
	ResourceGroup  string
	Location       string
	Image          string // e.g., "Canonical:0001-com-ubuntu-server-jammy:22_04-lts:latest"
	VNet           string
	Subnet         string
}

func (p *AzureProvider) Name() string { return "azure" }

func (p *AzureProvider) ProvisionNode(ctx context.Context, opts ProvisionOpts) (*ProvisionResult, error) {
	location := opts.Region
	if location == "" {
		location = p.Location
	}

	vmSize := opts.InstanceType
	if vmSize == "" {
		switch opts.Runtime {
		case "docker":
			vmSize = "Standard_B4ms" // 4 vCPU, 16 GB — Docker only
		default: // "firecracker" or empty
			vmSize = "Standard_D16s_v5" // 16 vCPU, 64 GB, nested virt
		}
	}

	args := []string{
		"vm", "create",
		"--resource-group", p.ResourceGroup,
		"--name", opts.Name,
		"--location", location,
		"--size", vmSize,
		"--image", p.Image,
		"--custom-data", opts.UserData,
		"--tags", "gitvm=node",
		"--output", "json",
	}
	if p.VNet != "" {
		args = append(args, "--vnet-name", p.VNet)
	}
	if p.Subnet != "" {
		args = append(args, "--subnet", p.Subnet)
	}

	cmd := exec.CommandContext(ctx, "az", args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("az vm create: %w", err)
	}

	var result struct {
		ID                string `json:"id"`
		PublicIpAddress    string `json:"publicIpAddress"`
		PrivateIpAddress   string `json:"privateIpAddress"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("parse az response: %w", err)
	}

	return &ProvisionResult{
		ProviderID:   opts.Name,
		PublicIP:     result.PublicIpAddress,
		PrivateIP:    result.PrivateIpAddress,
		Region:       location,
		InstanceType: vmSize,
	}, nil
}

func (p *AzureProvider) TerminateNode(ctx context.Context, providerID string) error {
	cmd := exec.CommandContext(ctx, "az", "vm", "delete",
		"--resource-group", p.ResourceGroup,
		"--name", providerID,
		"--yes",
	)
	return cmd.Run()
}

func (p *AzureProvider) ListNodes(ctx context.Context) ([]ProvisionResult, error) {
	cmd := exec.CommandContext(ctx, "az", "vm", "list",
		"--resource-group", p.ResourceGroup,
		"--query", "[?tags.gitvm=='node']",
		"--show-details",
		"--output", "json",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("az vm list: %w", err)
	}

	var vms []struct {
		Name             string `json:"name"`
		PublicIps        string `json:"publicIps"`
		PrivateIps       string `json:"privateIps"`
		HardwareProfile  struct {
			VMSize string `json:"vmSize"`
		} `json:"hardwareProfile"`
		Location string `json:"location"`
	}
	if err := json.Unmarshal(out, &vms); err != nil {
		return nil, err
	}

	var results []ProvisionResult
	for _, vm := range vms {
		results = append(results, ProvisionResult{
			ProviderID:   vm.Name,
			PublicIP:     vm.PublicIps,
			PrivateIP:    vm.PrivateIps,
			Region:       vm.Location,
			InstanceType: vm.HardwareProfile.VMSize,
		})
	}
	return results, nil
}
