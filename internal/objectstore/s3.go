package objectstore

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type S3Config struct {
	Endpoint, Region, Bucket, AccessKey, SecretKey, SessionToken string
	Secure, PathStyle, CreateBucket                              bool
}

type S3 struct {
	client *minio.Client
	bucket string
}

func NewS3(ctx context.Context, cfg S3Config) (*S3, error) {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" || cfg.Bucket == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, errors.New("S3 endpoint, bucket and credentials are required")
	}
	if parsed, err := url.Parse(endpoint); err == nil && parsed.Host != "" {
		if parsed.Path != "" && parsed.Path != "/" {
			return nil, errors.New("S3 endpoint must not contain a path")
		}
		endpoint = parsed.Host
		if parsed.Scheme == "https" {
			cfg.Secure = true
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, cfg.SessionToken),
		Secure: cfg.Secure, Region: cfg.Region, BucketLookup: bucketLookup(cfg.PathStyle), Transport: transport,
	})
	if err != nil {
		return nil, err
	}
	store := &S3{client: client, bucket: cfg.Bucket}
	if cfg.CreateBucket {
		exists, err := client.BucketExists(ctx, cfg.Bucket)
		if err != nil {
			return nil, fmt.Errorf("check S3 bucket: %w", err)
		}
		if !exists {
			if err := client.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{Region: cfg.Region}); err != nil {
				return nil, fmt.Errorf("create S3 bucket: %w", err)
			}
		}
	}
	return store, nil
}

func bucketLookup(pathStyle bool) minio.BucketLookupType {
	if pathStyle {
		return minio.BucketLookupPath
	}
	return minio.BucketLookupAuto
}

func (s *S3) Put(ctx context.Context, key string, source io.Reader, size int64, options PutOptions) (ObjectInfo, error) {
	if err := validRemoteKey(key); err != nil {
		return ObjectInfo{}, err
	}
	info, err := s.client.PutObject(ctx, s.bucket, key, source, size, minio.PutObjectOptions{
		ContentType: options.ContentType, UserMetadata: options.Metadata,
	})
	if err != nil {
		_ = s.client.RemoveIncompleteUpload(context.Background(), s.bucket, key)
		return ObjectInfo{}, err
	}
	return ObjectInfo{Key: key, Size: info.Size, ETag: info.ETag}, nil
}

func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	info, err := s.Stat(ctx, key)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	object, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	return object, info, nil
}

func (s *S3) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	if err := validRemoteKey(key); err != nil {
		return ObjectInfo{}, err
	}
	info, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return ObjectInfo{}, err
	}
	return ObjectInfo{Key: key, Size: info.Size, ETag: info.ETag, LastModified: info.LastModified.UTC(), Metadata: info.UserMetadata}, nil
}

func (s *S3) Delete(ctx context.Context, key string) error {
	if err := validRemoteKey(key); err != nil {
		return err
	}
	return s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
}

func (s *S3) List(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	if prefix != "" {
		if err := validRemoteKey(prefix); err != nil {
			return nil, err
		}
	}
	result := []ObjectInfo{}
	for object := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if object.Err != nil {
			return nil, object.Err
		}
		result = append(result, ObjectInfo{Key: object.Key, Size: object.Size, ETag: object.ETag, LastModified: object.LastModified.UTC()})
	}
	return result, nil
}

func validRemoteKey(key string) error {
	if key == "" || strings.HasPrefix(key, "/") || strings.Contains(key, "\\") || strings.Contains(key, "..") {
		return errors.New("invalid object key")
	}
	return nil
}
