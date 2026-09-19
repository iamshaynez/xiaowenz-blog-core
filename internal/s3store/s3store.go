// Package s3store is the object-storage seam (Cloudflare R2 via S3 API).
// Tests use Fake; the real client is built with aws-sdk-go-v2.
package s3store

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Client abstracts the S3 operations the service needs.
type Client interface {
	// Put stores body under key and returns its public URL.
	Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) (string, error)
	// ListKeys returns object keys under prefix.
	ListKeys(ctx context.Context, prefix string) ([]string, error)
	// Delete removes key (missing keys are not an error).
	Delete(ctx context.Context, key string) error
}

// R2 is a Client backed by Cloudflare R2's S3-compatible API.
type R2 struct {
	api        *s3.Client
	bucket     string
	publicBase string
}

// NewR2 builds the R2 client from explicit credentials and endpoint.
func NewR2(endpoint, keyID, secret, bucket, publicBase string) *R2 {
	cfg := aws.Config{
		Region:      "auto", // R2 ignores region but the SDK requires one
		Credentials: credentials.NewStaticCredentialsProvider(keyID, secret, ""),
	}
	api := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
	return &R2{api: api, bucket: bucket, publicBase: strings.TrimRight(publicBase, "/")}
}

// Put implements Client.
func (r *R2) Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) (string, error) {
	data, err := io.ReadAll(io.LimitReader(body, size+1))
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	_, err = r.api.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(r.bucket),
		Key:           aws.String(key),
		Body:          bytes.NewReader(data),
		ContentType:   aws.String(contentType),
		ContentLength: aws.Int64(int64(len(data))),
	})
	if err != nil {
		return "", fmt.Errorf("s3 put %s: %w", key, err)
	}
	return r.publicBase + "/" + key, nil
}

// ListKeys implements Client.
func (r *R2) ListKeys(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	paginator := s3.NewListObjectsV2Paginator(r.api, &s3.ListObjectsV2Input{
		Bucket: aws.String(r.bucket),
		Prefix: aws.String(prefix),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("s3 list %s: %w", prefix, err)
		}
		for _, obj := range page.Contents {
			keys = append(keys, aws.ToString(obj.Key))
		}
	}
	return keys, nil
}

// Delete implements Client.
func (r *R2) Delete(ctx context.Context, key string) error {
	_, err := r.api.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(r.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("s3 delete %s: %w", key, err)
	}
	return nil
}

// Fake is an in-memory Client for tests.
type Fake struct {
	mu         sync.Mutex
	objects    map[string][]byte
	publicBase string
}

// NewFake builds a Fake whose URLs resolve under publicBase.
func NewFake(publicBase string) *Fake {
	return &Fake{objects: map[string][]byte{}, publicBase: strings.TrimRight(publicBase, "/")}
}

// Put implements Client.
func (f *Fake) Put(_ context.Context, key string, body io.Reader, _ int64, _ string) (string, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = data
	return f.publicBase + "/" + key, nil
}

// Get reads back a stored object (test helper).
func (f *Fake) Get(_ context.Context, key string) (io.ReadCloser, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.objects[key]
	if !ok {
		return nil, 0, fmt.Errorf("not found: %s", key)
	}
	return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
}

// ListKeys implements Client.
func (f *Fake) ListKeys(_ context.Context, prefix string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var keys []string
	for k := range f.objects {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

// Delete implements Client.
func (f *Fake) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, key)
	return nil
}
