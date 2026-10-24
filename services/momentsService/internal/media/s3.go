package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

type S3 struct {
	client *s3.Client
	// signer 只在配置了RUSTFS_EXTERNAL_ENDPOINT_URL时固定使用；否则按请求推导的地址签名，见WithPublicEndpoint。
	signer *s3.PresignClient
	bucket string
}

type publicEndpointKey struct{}

// WithPublicEndpoint 记下本次请求里客户端访问对象存储应使用的地址。
// 没有配置RUSTFS_EXTERNAL_ENDPOINT_URL时，预签名URL按它生成，真机拿到的是服务器地址而不是localhost；
// 配置了则忽略。ctx里没有时回退到http://localhost:9000。
func WithPublicEndpoint(ctx context.Context, endpoint string) context.Context {
	return context.WithValue(ctx, publicEndpointKey{}, endpoint)
}
func (s *S3) presigner(ctx context.Context) (*s3.PresignClient, error) {
	if s.signer != nil {
		return s.signer, nil
	}
	endpoint, _ := ctx.Value(publicEndpointKey{}).(string)
	if endpoint == "" {
		endpoint = "http://localhost:9000"
	}
	// 推导的主机名来自请求，按次构造，不缓存，避免被任意Host撑大
	c, err := client(endpoint)
	if err != nil {
		return nil, err
	}
	return s3.NewPresignClient(c), nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func client(endpoint string) (*s3.Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.Scheme != "http" && u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("invalid RustFS endpoint")
	}
	access, secret := os.Getenv("RUSTFS_ACCESS_KEY_ID"), os.Getenv("RUSTFS_SECRET_ACCESS_KEY")
	if access == "" || secret == "" {
		return nil, fmt.Errorf("RustFS credentials are required")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxConnsPerHost = 8
	config := aws.Config{Region: env("RUSTFS_REGION", "cn-east-1"), Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(access, secret, "")), HTTPClient: &http.Client{Timeout: 25 * time.Second, Transport: transport}}
	return s3.NewFromConfig(config, func(o *s3.Options) { o.UsePathStyle = true; o.BaseEndpoint = aws.String(endpoint) }), nil
}
func New(ctx context.Context, origin string) (*S3, error) {
	c, err := client(env("RUSTFS_ENDPOINT_URL", "http://localhost:9000"))
	if err != nil {
		return nil, err
	}
	store := &S3{client: c, bucket: env("MOMENTS_BUCKET", "betterfly-moments")}
	publicEndpoint := os.Getenv("RUSTFS_EXTERNAL_ENDPOINT_URL")
	if strings.HasPrefix(origin, "https://") && !strings.HasPrefix(publicEndpoint, "https://") {
		return nil, fmt.Errorf("HTTPS Moments requires an HTTPS RUSTFS_EXTERNAL_ENDPOINT_URL")
	}
	if publicEndpoint != "" {
		public, err := client(publicEndpoint)
		if err != nil {
			return nil, err
		}
		store.signer = s3.NewPresignClient(public)
	}
	if store.bucket == env("RUSTFS_BUCKET", "betterfly-files") {
		return nil, fmt.Errorf("Moments requires a separate private bucket")
	}
	if _, err = c.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: &store.bucket}); err != nil {
		var api smithy.APIError
		if !errors.As(err, &api) || api.ErrorCode() != "NotFound" && api.ErrorCode() != "NoSuchBucket" {
			return nil, fmt.Errorf("head private bucket: %w", err)
		}
		if _, err = c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: &store.bucket}); err != nil {
			return nil, err
		}
	}
	// A private namespace must not inherit an existing public policy or ACL.
	policy, err := c.GetBucketPolicy(ctx, &s3.GetBucketPolicyInput{Bucket: &store.bucket})
	if err != nil {
		var api smithy.APIError
		if !errors.As(err, &api) || api.ErrorCode() != "NoSuchBucketPolicy" {
			return nil, fmt.Errorf("check private bucket policy: %w", err)
		}
	} else if aws.ToString(policy.Policy) != "" {
		return nil, fmt.Errorf("Moments bucket must not have a bucket policy")
	}
	acl, err := c.GetBucketAcl(ctx, &s3.GetBucketAclInput{Bucket: &store.bucket})
	if err != nil {
		return nil, err
	}
	for _, grant := range acl.Grants {
		if grant.Grantee != nil && grant.Grantee.Type == types.TypeGroup {
			return nil, fmt.Errorf("Moments bucket must not have group ACL grants")
		}
	}
	_, err = c.PutBucketCors(ctx, &s3.PutBucketCorsInput{Bucket: &store.bucket, CORSConfiguration: &types.CORSConfiguration{CORSRules: []types.CORSRule{{AllowedOrigins: []string{origin}, AllowedMethods: []string{"PUT", "GET", "HEAD"}, AllowedHeaders: []string{"content-type", "content-length", "x-amz-*"}, ExposeHeaders: []string{"ETag"}, MaxAgeSeconds: aws.Int32(600)}}}})
	if err != nil {
		return nil, fmt.Errorf("configure private bucket CORS: %w", err)
	}
	return store, nil
}
func (s *S3) Upload(ctx context.Context, key, mime string, size int64) (string, map[string]string, error) {
	signer, err := s.presigner(ctx)
	if err != nil {
		return "", nil, err
	}
	result, err := signer.PresignPutObject(ctx, &s3.PutObjectInput{Bucket: &s.bucket, Key: &key, ContentType: &mime, ContentLength: &size}, func(o *s3.PresignOptions) { o.Expires = 10 * time.Minute })
	if err != nil {
		return "", nil, err
	}
	return result.URL, map[string]string{"Content-Type": mime}, nil
}
func (s *S3) Read(ctx context.Context, key string) (io.ReadCloser, error) {
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil {
		return nil, err
	}
	return result.Body, nil
}
func (s *S3) Seal(ctx context.Context, key string, data []byte, mime string) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: &s.bucket, Key: &key, Body: bytes.NewReader(data), ContentType: &mime})
	return err
}
func (s *S3) Download(ctx context.Context, key string) (string, error) {
	signer, err := s.presigner(ctx)
	if err != nil {
		return "", err
	}
	result, err := signer.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &key, ResponseCacheControl: aws.String("private, no-store")}, func(o *s3.PresignOptions) { o.Expires = time.Minute })
	if err != nil {
		return "", err
	}
	return result.URL, nil
}
func (s *S3) Avatar(ctx context.Context, key string) (string, error) {
	bucket := env("RUSTFS_BUCKET", "betterfly-files")
	signer, err := s.presigner(ctx)
	if err != nil {
		return "", err
	}
	result, err := signer.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: &bucket, Key: &key, ResponseCacheControl: aws.String("private, no-store")}, func(o *s3.PresignOptions) { o.Expires = time.Minute })
	if err != nil {
		return "", err
	}
	return result.URL, nil
}

// Delete removes objects from the private bucket; missing keys are not an error.
func (s *S3) Delete(ctx context.Context, keys []string) error {
	for _, key := range keys {
		if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: aws.String(key)}); err != nil {
			return fmt.Errorf("delete %s: %w", key, err)
		}
	}
	return nil
}

// DeleteStagingBefore removes staging uploads last written before cutoff. Upload URLs
// live for minutes, so anything older is an abandoned upload or a copy already sealed.
func (s *S3) DeleteStagingBefore(ctx context.Context, cutoff time.Time) (int, error) {
	deleted := 0
	pages := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{Bucket: &s.bucket, Prefix: aws.String("staging/")})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return deleted, err
		}
		for _, object := range page.Contents {
			if object.Key == nil || object.LastModified == nil || !object.LastModified.Before(cutoff) {
				continue
			}
			if err := s.Delete(ctx, []string{*object.Key}); err != nil {
				return deleted, err
			}
			deleted++
		}
	}
	return deleted, nil
}
func (s *S3) Health(ctx context.Context) error {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: &s.bucket})
	return err
}
