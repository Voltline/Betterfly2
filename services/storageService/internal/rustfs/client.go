package rustfs

import (
	"Betterfly2/shared/logger"
	"context"
	"crypto/sha512"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// RustFSClient RustFS客户端封装
type RustFSClient struct {
	client           *s3.Client
	bucket           string
	presign          *s3.PresignClient
	internalEndpoint string
	externalEndpoint string // 外部可访问的endpoint（用于预签名URL）
	region           string
	accessKeyID      string
	secretAccessKey  string
}

func buildPresignClient(region, accessKeyID, secretAccessKey, endpoint string) *s3.PresignClient {
	cfg := aws.Config{
		Region: region,
		EndpointResolverWithOptions: aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
			return aws.Endpoint{
				URL:           endpoint,
				SigningRegion: region,
			}, nil
		}),
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")),
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = true
	})
	return s3.NewPresignClient(client, func(options *s3.PresignOptions) {
		options.ClientOptions = []func(oo *s3.Options){
			func(oo *s3.Options) {
				oo.UsePathStyle = true
			},
		}
	})
}

// NewRustFSClient 创建新的RustFS客户端
func NewRustFSClient() (*RustFSClient, error) {
	sugar := logger.Sugar()

	// 从环境变量获取配置
	region := os.Getenv("RUSTFS_REGION")
	accessKeyID := os.Getenv("RUSTFS_ACCESS_KEY_ID")
	secretAccessKey := os.Getenv("RUSTFS_SECRET_ACCESS_KEY")
	endpoint := os.Getenv("RUSTFS_ENDPOINT_URL")
	// 外部可访问的RustFS地址（用于生成预签名URL）
	externalEndpoint := os.Getenv("RUSTFS_EXTERNAL_ENDPOINT_URL")
	bucket := os.Getenv("RUSTFS_BUCKET")

	if accessKeyID == "" || secretAccessKey == "" || region == "" || endpoint == "" {
		return nil, fmt.Errorf("missing required env vars: RUSTFS_ACCESS_KEY_ID / RUSTFS_SECRET_ACCESS_KEY / RUSTFS_REGION / RUSTFS_ENDPOINT_URL")
	}

	if bucket == "" {
		bucket = "betterfly-files" // 默认bucket名称
		sugar.Warnf("RUSTFS_BUCKET not set, using default: %s", bucket)
	}

	// 构建aws.Config
	cfg := aws.Config{
		Region: region,
		EndpointResolverWithOptions: aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
			return aws.Endpoint{
				URL:           endpoint,
				SigningRegion: region,
			}, nil
		}),
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")),
	}

	// 构建S3客户端
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = true
	})

	presignEndpoint := externalEndpoint
	if presignEndpoint == "" {
		presignEndpoint = endpoint
		sugar.Warnf("RUSTFS_EXTERNAL_ENDPOINT_URL not set, presigned URLs will fall back to the internal endpoint unless overridden per request")
	}
	presignClient := buildPresignClient(region, accessKeyID, secretAccessKey, presignEndpoint)

	// 确保bucket存在
	ctx := context.Background()
	_, err := client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		// bucket不存在，尝试创建
		sugar.Infof("Bucket %s does not exist, creating...", bucket)
		_, err = client.CreateBucket(ctx, &s3.CreateBucketInput{
			Bucket: aws.String(bucket),
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create bucket: %v", err)
		}
		sugar.Infof("Bucket %s created successfully", bucket)
	}

	return &RustFSClient{
		client:           client,
		bucket:           bucket,
		presign:          presignClient,
		internalEndpoint: endpoint,
		externalEndpoint: externalEndpoint,
		region:           region,
		accessKeyID:      accessKeyID,
		secretAccessKey:  secretAccessKey,
	}, nil
}

// GetStoragePath 根据文件哈希生成存储路径
// 使用哈希值的前2位作为目录，避免单目录文件过多
func GetStoragePath(fileHash string) string {
	if len(fileHash) < 2 {
		return fileHash
	}
	return fmt.Sprintf("%s/%s", fileHash[:2], fileHash)
}

// GetStagingPath 返回某个用户上传某个文件时使用的暂存路径。
// 客户端只能拿到暂存路径的上传URL，正式路径只由服务端在哈希校验通过后复制写入。
func GetStagingPath(userID int64, fileHash string) string {
	return fmt.Sprintf("staging/%d/%s", userID, fileHash)
}

// OpenObject 读取对象内容，并返回读取时的ETag，供后续条件复制使用。
func (c *RustFSClient) OpenObject(ctx context.Context, key string) (io.ReadCloser, string, error) {
	resp, err := c.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, "", fmt.Errorf("failed to download file: %v", err)
	}

	return resp.Body, aws.ToString(resp.ETag), nil
}

// CopyObject 在桶内复制对象；sourceETag 非空时要求源对象未被改写。
func (c *RustFSClient) CopyObject(ctx context.Context, sourceKey, destinationKey, sourceETag string) (string, error) {
	input := &s3.CopyObjectInput{
		Bucket:     aws.String(c.bucket),
		CopySource: aws.String(c.bucket + "/" + sourceKey),
		Key:        aws.String(destinationKey),
	}
	if sourceETag != "" {
		input.CopySourceIfMatch = aws.String(sourceETag)
	}
	resp, err := c.client.CopyObject(ctx, input)
	if err != nil {
		return "", fmt.Errorf("failed to copy object: %v", err)
	}
	if resp.CopyObjectResult == nil {
		return "", nil
	}
	return aws.ToString(resp.CopyObjectResult.ETag), nil
}

// FileExists 检查文件是否存在
func (c *RustFSClient) FileExists(ctx context.Context, fileHash string) (bool, error) {
	return c.ObjectExists(ctx, GetStoragePath(fileHash))
}

// ObjectExists 检查指定路径的对象是否存在
func (c *RustFSClient) ObjectExists(ctx context.Context, key string) (bool, error) {
	_, err := c.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isObjectNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// isObjectNotFound 识别 SDK 包装后的 404；HeadObject 没有响应体，只能依赖类型或状态码。
func isObjectNotFound(err error) bool {
	var notFound *types.NotFound
	var noSuchKey *types.NoSuchKey
	if errors.As(err, &notFound) || errors.As(err, &noSuchKey) {
		return true
	}
	var responseErr *smithyhttp.ResponseError
	return errors.As(err, &responseErr) && responseErr.HTTPStatusCode() == http.StatusNotFound
}

// GetPresignedUploadURLForKey 为指定对象路径生成预签名上传URL
func (c *RustFSClient) GetPresignedUploadURLForKey(ctx context.Context, storagePath string, expiresIn time.Duration, externalEndpoint string) (string, error) {
	presignClient, endpoint := c.presignClientForEndpoint(externalEndpoint)

	putObjectInput := s3.PutObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(storagePath),
	}

	presignResult, err := presignClient.PresignPutObject(ctx, &putObjectInput, func(po *s3.PresignOptions) {
		po.ClientOptions = []func(oo *s3.Options){
			func(oo *s3.Options) {
				oo.UsePathStyle = true
			},
		}
		po.Expires = expiresIn
	})
	if err != nil {
		return "", fmt.Errorf("failed to generate presigned upload URL: %v", err)
	}

	logger.Sugar().Debugf("生成预签名上传URL (endpoint=%s): %s", endpoint, presignResult.URL)
	return presignResult.URL, nil
}

// GetPresignedDownloadURL 获取预签名下载URL
func (c *RustFSClient) GetPresignedDownloadURL(ctx context.Context, fileHash string, expiresIn time.Duration) (string, error) {
	return c.GetPresignedDownloadURLForEndpoint(ctx, fileHash, expiresIn, "")
}

func (c *RustFSClient) GetPresignedDownloadURLForEndpoint(ctx context.Context, fileHash string, expiresIn time.Duration, externalEndpoint string) (string, error) {
	storagePath := GetStoragePath(fileHash)
	presignClient, endpoint := c.presignClientForEndpoint(externalEndpoint)

	getObjectInput := s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(storagePath),
	}

	presignResult, err := presignClient.PresignGetObject(ctx, &getObjectInput, func(po *s3.PresignOptions) {
		po.ClientOptions = []func(oo *s3.Options){
			func(oo *s3.Options) {
				oo.UsePathStyle = true
			},
		}
		po.Expires = expiresIn
	})
	if err != nil {
		return "", fmt.Errorf("failed to generate presigned download URL: %v", err)
	}

	logger.Sugar().Debugf("生成预签名下载URL (endpoint=%s): %s", endpoint, presignResult.URL)
	return presignResult.URL, nil
}

func (c *RustFSClient) presignClientForEndpoint(externalEndpoint string) (*s3.PresignClient, string) {
	resolvedEndpoint := externalEndpoint
	if resolvedEndpoint == "" {
		if c.externalEndpoint != "" {
			resolvedEndpoint = c.externalEndpoint
		} else {
			resolvedEndpoint = c.internalEndpoint
		}
	}
	if resolvedEndpoint == c.externalEndpoint || (c.externalEndpoint == "" && resolvedEndpoint == c.internalEndpoint) {
		return c.presign, resolvedEndpoint
	}
	return buildPresignClient(c.region, c.accessKeyID, c.secretAccessKey, resolvedEndpoint), resolvedEndpoint
}

// HealthCheck 检查对象存储是否可用
func (c *RustFSClient) HealthCheck(ctx context.Context) error {
	_, err := c.client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(c.bucket),
	})
	if err != nil {
		return fmt.Errorf("failed to check bucket health: %v", err)
	}
	return nil
}

// VerifyFileHash 验证文件哈希值
func VerifyFileHash(reader io.Reader, expectedHash string) (bool, error) {
	hasher := sha512.New()
	_, err := io.Copy(hasher, reader)
	if err != nil {
		return false, fmt.Errorf("failed to read file for hash verification: %v", err)
	}

	actualHash := fmt.Sprintf("%x", hasher.Sum(nil))
	return actualHash == expectedHash, nil
}

// DeleteObject 删除指定路径的对象
func (c *RustFSClient) DeleteObject(ctx context.Context, storagePath string) error {
	_, err := c.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(storagePath),
	})
	if err != nil {
		return fmt.Errorf("failed to delete file: %v", err)
	}

	logger.Sugar().Debugf("Object deleted successfully: path=%s", storagePath)
	return nil
}
