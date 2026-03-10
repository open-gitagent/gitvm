package firecracker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
)

// FirecrackerClient talks to the Firecracker REST API over a Unix socket.
type FirecrackerClient struct {
	client     *http.Client
	socketPath string
}

// NewFirecrackerClient creates a client for the given socket path.
func NewFirecrackerClient(socketPath string) *FirecrackerClient {
	return &FirecrackerClient{
		socketPath: socketPath,
		client: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return net.Dial("unix", socketPath)
				},
			},
		},
	}
}

// --- Firecracker API types ---

type MachineConfiguration struct {
	VCPUCount  int  `json:"vcpu_count"`
	MemSizeMib int  `json:"mem_size_mib"`
	HtEnabled  bool `json:"ht_enabled"`
}

type BootSource struct {
	KernelImagePath string `json:"kernel_image_path"`
	BootArgs        string `json:"boot_args"`
}

type Drive struct {
	DriveID      string `json:"drive_id"`
	PathOnHost   string `json:"path_on_host"`
	IsRootDevice bool   `json:"is_root_device"`
	IsReadOnly   bool   `json:"is_read_only"`
}

type NetworkInterface struct {
	IfaceID     string `json:"iface_id"`
	GuestMAC    string `json:"guest_mac,omitempty"`
	HostDevName string `json:"host_dev_name"`
}

type InstanceAction struct {
	ActionType string `json:"action_type"`
}

// --- API calls ---

// SetMachineConfig configures vCPU and memory.
func (fc *FirecrackerClient) SetMachineConfig(cfg MachineConfiguration) error {
	return fc.put("/machine-config", cfg)
}

// SetBootSource configures the kernel and boot args.
func (fc *FirecrackerClient) SetBootSource(src BootSource) error {
	return fc.put("/boot-source", src)
}

// AddDrive adds a block device (rootfs).
func (fc *FirecrackerClient) AddDrive(drive Drive) error {
	return fc.put(fmt.Sprintf("/drives/%s", drive.DriveID), drive)
}

// AddNetworkInterface adds a network interface.
func (fc *FirecrackerClient) AddNetworkInterface(iface NetworkInterface) error {
	return fc.put(fmt.Sprintf("/network-interfaces/%s", iface.IfaceID), iface)
}

// StartInstance boots the VM.
func (fc *FirecrackerClient) StartInstance() error {
	return fc.put("/actions", InstanceAction{ActionType: "InstanceStart"})
}

// --- HTTP helpers ---

func (fc *FirecrackerClient) put(path string, body interface{}) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequest(http.MethodPut, "http://localhost"+path, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := fc.client.Do(req)
	if err != nil {
		return fmt.Errorf("request %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("firecracker %s returned %d: %s", path, resp.StatusCode, string(body))
	}

	return nil
}
