package media

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"strings"
	"testing"
)

type memoryObjects map[string][]byte

func (m memoryObjects) Open(_ context.Context, key string) (io.ReadCloser, error) {
	data, ok := m[key]
	if !ok {
		return nil, errors.New("not found")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func encodePNG(t *testing.T, width, height int, fill color.Color) []byte {
	t.Helper()
	picture := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			picture.Set(x, y, fill)
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, picture); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func decodeDataURL(t *testing.T, value, wantType string) image.Image {
	t.Helper()
	prefix := "data:" + wantType + ";base64,"
	if !strings.HasPrefix(value, prefix) {
		t.Fatalf("data URL prefix = %.40s", value)
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, prefix))
	if err != nil {
		t.Fatal(err)
	}
	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestEncodePassesSmallPNGThrough(t *testing.T) {
	data := encodePNG(t, 40, 20, color.NRGBA{R: 255, A: 255})
	value, err := Encode(data, 1568)
	if err != nil || value != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(data) {
		t.Fatalf("value=%.60s err=%v", value, err)
	}
}

func TestEncodeResizesLongSideAndFlattensAlpha(t *testing.T) {
	data := encodePNG(t, 400, 100, color.NRGBA{})
	value, err := Encode(data, 200)
	if err != nil {
		t.Fatal(err)
	}
	decoded := decodeDataURL(t, value, "image/jpeg")
	if decoded.Bounds().Dx() != 200 || decoded.Bounds().Dy() != 50 {
		t.Fatalf("size = %v", decoded.Bounds())
	}
	if r, g, b, _ := decoded.At(100, 25).RGBA(); r>>8 < 240 || g>>8 < 240 || b>>8 < 240 {
		t.Fatalf("transparent pixel not white: %d %d %d", r>>8, g>>8, b>>8)
	}
}

func TestEncodeConvertsGIFToJPEG(t *testing.T) {
	palette := color.Palette{color.Black, color.White}
	var buffer bytes.Buffer
	if err := gif.EncodeAll(&buffer, &gif.GIF{Image: []*image.Paletted{image.NewPaletted(image.Rect(0, 0, 10, 30), palette)}, Delay: []int{0}}); err != nil {
		t.Fatal(err)
	}
	value, err := Encode(buffer.Bytes(), 1568)
	if err != nil {
		t.Fatal(err)
	}
	if decoded := decodeDataURL(t, value, "image/jpeg"); decoded.Bounds().Dx() != 10 || decoded.Bounds().Dy() != 30 {
		t.Fatalf("size = %v", decoded.Bounds())
	}
}

func TestEncodeRejectsUnsupportedAndHugeImages(t *testing.T) {
	if _, err := Encode([]byte("\x00\x00\x00\x18ftypheic"), 1568); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("heic err = %v", err)
	}
	// 只有头部声明了超大尺寸，解码前就要拒绝。
	var header bytes.Buffer
	if err := jpeg.Encode(&header, image.NewGray(image.Rect(0, 0, 8, 8)), nil); err != nil {
		t.Fatal(err)
	}
	huge := header.Bytes()
	for i := 0; i+8 < len(huge); i++ {
		if huge[i] == 0xFF && huge[i+1] == 0xC0 { // SOF0: 长度2字节、精度1字节，然后是高和宽
			huge[i+5], huge[i+6], huge[i+7], huge[i+8] = 0xFF, 0xFF, 0xFF, 0xFF
			break
		}
	}
	if _, err := Encode(huge, 1568); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("huge err = %v", err)
	}
}

func TestLoaderEnforcesByteLimit(t *testing.T) {
	data := encodePNG(t, 4, 4, color.White)
	objects := memoryObjects{"ab/abc": data}
	if value, err := NewLoader(objects, int64(len(data)), 1568).DataURL(context.Background(), "ab/abc"); err != nil || value == "" {
		t.Fatalf("value=%q err=%v", value, err)
	}
	if _, err := NewLoader(objects, int64(len(data))-1, 1568).DataURL(context.Background(), "ab/abc"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v", err)
	}
	if _, err := NewLoader(objects, 1<<20, 1568).DataURL(context.Background(), "missing"); err == nil {
		t.Fatal("missing object accepted")
	}
}
