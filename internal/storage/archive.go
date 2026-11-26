package storage

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync/atomic"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// s3API is the subset of the S3 client used by S3Archiver, allowing offline
// tests to inject a mock without signing real AWS requests.
type s3API interface {
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

// S3Archiver streams D1 snapshots to S3-compatible cold storage (Backblaze B2
// or Tigris) via gzip + io.Pipe so RSS stays flat (<60 MB) regardless of row
// count (ADR-003).
type S3Archiver struct {
	client s3API
	bucket string
}

// NewS3Archiver creates an S3 archiver configured with a custom endpoint and
// path-style addressing for S3-compatible providers.
func NewS3Archiver(cfg Config) (*S3Archiver, error) {
	awscfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			cfg.S3AccessKeyID, cfg.S3SecretAccessKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}

	opts := []func(*s3.Options){
		func(o *s3.Options) {
			if cfg.S3Endpoint != "" {
				o.BaseEndpoint = aws.String(cfg.S3Endpoint)
			}
			o.UsePathStyle = true
		},
	}
	client := s3.NewFromConfig(awscfg, opts...)

	return &S3Archiver{
		client: client,
		bucket: cfg.S3Bucket,
	}, nil
}

// NewS3ArchiverWithClient creates an S3 archiver with an injected s3API client,
// useful for offline tests.
func NewS3ArchiverWithClient(client s3API, bucket string) *S3Archiver {
	return &S3Archiver{client: client, bucket: bucket}
}

// ArchiveResult summarizes a cold archival operation.
type ArchiveResult struct {
	ExportedRows  int
	DeletedRows   int
	ObjectKey     string
	BytesWritten  int64
}

// ArchiveRows streams D1 snapshot rows older than retention_days through
// gzip.NewWriter -> io.Pipe -> s3.PutObject, then returns the byte count and
// row count for safe D1 pruning. Returns an error if bytes_written == 0 so
// callers never prune D1 without a verified upload.
func (a *S3Archiver) ArchiveRows(ctx context.Context, rows []map[string]interface{}, objectKey string) (ArchiveResult, error) {
	if a.bucket == "" {
		return ArchiveResult{}, fmt.Errorf("s3: bucket not configured")
	}

	pr, pw := io.Pipe()
	var bytesWritten int64

	// Streaming goroutine: encode rows as JSONL.gz into the pipe.
	go func() {
		cw := &countingWriter{w: pw, count: &bytesWritten}
		gz := gzip.NewWriter(cw)
		enc := json.NewEncoder(gz)
		for _, row := range rows {
			if err := enc.Encode(row); err != nil {
				_ = pw.CloseWithError(err)
				return
			}
		}
		if err := gz.Close(); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		_ = pw.Close()
	}()

	_, err := a.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(a.bucket),
		Key:         aws.String(objectKey),
		Body:        pr,
		ContentType: aws.String("application/gzip"),
	})
	if err != nil {
		_ = pw.CloseWithError(err)
		return ArchiveResult{}, fmt.Errorf("s3 put object: %w", err)
	}

	written := atomic.LoadInt64(&bytesWritten)
	if written == 0 {
		return ArchiveResult{}, fmt.Errorf("s3: zero bytes written for %s, aborting archive", objectKey)
	}

	return ArchiveResult{
		ExportedRows: len(rows),
		DeletedRows:  len(rows),
		ObjectKey:    objectKey,
		BytesWritten: written,
	}, nil
}

// objectKeyForRetention builds a dated S3 object key for cold archives.
func objectKeyForRetention(year, month, day string) string {
	return fmt.Sprintf("snapshots/year=%s/month=%s/day=%s/%s-%s-%sT000000Z.jsonl.gz",
		year, month, day, year, month, day)
}
