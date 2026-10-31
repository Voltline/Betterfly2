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
	"os"
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

// testdata/sample.heic 是64x32的HEIC，左半红、右半蓝（macOS的sips生成）。
func TestEncodeConvertsHEICToJPEG(t *testing.T) {
	data, err := os.ReadFile("testdata/sample.heic")
	if err != nil {
		t.Fatal(err)
	}
	// 主品牌为mif1的HEIF也要能识别。
	mif1 := append([]byte(nil), data...)
	copy(mif1[8:12], "mif1")
	for name, input := range map[string][]byte{"heic": data, "mif1": mif1} {
		value, err := Encode(input, 1568)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		decoded := decodeDataURL(t, value, "image/jpeg")
		if decoded.Bounds().Dx() != 64 || decoded.Bounds().Dy() != 32 {
			t.Fatalf("%s size = %v", name, decoded.Bounds())
		}
		if !reddish(decoded.At(8, 16)) || !bluish(decoded.At(56, 16)) {
			t.Fatalf("%s colors: %v %v", name, decoded.At(8, 16), decoded.At(56, 16))
		}
	}
}

func reddish(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	return r>>8 > 150 && g>>8 < 100 && b>>8 < 100
}

func bluish(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	return b>>8 > 150 && r>>8 < 100 && g>>8 < 100
}

// withOrientation 在JPEG的SOI后插入只含Orientation的EXIF段。
func withOrientation(data []byte, orientation byte, order string) []byte {
	tiff := []byte("II*\x00\x08\x00\x00\x00\x01\x00\x12\x01\x03\x00\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00")
	tiff[18] = orientation
	if order == "MM" {
		tiff = []byte("MM\x00*\x00\x00\x00\x08\x00\x01\x01\x12\x00\x03\x00\x00\x00\x01\x00\x00\x00\x00\x00\x00\x00\x00")
		tiff[19] = orientation
	}
	payload := append([]byte("Exif\x00\x00"), tiff...)
	segment := append([]byte{0xFF, 0xE1, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)}, payload...)
	return append(append(append([]byte(nil), data[:2]...), segment...), data[2:]...)
}

func TestEncodeAppliesJPEGOrientation(t *testing.T) {
	picture := image.NewRGBA(image.Rect(0, 0, 64, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 64; x++ {
			if x < 32 {
				picture.Set(x, y, color.RGBA{R: 220, G: 30, B: 30, A: 255})
			} else {
				picture.Set(x, y, color.RGBA{R: 30, G: 30, B: 220, A: 255})
			}
		}
	}
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, picture, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	plain := buffer.Bytes()
	if jpegOrientation(plain) != 1 || jpegOrientation(withOrientation(plain, 6, "II")) != 6 || jpegOrientation(withOrientation(plain, 8, "MM")) != 8 {
		t.Fatal("orientation not parsed")
	}
	if jpegOrientation(withOrientation(plain, 9, "II")) != 1 || jpegOrientation(plain[:10]) != 1 {
		t.Fatal("invalid orientation accepted")
	}
	// 方向为1的小JPEG原样发送。
	if value, _ := Encode(withOrientation(plain, 1, "II"), 1568); !strings.HasPrefix(value, "data:image/jpeg;base64,") || len(value) < len(plain) {
		t.Fatalf("value = %.40s", value)
	}
	// 6：顺时针转90度，左边的红色到了上面。
	value, err := Encode(withOrientation(plain, 6, "II"), 1568)
	if err != nil {
		t.Fatal(err)
	}
	decoded := decodeDataURL(t, value, "image/jpeg")
	if decoded.Bounds().Dx() != 32 || decoded.Bounds().Dy() != 64 || !reddish(decoded.At(16, 8)) || !bluish(decoded.At(16, 56)) {
		t.Fatalf("orientation 6: size=%v top=%v bottom=%v", decoded.Bounds(), decoded.At(16, 8), decoded.At(16, 56))
	}
	// 8：逆时针转90度，红色在下面。
	value, _ = Encode(withOrientation(plain, 8, "MM"), 1568)
	if decoded := decodeDataURL(t, value, "image/jpeg"); !bluish(decoded.At(16, 8)) || !reddish(decoded.At(16, 56)) {
		t.Fatalf("orientation 8: top=%v bottom=%v", decoded.At(16, 8), decoded.At(16, 56))
	}
	// 3：旋转180度，宽高不变，红色到右边。
	value, _ = Encode(withOrientation(plain, 3, "II"), 1568)
	if decoded := decodeDataURL(t, value, "image/jpeg"); decoded.Bounds().Dx() != 64 || !bluish(decoded.At(8, 16)) || !reddish(decoded.At(56, 16)) {
		t.Fatalf("orientation 3: size=%v", decoded.Bounds())
	}
}

func TestEncodeRejectsUnsupportedAndHugeImages(t *testing.T) {
	for name, data := range map[string][]byte{
		"truncated heic": []byte("\x00\x00\x00\x18ftypheic"),
		"text":           []byte("not an image"),
	} {
		if _, err := Encode(data, 1568); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("%s err = %v", name, err)
		}
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
