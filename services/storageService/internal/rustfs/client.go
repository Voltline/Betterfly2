package rustfs

import (
	"Betterfly2/shared/logger"
	"context"
	"crypto/sha512"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// RustFSClient RustFS客户端封装
type RustFSClient struct {
	client           *s3.Client
	bucket           string
	presign          *s3.PresignClient
	externalEndpoint string // 外部可访问的endpoint（用于预签名URL）
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

	// 如果没有配置外部endpoint，使用内部endpoint
	if externalEndpoint == "" {
		externalEndpoint = endpoint
		sugar.Warnf("RUSTFS_EXTERNAL_ENDPOINT_URL not set, using internal endpoint: %s", endpoint)
	}

	// 构建用于内部操作的S3客户端（使用内部endpoint）
	// 构建用于预签名的S3客户端（使用外部endpoint，这样生成的URL客户端可以直接访问）
	externalCfg := aws.Config{
		Region: region,
		EndpointResolverWithOptions: aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
			return aws.Endpoint{
				URL:           externalEndpoint,
				SigningRegion: region,
			}, nil
		}),
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")),
	}
	externalClient := s3.NewFromConfig(externalCfg, func(o *s3.Options) {
		o.UsePathStyle = true
	})

	// 构建预签名客户端（使用外部endpoint，这样生成的URL可以直接被客户端访问）
	presignClient := s3.NewPresignClient(externalClient, func(options *s3.PresignOptions) {
		options.ClientOptions = []func(oo *s3.Options){
			func(oo *s3.Options) {
				oo.UsePathStyle = true
			},
		}
	})

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
		externalEndpoint: externalEndpoint,
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

// UploadFile 上传文件到RustFS
func (c *RustFSClient) UploadFile(ctx context.Context, fileHash string, fileSize int64, reader io.Reader) error {
	storagePath := GetStoragePath(fileHash)

	_, err := c.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(storagePath),
		Body:   reader,
	})
	if err != nil {
		return fmt.Errorf("failed to upload file: %v", err)
	}

	logger.Sugar().Debugf("File uploaded successfully: hash=%s, path=%s", fileHash, storagePath)
	return nil
}

// DownloadFile 从RustFS下载文件
func (c *RustFSClient) DownloadFile(ctx context.Context, fileHash string) (io.ReadCloser, error) {
	storagePath := GetStoragePath(fileHash)

	resp, err := c.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(storagePath),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to download file: %v", err)
	}

	return resp.Body, nil
}

// FileExists 检查文件是否存在
func (c *RustFSClient) FileExists(ctx context.Context, fileHash string) (bool, error) {
	storagePath := GetStoragePath(fileHash)

	_, err := c.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(storagePath),
	})
	if err != nil {
		var nsk *types.NotFound
		if err, ok := err.(interface{ As(interface{}) bool }); ok && err.As(&nsk) {
			return false, nil
		}
		// 检查是否是NoSuchKey错误
		if err.Error() == "NotFound" {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// GetPresignedUploadURL 获取预签名上传URL
func (c *RustFSClient) GetPresignedUploadURL(ctx context.Context, fileHash string, expiresIn time.Duration) (string, error) {
	storagePath := GetStoragePath(fileHash)

	putObjectInput := s3.PutObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(storagePath),
	}

	presignResult, err := c.presign.PresignPutObject(ctx, &putObjectInput, func(po *s3.PresignOptions) {
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

	logger.Sugar().Debugf("生成预签名上传URL (使用外部endpoint): %s", presignResult.URL)
	return presignResult.URL, nil
}

// GetPresignedDownloadURL 获取预签名下载URL
func (c *RustFSClient) GetPresignedDownloadURL(ctx context.Context, fileHash string, expiresIn time.Duration) (string, error) {
	storagePath := GetStoragePath(fileHash)

	getObjectInput := s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(storagePath),
	}

	presignResult, err := c.presign.PresignGetObject(ctx, &getObjectInput, func(po *s3.PresignOptions) {
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

	logger.Sugar().Debugf("生成预签名下载URL: %s", presignResult.URL)
	return presignResult.URL, nil
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

// DeleteFile 删除文件
func (c *RustFSClient) DeleteFile(ctx context.Context, fileHash string) error {
	storagePath := GetStoragePath(fileHash)

	_, err := c.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(storagePath),
	})
	if err != nil {
		return fmt.Errorf("failed to delete file: %v", err)
	}

	logger.Sugar().Debugf("File deleted successfully: hash=%s, path=%s", fileHash, storagePath)
	return nil
}
