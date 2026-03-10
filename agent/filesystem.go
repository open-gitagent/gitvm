package agent

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// ReadFile reads the contents of a file at the given path.
func ReadFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return data, nil
}

// WriteFile writes content to a file at the given path, creating directories as needed.
func WriteFile(path string, content []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// ListDir lists entries in a directory.
func ListDir(path string) ([]FileInfo, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", path, err)
	}

	infos := make([]FileInfo, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		infos = append(infos, FileInfo{
			Name:  entry.Name(),
			IsDir: entry.IsDir(),
			Size:  info.Size(),
			Mode:  info.Mode().String(),
		})
	}
	return infos, nil
}

// MakeDir creates a directory and all parent directories.
func MakeDir(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", path, err)
	}
	return nil
}

// RemovePath removes a file or directory (recursive).
func RemovePath(path string) error {
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

// Exists checks if a path exists and returns its info.
func Exists(path string) (bool, fs.FileInfo, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	return true, info, nil
}
