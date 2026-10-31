package media

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
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
	"github.com/gen2brain/heic"
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

func init() {
	// heic包登记了heic、heix、hevc、hevx、msf1；补上其余HEVC品牌和通用的mif1，
	// mif1里若是AVIF等解不了的编码，解码失败时按格式不支持处理。
	for _, brand := range []string{"heim", "heis", "mif1"} {
		image.RegisterFormat("heic", "????ftyp"+brand, heic.Decode, heic.DecodeConfig)
	}
}

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

// Encode 把图片字节转成data URL。HEIC与带旋转方向的JPEG重新编码为JPEG；无法解码的格式返回ErrUnsupported。
func Encode(data []byte, maxSide int) (url string, err error) {
	// 解码器处理的是用户上传的任意字节，出现意外panic时当作格式不支持。
	defer func() {
		if recovered := recover(); recovered != nil {
			url, err = "", fmt.Errorf("%w: %v", ErrUnsupported, recovered)
		}
	}()
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", ErrUnsupported
	}
	if config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > maxPixels {
		return "", ErrTooLarge
	}
	orientation := 1
	if format == "jpeg" {
		orientation = jpegOrientation(data)
	}
	fits := config.Width <= maxSide && config.Height <= maxSide
	if fits && len(data) <= passThroughBytes && orientation == 1 && (format == "jpeg" || format == "png") {
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
	if err := jpeg.Encode(&encoded, orient(canvas, orientation), &jpeg.Options{Quality: jpegQuality}); err != nil {
		return "", err
	}
	return dataURL("image/jpeg", encoded.Bytes()), nil
}

// jpegOrientation 读取JPEG里EXIF的Orientation（1–8），没有或无法解析时返回1。
// 重新编码会丢掉EXIF，所以需要先按方向把像素转正。
func jpegOrientation(data []byte) int {
	for i := 2; i+4 <= len(data) && data[0] == 0xFF && data[1] == 0xD8; {
		if data[i] != 0xFF {
			return 1
		}
		marker := data[i+1]
		if marker == 0xDA || marker == 0xD9 { // 图像数据开始，EXIF只会出现在这之前
			return 1
		}
		length := int(data[i+2])<<8 | int(data[i+3])
		if length < 2 || i+2+length > len(data) {
			return 1
		}
		segment := data[i+4 : i+2+length]
		if marker == 0xE1 && len(segment) >= 6 && string(segment[:6]) == "Exif\x00\x00" {
			return exifOrientation(segment[6:])
		}
		i += 2 + length
	}
	return 1
}

func exifOrientation(tiff []byte) int {
	if len(tiff) < 8 {
		return 1
	}
	var order binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	offset := int(order.Uint32(tiff[4:8]))
	if offset < 8 || offset+2 > len(tiff) {
		return 1
	}
	count := int(order.Uint16(tiff[offset:]))
	for entry := offset + 2; count > 0 && entry+12 <= len(tiff); count, entry = count-1, entry+12 {
		if order.Uint16(tiff[entry:]) == 0x0112 { // Orientation，类型SHORT，值在条目内
			if value := int(order.Uint16(tiff[entry+8:])); value >= 1 && value <= 8 {
				return value
			}
			return 1
		}
	}
	return 1
}

// orient 按EXIF方向把图片转正：2–4是翻转与180度，5–8会交换宽高。
func orient(src *image.RGBA, orientation int) *image.RGBA {
	if orientation < 2 || orientation > 8 {
		return src
	}
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	dw, dh := w, h
	if orientation >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch orientation {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			si, di := src.PixOffset(x, y), dst.PixOffset(dx, dy)
			copy(dst.Pix[di:di+4], src.Pix[si:si+4])
		}
	}
	return dst
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
