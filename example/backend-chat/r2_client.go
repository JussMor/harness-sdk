package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// R2Client wraps Cloudflare R2 via the S3-compatible API.
// Used for artifact binary storage: images, large files, sandbox outputs.
//
// Configure via environment variables:
//
//	R2_ACCOUNT_ID      — Cloudflare account ID
//	R2_ACCESS_KEY_ID   — R2 access key ID
//	R2_SECRET_KEY      — R2 secret access key
//	R2_BUCKET          — bucket name (default: "artifacts")
//	R2_PUBLIC_URL      — public base URL for generated presigned-style URLs
//	                     e.g. https://pub-xxx.r2.dev or your custom domain
type R2Client struct {
	client    *s3.Client
	bucket    string
	publicURL string
}

// NewR2Client creates a client from environment variables.
// Returns nil if R2_ACCOUNT_ID is not set — callers must check for nil.
func NewR2Client() *R2Client {
	accountID := strings.TrimSpace(os.Getenv("R2_ACCOUNT_ID"))
	if accountID == "" {
		return nil
	}

	accessKey := os.Getenv("R2_ACCESS_KEY_ID")
	secretKey := os.Getenv("R2_SECRET_KEY")
	bucket := getenv("R2_BUCKET", "artifacts")
	publicURL := strings.TrimRight(os.Getenv("R2_PUBLIC_URL"), "/")

	endpoint := fmt.Sprintf("https://%s.r2.cloudflarestorage.com", accountID)

	client := s3.New(s3.Options{
		BaseEndpoint: aws.String(endpoint),
		Region:       "auto",
		Credentials: aws.NewCredentialsCache(
			credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		),
	})

	return &R2Client{
		client:    client,
		bucket:    bucket,
		publicURL: publicURL,
	}
}

// IsAvailable returns true when the client is configured.
func (r *R2Client) IsAvailable() bool {
	return r != nil && r.client != nil
}

// Put uploads content to R2 under key. contentType is e.g. "image/png", "text/html".
// Returns the public URL of the uploaded object.
func (r *R2Client) Put(ctx context.Context, key, contentType string, data []byte) (string, error) {
	_, err := r.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(r.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return "", fmt.Errorf("r2 put %s: %w", key, err)
	}
	return r.urlFor(key), nil
}

// Get downloads an object from R2 by key.
func (r *R2Client) Get(ctx context.Context, key string) ([]byte, string, error) {
	out, err := r.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(r.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, "", fmt.Errorf("r2 get %s: %w", key, err)
	}
	defer out.Body.Close()

	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, "", fmt.Errorf("r2 read %s: %w", key, err)
	}

	contentType := ""
	if out.ContentType != nil {
		contentType = *out.ContentType
	}
	return data, contentType, nil
}

// Delete removes an object from R2.
func (r *R2Client) Delete(ctx context.Context, key string) error {
	_, err := r.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(r.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("r2 delete %s: %w", key, err)
	}
	return nil
}

// URLFor returns the public URL for a key.
func (r *R2Client) urlFor(key string) string {
	if r.publicURL != "" {
		return r.publicURL + "/" + key
	}
	// Fallback: serve through our own backend
	return "/api/artifacts/r2/" + key
}

// ArtifactKey builds the R2 key for an artifact version binary.
// Pattern: artifacts/{artifactID}/{version}/{filename}
func ArtifactKey(artifactID string, version int, filename string) string {
	return fmt.Sprintf("artifacts/%s/%d/%s", artifactID, version, filename)
}

// contentTypeFor returns a MIME type for a given artifact language.
func contentTypeFor(language string) string {
	switch strings.ToLower(language) {
	case "html", "htm":
		return "text/html"
	case "svg":
		return "image/svg+xml"
	case "md", "markdown":
		return "text/markdown"
	case "json":
		return "application/json"
	case "css":
		return "text/css"
	case "jsx", "tsx", "javascript", "js", "typescript", "ts":
		return "application/javascript"
	case "python", "py":
		return "text/x-python"
	case "go":
		return "text/x-go"
	default:
		return "text/plain"
	}
}
