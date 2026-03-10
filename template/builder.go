package template

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
)

// Builder creates rootfs images from Dockerfiles.
type Builder struct {
	cache  *Cache
	logger *slog.Logger
}

// NewBuilder creates a template builder.
func NewBuilder(cache *Cache, logger *slog.Logger) *Builder {
	return &Builder{cache: cache, logger: logger}
}

// Build creates a rootfs image from a Dockerfile directory.
// Steps:
// 1. docker build the image
// 2. docker create a container
// 3. docker export the filesystem
// 4. Convert to ext4 image
func (b *Builder) Build(name string, dockerfileDir string) error {
	b.logger.Info("building template", "name", name, "dir", dockerfileDir)

	imageName := fmt.Sprintf("gitvm-template-%s", name)

	// 1. Docker build
	b.logger.Info("building docker image")
	cmd := exec.Command("docker", "build", "-t", imageName, dockerfileDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker build: %w", err)
	}

	// 2. Create container
	b.logger.Info("creating container")
	out, err := exec.Command("docker", "create", imageName).Output()
	if err != nil {
		return fmt.Errorf("docker create: %w", err)
	}
	containerID := string(out[:len(out)-1]) // trim newline
	defer exec.Command("docker", "rm", containerID).Run()

	// 3. Export filesystem to tar
	templateDir := filepath.Join(b.cache.Dir(), name)
	if err := os.MkdirAll(templateDir, 0o755); err != nil {
		return fmt.Errorf("create template dir: %w", err)
	}

	tarPath := filepath.Join(templateDir, "rootfs.tar")
	b.logger.Info("exporting filesystem")
	exportCmd := exec.Command("docker", "export", "-o", tarPath, containerID)
	if err := exportCmd.Run(); err != nil {
		return fmt.Errorf("docker export: %w", err)
	}
	defer os.Remove(tarPath)

	// 4. Create ext4 image from tar
	rootfsPath := filepath.Join(templateDir, "rootfs.ext4")
	b.logger.Info("creating ext4 image")
	if err := b.createExt4(tarPath, rootfsPath); err != nil {
		return fmt.Errorf("create ext4: %w", err)
	}

	b.logger.Info("template built", "name", name, "path", rootfsPath)
	return nil
}

// createExt4 creates an ext4 filesystem image from a tar archive.
func (b *Builder) createExt4(tarPath, rootfsPath string) error {
	// Create a 2GB sparse file
	sizeMB := 2048
	if err := exec.Command("dd", "if=/dev/zero", "of="+rootfsPath,
		"bs=1M", "count=0", fmt.Sprintf("seek=%d", sizeMB)).Run(); err != nil {
		return fmt.Errorf("create sparse file: %w", err)
	}

	// Format as ext4
	if err := exec.Command("mkfs.ext4", "-F", rootfsPath).Run(); err != nil {
		return fmt.Errorf("mkfs.ext4: %w", err)
	}

	// Mount and extract tar
	mountDir := rootfsPath + ".mnt"
	if err := os.MkdirAll(mountDir, 0o755); err != nil {
		return fmt.Errorf("create mount dir: %w", err)
	}
	defer os.RemoveAll(mountDir)

	if err := exec.Command("mount", "-o", "loop", rootfsPath, mountDir).Run(); err != nil {
		return fmt.Errorf("mount: %w", err)
	}
	defer exec.Command("umount", mountDir).Run()

	if err := exec.Command("tar", "xf", tarPath, "-C", mountDir).Run(); err != nil {
		return fmt.Errorf("extract tar: %w", err)
	}

	return nil
}
