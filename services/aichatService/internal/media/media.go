package media

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif" // GIF只取第一帧
	"image/jpeg"
	_ "image/png"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	// maxPixels 在解码前按图片头部的尺寸拦截超大图，防止小文件解压成巨大位图。
	maxPixels = 40_000_000
	// passThroughBytes 以内、尺寸也不超限的JPEG/PNG原样发送，省去重新编码。
	passThroughBytes = 4 << 20
	jpegQuality      = 85
)

var (
	ErrTooLarge    = errors.New("图片超过大小限制")
	ErrUnsupported = errors.New("图片格式不支持")
)

// ObjectReader 读取对象存储里的文件。
type ObjectReader interface {
	Open(ctx context.Context, key string) (io.ReadCloser, error)
}

// Loader 把对象存储里的图片转成模型可用的data URL：必要时缩小长边并转成JPEG。
type Loader struct {
	objects  ObjectReader
	maxBytes int64
	maxSide  int
}

func NewLoader(objects ObjectReader, maxBytes int64, maxSide int) *Loader {
	return &Loader{objects: objects, maxBytes: maxBytes, maxSide: maxSide}
}

// DataURL 读取storagePath处的图片并返回data URL。
func (l *Loader) DataURL(ctx context.Context, storagePath string) (string, error) {
	body, err := l.objects.Open(ctx, storagePath)
	if err != nil {
		return "", err
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, l.maxBytes+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > l.maxBytes {
		return "", ErrTooLarge
	}
	return Encode(data, l.maxSide)
}

// Encode 把图片字节转成data URL。HEIC等无法解码的格式返回ErrUnsupported。
func Encode(data []byte, maxSide int) (string, error) {
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", ErrUnsupported
	}
	if config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > maxPixels {
		return "", ErrTooLarge
	}
	fits := config.Width <= maxSide && config.Height <= maxSide
	if fits && len(data) <= passThroughBytes && (format == "jpeg" || format == "png") {
		return dataURL("image/"+format, data), nil
	}
	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnsupported, err)
	}
	bounds := decoded.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if !fits {
		if width >= height {
			width, height = maxSide, max(1, height*maxSide/bounds.Dx())
		} else {
			width, height = max(1, width*maxSide/bounds.Dy()), maxSide
		}
	}
	// 透明区域铺白底，JPEG没有alpha通道，否则会变成黑色。
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.ApproxBiLinear.Scale(canvas, canvas.Bounds(), decoded, bounds, draw.Over, nil)
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, canvas, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return "", err
	}
	return dataURL("image/jpeg", encoded.Bytes()), nil
}

func dataURL(mimeType string, data []byte) string {
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data)
}

// S3Objects 用RustFS的S3接口读取对象。
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
