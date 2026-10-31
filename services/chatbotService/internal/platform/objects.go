package platform

import (
	"context"
	"io"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// waiters 唤醒同一实例里正在长轮询的请求；其他实例写入的更新靠轮询间隔兜底。
type waiters struct {
	mu      sync.Mutex
	channel map[int64]chan struct{}
}

func newWaiters() *waiters { return &waiters{channel: map[int64]chan struct{}{}} }

// wait 返回一个在该机器人有新更新时关闭的channel。
func (w *waiters) wait(botID int64) <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	ch, ok := w.channel[botID]
	if !ok {
		ch = make(chan struct{})
		w.channel[botID] = ch
	}
	return ch
}

func (w *waiters) notify(botID int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if ch, ok := w.channel[botID]; ok {
		close(ch)
		delete(w.channel, botID)
	}
}

// Objects 读写对象存储里的文件。
type Objects interface {
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	Put(ctx context.Context, key string, body io.ReadSeeker, size int64) error
}

// S3Objects 用RustFS的S3接口读写对象。
type S3Objects struct {
	client *s3.Client
	bucket string
}

func NewS3Objects(endpoint, region, accessKeyID, secretAccessKey, bucket string) *S3Objects {
	client := s3.NewFromConfig(aws.Config{
		Region:      region,
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")),
	}, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(endpoint)
		options.UsePathStyle = true
	})
	return &S3Objects{client: client, bucket: bucket}
}

func (o *S3Objects) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	response, err := o.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(o.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, err
	}
	return response.Body, nil
}

func (o *S3Objects) Put(ctx context.Context, key string, body io.ReadSeeker, size int64) error {
	_, err := o.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(o.bucket), Key: aws.String(key), Body: body, ContentLength: aws.Int64(size),
	})
	return err
}
