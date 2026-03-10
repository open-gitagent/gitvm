package template

import (
	"fmt"
	"os"
	"path/filepath"
)

const defaultTemplatesDir = ".gitvm/templates"

// Cache manages locally stored template images.
type Cache struct {
	dir string
}

// NewCache creates a template cache at the given directory.
// If dir is empty, uses ~/.gitvm/templates.
func NewCache(dir string) *Cache {
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, defaultTemplatesDir)
	}
	return &Cache{dir: dir}
}

// Dir returns the cache directory.
func (c *Cache) Dir() string {
	return c.dir
}

// TemplatePath returns the rootfs path for a given template name.
func (c *Cache) TemplatePath(name string) string {
	return filepath.Join(c.dir, name, "rootfs.ext4")
}

// KernelPath returns the kernel path for a given template name.
// Falls back to the global kernel path.
func (c *Cache) KernelPath(name string) string {
	templateKernel := filepath.Join(c.dir, name, "vmlinux")
	if _, err := os.Stat(templateKernel); err == nil {
		return templateKernel
	}
	// Global kernel
	return filepath.Join(c.dir, "vmlinux")
}

// Exists checks if a template exists in the cache.
func (c *Cache) Exists(name string) bool {
	_, err := os.Stat(c.TemplatePath(name))
	return err == nil
}

// List returns all cached template names.
func (c *Cache) List() ([]TemplateInfo, error) {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read templates dir: %w", err)
	}

	var templates []TemplateInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		rootfs := filepath.Join(c.dir, entry.Name(), "rootfs.ext4")
		info, err := os.Stat(rootfs)
		if err != nil {
			continue
		}
		templates = append(templates, TemplateInfo{
			Name:    entry.Name(),
			Path:    rootfs,
			SizeMB:  info.Size() / (1024 * 1024),
			ModTime: info.ModTime().Format("2006-01-02 15:04:05"),
		})
	}
	return templates, nil
}

// TemplateInfo describes a cached template.
type TemplateInfo struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	SizeMB  int64  `json:"sizeMB"`
	ModTime string `json:"modTime"`
}
