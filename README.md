<p align="center">
  <h1 align="center">gitvm</h1>
  <p align="center">Self-hosted sandbox platform with Firecracker & Docker runtimes</p>
</p>

<p align="center">
  <a href="#quickstart">Quickstart</a> &middot;
  <a href="#architecture">Architecture</a> &middot;
  <a href="#sdk">SDK</a> &middot;
  <a href="#cloud-deployment">Cloud Deployment</a> &middot;
  <a href="#api-reference">API</a>
</p>

---

**gitvm** is a self-hosted alternative to [E2B](https://e2b.dev) for running isolated sandbox environments. Give your AI agents a full Linux VM to write code, run tests, and execute commands — on your own infrastructure.

```
SDK → REST API → Orchestrator → Firecracker VM or Docker Container → Guest Agent
```

## Quickstart

### Install

```bash
go install github.com/open-gitagent/gitvm/cmd/gitvm@latest
```

### Start the server

```bash
gitvm server start --port 8080
```

### Create and use a sandbox

```bash
# Create a sandbox
gitvm vm create --template ubuntu:22.04 --vcpus 2 --memory 1024

# Execute a command
gitvm vm exec <sandbox-id> "echo hello world"

# List running sandboxes
gitvm vm list

# Stop a sandbox
gitvm vm stop <sandbox-id>
```

### Use as a Go SDK

```go
package main

import (
    "context"
    "fmt"

    "github.com/open-gitagent/gitvm/sdk"
)

func main() {
    machine := sdk.NewGitVMMachine(sdk.GitVMConfig{
        ServerURL: "http://localhost:8080",
        Template:  "ubuntu:22.04",
        VCPUs:     2,
        MemoryMB:  1024,
    })

    ctx := context.Background()
    machine.Start(ctx)
    defer machine.Stop(ctx)

    result, _ := machine.Execute(ctx, "cat /etc/os-release", nil)
    fmt.Println(result.Stdout)
}
```

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                     Control Plane (gitvmd)                   │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌────────────┐  │
│  │ REST API │  │ Scaler   │  │ Auth     │  │ Dashboard  │  │
│  └────┬─────┘  └────┬─────┘  └──────────┘  └────────────┘  │
│       │              │                                       │
│       │    ┌─────────┴──────────┐                           │
│       │    │  Cloud Providers   │                           │
│       │    │  AWS · GCP · Azure │                           │
│       │    └────────────────────┘                           │
└───────┼─────────────────────────────────────────────────────┘
        │
        ▼
┌─────────────────────────────────────────────────────────────┐
│                    Node (gitvm-node)                         │
│  ┌──────────────┐  ┌────────────────┐  ┌────────────────┐  │
│  │ Orchestrator │  │ Docker Runtime │  │ FC Runtime     │  │
│  │              │  │                │  │ (Firecracker)  │  │
│  └──────┬───────┘  └───────┬────────┘  └───────┬────────┘  │
│         │                  │                    │            │
│         ▼                  ▼                    ▼            │
│  ┌────────────┐     ┌───────────┐       ┌───────────┐      │
│  │ Sandbox 1  │     │ Sandbox 2 │  ...  │ Sandbox N │      │
│  │ ┌────────┐ │     │ ┌────────┐│       │ ┌────────┐│      │
│  │ │ Agent  │ │     │ │ Agent  ││       │ │ Agent  ││      │
│  │ └────────┘ │     │ └────────┘│       │ └────────┘│      │
│  └────────────┘     └───────────┘       └───────────┘      │
└─────────────────────────────────────────────────────────────┘
```

### Three binaries

| Binary | Role | Runs on |
|--------|------|---------|
| `gitvmd` | Control plane — manages nodes, projects, API keys, auto-scaling | Central server |
| `gitvm-node` | Node agent — manages sandboxes on a single host | Each cloud VM |
| `gitvm` | CLI + guest agent — user-facing commands + in-sandbox daemon | Everywhere |

### Pluggable runtimes

| | Docker | Firecracker |
|---|---|---|
| **Isolation** | Container-level | Hardware VM (KVM) |
| **Startup** | ~1s | ~125ms |
| **Overhead** | Shared kernel | Dedicated kernel |
| **Requirements** | Docker installed | KVM (`/dev/kvm`) |
| **Snapshots** | `docker commit` | VM snapshots |
| **Best for** | Dev/testing, Mac | Production, Linux |

## Features

- **Sandbox lifecycle** — create, pause, resume, stop, destroy
- **Command execution** — run any command inside a sandbox, get stdout/stderr/exit code
- **Filesystem access** — read, write, list, mkdir, delete files inside sandboxes
- **Snapshots** — freeze a sandbox, restore it later with all state preserved
- **Templates** — build custom rootfs images with your tools pre-installed
- **Auto-scaling** — control plane scales nodes up/down based on capacity
- **Multi-cloud** — provision nodes on AWS, GCP, or Azure
- **Runtime-aware provisioning** — auto-selects instance types (KVM-capable for Firecracker, standard for Docker)
- **SDK** — Go client implementing `gitmachine-go` Machine interface

## Cloud Deployment

The control plane auto-provisions nodes on AWS, GCP, or Azure. Each node installs gitvm from GitHub on boot:

```bash
go install github.com/open-gitagent/gitvm/cmd/gitvm-node@latest
go install github.com/open-gitagent/gitvm/cmd/gitvm@latest
```

### Default instance types

| Provider | Firecracker | Docker |
|----------|-------------|--------|
| **AWS** | `c8i.xlarge` (4 vCPU, 8 GB) | `t3.xlarge` (4 vCPU, 16 GB) |
| **GCP** | `n2-standard-16` + nested virt | `e2-standard-4` |
| **Azure** | `Standard_D16s_v5` | `Standard_B4ms` |

### Start the control plane

```bash
gitvmd start \
  --port 7070 \
  --node-key "your-shared-secret" \
  --aws-access-key $AWS_ACCESS_KEY_ID \
  --aws-secret-key $AWS_SECRET_ACCESS_KEY \
  --aws-region us-east-1
```

## API Reference

All endpoints are served by `gitvm-node` (port 9090) or `gitvm server` (port 8080).

### Sandboxes

```
POST   /sandboxes                      Create sandbox
GET    /sandboxes                      List sandboxes
GET    /sandboxes/:id                  Get sandbox
DELETE /sandboxes/:id                  Delete sandbox
POST   /sandboxes/:id/start           Start stopped sandbox
POST   /sandboxes/:id/stop            Stop sandbox
POST   /sandboxes/:id/pause           Pause sandbox
POST   /sandboxes/:id/resume          Resume sandbox
```

### Execution

```
POST   /sandboxes/:id/exec            Execute command
```

```json
{ "command": "echo hello", "cwd": "/home", "timeout": 30 }
```

```json
{ "exitCode": 0, "stdout": "hello\n", "stderr": "" }
```

### Files

```
GET    /sandboxes/:id/files?path=     Read file
PUT    /sandboxes/:id/files?path=     Write file
GET    /sandboxes/:id/files/list?path= List directory
POST   /sandboxes/:id/files/mkdir?path= Create directory
DELETE /sandboxes/:id/files?path=     Delete file
```

### Snapshots

```
POST   /sandboxes/:id/snapshot        Create snapshot
POST   /snapshots/:id/restore         Restore from snapshot
DELETE /snapshots/:id                  Delete snapshot
GET    /snapshots                     List snapshots
```

## Building from source

```bash
git clone https://github.com/open-gitagent/gitvm.git
cd gitvm

# Build all binaries
make build

# Or individually
make build-cli          # bin/gitvm
make build-node         # bin/gitvm-node (linux/amd64)
make build-controlplane # bin/gitvmd

# Run tests
make test
```

## Compared to E2B

| | E2B | gitvm |
|---|---|---|
| Hosting | Managed cloud | Self-hosted |
| Protocol | gRPC + Protobuf | HTTP/JSON |
| Auth | Supabase JWT | API key |
| State | PostgreSQL + Redis | SQLite + in-memory |
| Templates | GCS | Local filesystem |
| Orchestration | Nomad + Consul | Single binary |
| Runtime | Firecracker only | Firecracker + Docker |
| Cost | Per-sandbox pricing | Your cloud costs |

## License

MIT
