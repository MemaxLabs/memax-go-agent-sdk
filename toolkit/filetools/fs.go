package filetools

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
)

type FileSystem interface {
	ReadFile(context.Context, string) (string, error)
	WriteFile(context.Context, string, string) error
	ListFiles(context.Context, string) ([]string, error)
}

type MemoryFS struct {
	mu    sync.RWMutex
	files map[string]string
}

func NewMemoryFS(files map[string]string) *MemoryFS {
	fs := &MemoryFS{files: make(map[string]string, len(files))}
	for name, content := range files {
		fs.files[cleanPath(name)] = content
	}
	return fs
}

func (fs *MemoryFS) ReadFile(_ context.Context, name string) (string, error) {
	name = cleanPath(name)
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	content, ok := fs.files[name]
	if !ok {
		return "", fmt.Errorf("file not found: %s", name)
	}
	return content, nil
}

func (fs *MemoryFS) WriteFile(_ context.Context, name string, content string) error {
	name = cleanPath(name)
	if isInvalidWritePath(name) {
		return fmt.Errorf("invalid file path: %s", name)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.files[name] = content
	return nil
}

func (fs *MemoryFS) ListFiles(_ context.Context, prefix string) ([]string, error) {
	prefix = cleanPrefix(prefix)
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	var names []string
	for name := range fs.files {
		if prefix == "" || name == prefix || strings.HasPrefix(name, prefix+"/") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

func cleanPath(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "/")
	if name == "" {
		return "."
	}
	return path.Clean(name)
}

func isInvalidWritePath(name string) bool {
	return cleanPath(name) == "."
}

func cleanPrefix(prefix string) string {
	prefix = cleanPath(prefix)
	if prefix == "." {
		return ""
	}
	return prefix
}
