// Package storage defines the unified object-storage boundary.
//
// Everything binary (avatars, runtime attachments, mirrored artifacts)
// flows through Storage so production can move off LocalFS without code
// changes. LocalFS is the development driver; S3Compatible covers MinIO /
// OSS / TOS style object stores.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Object is a stored blob reference.
type Object struct {
	Key          string
	Size         int64
	ContentType  string
	LastModified time.Time
}

// Storage is the provider-neutral blob interface.
type Storage interface {
	Put(ctx context.Context, key string, r io.Reader, contentType string) (Object, error)
	Open(ctx context.Context, key string) (io.ReadSeekCloser, Object, error)
	Delete(ctx context.Context, key string) error
	Stat(ctx context.Context, key string) (Object, error)
	// PresignedURL is only meaningful for S3-compatible drivers; LocalFS
	// returns an empty string (callers serve bytes through the API).
	PresignedURL(ctx context.Context, key string, ttl time.Duration) (string, error)
}

var ErrNotFound = errors.New("storage: object not found")

// SanitizeKey rejects traversal and normalizes separators. Keys that
// contain ".." in raw form are rejected outright (a key must be a plain
// path under the bucket/root).
func SanitizeKey(key string) (string, error) {
	key = strings.ReplaceAll(key, "\\", "/")
	if strings.Contains(key, "..") {
		return "", fmt.Errorf("storage: invalid key %q", key)
	}
	clean := filepath.ToSlash(filepath.Clean("/" + key))
	clean = strings.TrimPrefix(clean, "/")
	if clean == "" {
		return "", fmt.Errorf("storage: invalid key %q", key)
	}
	return clean, nil
}

// ─────────────────────────────────────────────────────── LocalFS ──

type LocalFS struct {
	Root string
}

func NewLocalFS(root string) (*LocalFS, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("storage: mkdir root: %w", err)
	}
	return &LocalFS{Root: root}, nil
}

func (l *LocalFS) path(key string) (string, error) {
	safe, err := SanitizeKey(key)
	if err != nil {
		return "", err
	}
	return filepath.Join(l.Root, filepath.FromSlash(safe)), nil
}

func (l *LocalFS) Put(ctx context.Context, key string, r io.Reader, contentType string) (Object, error) {
	p, err := l.path(key)
	if err != nil {
		return Object{}, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return Object{}, err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".upload-*")
	if err != nil {
		return Object{}, err
	}
	tmpName := f.Name()
	size, err := io.Copy(f, r)
	if err != nil {
		f.Close()
		os.Remove(tmpName)
		return Object{}, err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpName)
		return Object{}, err
	}
	if err := os.Rename(tmpName, p); err != nil {
		os.Remove(tmpName)
		return Object{}, err
	}
	return Object{Key: key, Size: size, ContentType: contentType, LastModified: time.Now()}, nil
}

func (l *LocalFS) Open(ctx context.Context, key string) (io.ReadSeekCloser, Object, error) {
	p, err := l.path(key)
	if err != nil {
		return nil, Object{}, err
	}
	f, err := os.Open(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, Object{}, ErrNotFound
		}
		return nil, Object{}, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, Object{}, err
	}
	return f, Object{Key: key, Size: st.Size(), LastModified: st.ModTime()}, nil
}

func (l *LocalFS) Delete(ctx context.Context, key string) error {
	p, err := l.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (l *LocalFS) Stat(ctx context.Context, key string) (Object, error) {
	p, err := l.path(key)
	if err != nil {
		return Object{}, err
	}
	st, err := os.Stat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return Object{}, ErrNotFound
		}
		return Object{}, err
	}
	return Object{Key: key, Size: st.Size(), LastModified: st.ModTime()}, nil
}

func (l *LocalFS) PresignedURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return "", nil
}

// ─────────────────────────────────────────────────── S3-compatible ──

// S3Config carries the connection settings for an S3-compatible store.
type S3Config struct {
	Endpoint  string
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
	UseSSL    bool
}

// New builds the configured driver.
func New(cfg StorageDriverConfig) (Storage, error) {
	switch cfg.Driver {
	case "s3":
		return NewS3Compatible(cfg.S3)
	case "", "localfs":
		return NewLocalFS(cfg.LocalRoot)
	default:
		return nil, fmt.Errorf("storage: unknown driver %q", cfg.Driver)
	}
}

type StorageDriverConfig struct {
	Driver    string
	LocalRoot string
	S3        S3Config
}
