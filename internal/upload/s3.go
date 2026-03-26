package upload

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3Config for S3/MinIO backend.
type S3Config struct {
	Bucket    string `yaml:"bucket"`
	Region    string `yaml:"region"`
	Prefix    string `yaml:"prefix"`     // key prefix inside bucket
	Endpoint  string `yaml:"endpoint"`   // for MinIO/S3-compatible
	AccessKey string `yaml:"access_key"` // optional override
	SecretKey string `yaml:"secret_key"` // optional override
}

type s3Backend struct {
	client *s3.Client
	bucket string
	prefix string
}

func NewS3Backend(cfg *S3Config) (Backend, error) {
	var opts []func(*config.LoadOptions) error

	if cfg.Region != "" {
		opts = append(opts, config.WithRegion(cfg.Region))
	}

	if cfg.AccessKey != "" && cfg.SecretKey != "" {
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		))
	}

	awsCfg, err := config.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, fmt.Errorf("s3: load config: %w", err)
	}

	var s3Opts []func(*s3.Options)
	if cfg.Endpoint != "" {
		s3Opts = append(s3Opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			o.UsePathStyle = true // required for MinIO
		})
	}

	client := s3.NewFromConfig(awsCfg, s3Opts...)

	return &s3Backend{
		client: client,
		bucket: cfg.Bucket,
		prefix: cfg.Prefix,
	}, nil
}

func (b *s3Backend) Download(ctx context.Context, remotePath, localPath string) error {
	key := path.Join(b.prefix, remotePath)

	result, err := b.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("s3: get %s: %w", key, err)
	}
	defer result.Body.Close()

	// Ensure parent directory exists
	dir := path.Dir(localPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("s3: mkdir %s: %w", dir, err)
	}

	f, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("s3: create %s: %w", localPath, err)
	}
	defer f.Close()

	if _, err := io.Copy(f, result.Body); err != nil {
		os.Remove(localPath)
		return fmt.Errorf("s3: download %s: %w", key, err)
	}
	return nil
}

func (b *s3Backend) List(ctx context.Context, prefix string) ([]RemoteSegment, error) {
	fullPrefix := path.Join(b.prefix, prefix)
	if !strings.HasSuffix(fullPrefix, "/") {
		fullPrefix += "/"
	}

	var segments []RemoteSegment
	paginator := s3.NewListObjectsV2Paginator(b.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(b.bucket),
		Prefix: aws.String(fullPrefix),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("s3: list %s: %w", fullPrefix, err)
		}
		for _, obj := range page.Contents {
			relPath := strings.TrimPrefix(aws.ToString(obj.Key), b.prefix)
			relPath = strings.TrimPrefix(relPath, "/")
			segments = append(segments, RemoteSegment{
				Path:    relPath,
				Size:    aws.ToInt64(obj.Size),
				ModTime: aws.ToTime(obj.LastModified),
			})
		}
	}
	return segments, nil
}

func (b *s3Backend) Delete(ctx context.Context, remotePath string) error {
	key := path.Join(b.prefix, remotePath)
	_, err := b.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("s3: delete %s: %w", key, err)
	}
	return nil
}

func (b *s3Backend) Upload(ctx context.Context, localPath, remotePath string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("s3: open %s: %w", localPath, err)
	}
	defer f.Close()

	key := path.Join(b.prefix, remotePath)

	_, err = b.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
		Body:   f,
	})
	if err != nil {
		return fmt.Errorf("s3: put %s: %w", key, err)
	}
	return nil
}

func (b *s3Backend) Close() error { return nil }
func (b *s3Backend) Name() string { return "s3" }
