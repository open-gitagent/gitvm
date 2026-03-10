package controlplane

import (
	"context"
	"fmt"
)

// CloudProvider is the interface for provisioning nodes on any cloud.
type CloudProvider interface {
	// Name returns the provider identifier (e.g., "aws", "gcp", "azure").
	Name() string

	// ProvisionNode creates a new cloud VM and returns its info.
	// The VM should be configured to run gitvm-node on boot.
	ProvisionNode(ctx context.Context, opts ProvisionOpts) (*ProvisionResult, error)

	// TerminateNode destroys a cloud VM by its provider-specific ID.
	TerminateNode(ctx context.Context, providerID string) error

	// ListNodes returns all gitvm nodes from this provider.
	ListNodes(ctx context.Context) ([]ProvisionResult, error)
}

// ProvisionOpts are the options for creating a cloud node.
type ProvisionOpts struct {
	Name         string
	Region       string
	InstanceType string // if empty, auto-selected based on Runtime
	Runtime      string // "firecracker" (default) or "docker"
	// UserData/startup script that installs and starts gitvm-node
	UserData string
	Tags     map[string]string
}

// ProvisionResult is returned after a node is provisioned.
type ProvisionResult struct {
	ProviderID   string // cloud instance ID
	PublicIP     string
	PrivateIP    string
	Region       string
	InstanceType string
}

// CloudProviderRegistry holds all registered cloud providers.
type CloudProviderRegistry struct {
	providers map[string]CloudProvider
}

func NewCloudProviderRegistry() *CloudProviderRegistry {
	return &CloudProviderRegistry{
		providers: make(map[string]CloudProvider),
	}
}

func (r *CloudProviderRegistry) Register(p CloudProvider) {
	r.providers[p.Name()] = p
}

func (r *CloudProviderRegistry) Get(name string) (CloudProvider, error) {
	p, ok := r.providers[name]
	if !ok {
		return nil, fmt.Errorf("cloud provider %q not registered", name)
	}
	return p, nil
}

func (r *CloudProviderRegistry) List() []string {
	var names []string
	for name := range r.providers {
		names = append(names, name)
	}
	return names
}

// NodeUserData generates the startup script for a new node.
// runtime should be "firecracker" (default) or "docker".
func NodeUserData(controlPlaneURL, nodeKey, nodeName, runtime string) string {
	if runtime == "" {
		runtime = "firecracker"
	}

	// Firecracker needs extra host setup
	fcSetup := ""
	if runtime == "firecracker" {
		fcSetup = `
# Enable KVM
modprobe kvm_intel || modprobe kvm_amd || true
chmod 666 /dev/kvm

# Hugepages (allocate 60% of RAM)
TOTAL_MEM_KB=$(grep MemTotal /proc/meminfo | awk '{print $2}')
HUGEPAGES=$((TOTAL_MEM_KB * 60 / 100 / 2048))
echo $HUGEPAGES > /proc/sys/vm/nr_hugepages

# System limits
sysctl -w vm.max_map_count=1048576
sysctl -w net.core.somaxconn=65535
ulimit -n 1048576
`
	}

	dockerSetup := ""
	if runtime == "docker" {
		dockerSetup = `
# Install Docker
curl -fsSL https://get.docker.com | bash
systemctl enable docker && systemctl start docker
`
	}

	return fmt.Sprintf(`#!/bin/bash
set -ex

%s%s

# Install Go 1.24 (apt has older version)
apt-get update -y && apt-get install -y git curl
curl -fsSL https://go.dev/dl/go1.24.1.linux-amd64.tar.gz | tar -C /usr/local -xzf -
export PATH=/usr/local/go/bin:$PATH
export GOPATH=/usr/local/gopath
export PATH=$PATH:$GOPATH/bin

# Install gitvm from GitHub
go install github.com/open-gitagent/gitvm/cmd/gitvm-node@latest
go install github.com/open-gitagent/gitvm/cmd/gitvm@latest

# Start gitvm-node
GITVM_RUNTIME=%s exec $GOPATH/bin/gitvm-node \
  --control-plane %s \
  --node-key %s \
  --name %s
`, fcSetup, dockerSetup, runtime, controlPlaneURL, nodeKey, nodeName)
}
