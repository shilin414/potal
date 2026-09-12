package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Compatible stores objects in any S3-compatible endpoint
// (MinIO, AWS S3, Volcengine TOS, Aliyun OSS with s3 gateway...).
type S3Compatible struct {
	client *minio.Client
	bucket string
}

func NewS3Compatible(cfg S3Config) (*S3Compatible, error) {
	if cfg.Bucket == "" || cfg.Endpoint == "" {
		return nil, errors.New("storage: s3 endpoint and bucket are required")
	}
	secure := cfg.UseSSL
	cl, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: secure,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("storage: minio client: %w", err)
	}
	return &S3Compatible{client: cl, bucket: cfg.Bucket}, nil
}

func (s *S3Compatible) Put(ctx context.Context, key string, r io.Reader, contentType string) (Object, error) {
	safe, err := SanitizeKey(key)
	if err != nil {
		return Object{}, err
	}
	info, err := s.client.PutObject(ctx, s.bucket, safe, r, -1, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return Object{}, fmt.Errorf("storage: put %s: %w", safe, err)
	}
	return Object{Key: safe, Size: info.Size, ContentType: contentType, LastModified: info.LastModified}, nil
}

func (s *S3Compatible) Open(ctx context.Context, key string) (io.ReadSeekCloser, Object, error) {
	safe, err := SanitizeKey(key)
	if err != nil {
		return nil, Object{}, err
	}
	obj, err := s.client.GetObject(ctx, s.bucket, safe, minio.GetObjectOptions{})
	if err != nil {
		return nil, Object{}, fmt.Errorf("storage: open %s: %w", safe, err)
	}
	st, err := obj.Stat()
	if err != nil {
		obj.Close()
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, Object{}, ErrNotFound
		}
		return nil, Object{}, err
	}
	return obj, Object{Key: safe, Size: st.Size, ContentType: st.ContentType, LastModified: st.LastModified}, nil
}

func (s *S3Compatible) Delete(ctx context.Context, key string) error {
	safe, err := SanitizeKey(key)
	if err != nil {
		return err
	}
	return s.client.RemoveObject(ctx, s.bucket, safe, minio.RemoveObjectOptions{})
}

func (s *S3Compatible) Stat(ctx context.Context, key string) (Object, error) {
	safe, err := SanitizeKey(key)
	if err != nil {
		return Object{}, err
	}
	st, err := s.client.StatObject(ctx, s.bucket, safe, minio.StatObjectOptions{})
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return Object{}, ErrNotFound
		}
		return Object{}, err
	}
	return Object{Key: safe, Size: st.Size, ContentType: st.ContentType, LastModified: st.LastModified}, nil
}

// PresignedURL returns a time-limited direct-download URL.
func (s *S3Compatible) PresignedURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	safe, err := SanitizeKey(key)
	if err != nil {
		return "", err
	}
	u, err := s.client.PresignedGetObject(ctx, s.bucket, safe, ttl, url.Values{})
	if err != nil {
		return "", err
	}
	return u.String(), nil
}
