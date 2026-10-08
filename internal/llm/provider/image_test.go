package provider

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
)

func testBitmap() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for x := range 4 {
		for y := range 4 {
			img.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	return img
}

// testPNG returns a valid PNG that checkImage accepts.
func testPNG(t testing.TB) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, testBitmap()); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// testJPEG returns a valid JPEG that checkImage accepts.
func testJPEG(t testing.TB) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, testBitmap(), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func testGIF(t testing.TB) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := gif.Encode(&buf, testBitmap(), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// testWebP is a 1x1 lossless WebP; x/image ships a decoder only.
func testWebP(t testing.TB) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString("UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA==")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// testAnimatedWebPHeader is a VP8X header with the animation flag set and
// no frames: the x/image decoder cannot decode animation, so checkImage
// must accept it on the header alone.
func testAnimatedWebPHeader() []byte {
	data := []byte("RIFF\x16\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00")
	return append(data, 0x02, 0, 0, 0, 0x0f, 0, 0, 0x0f, 0, 0)
}

// truncated cuts the trailing chunks off an image — the partial download
// that providers reject ("failed to decode image") while its header still
// parses.
func truncated(data []byte) []byte {
	return data[:len(data)-16]
}

func TestCheckImage(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		wantMIME string
		wantErr  string
	}{
		{name: "png", data: testPNG(t), wantMIME: "image/png"},
		{name: "jpeg", data: testJPEG(t), wantMIME: "image/jpeg"},
		{name: "gif", data: testGIF(t), wantMIME: "image/gif"},
		{name: "webp", data: testWebP(t), wantMIME: "image/webp"},
		{name: "animated webp is checked by header only", data: testAnimatedWebPHeader(), wantMIME: "image/webp"},
		{name: "truncated png", data: truncated(testPNG(t)), wantErr: "corrupt png data"},
		{name: "truncated jpeg", data: truncated(testJPEG(t)), wantErr: "corrupt jpeg data"},
		{name: "random bytes", data: []byte("fakeimage"), wantErr: "not a PNG, JPEG, GIF or WebP image"},
		{name: "empty", data: nil, wantErr: "not a PNG, JPEG, GIF or WebP image"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Twice: the second call is served from the memo.
			for range 2 {
				mime, err := checkImage(tt.data)
				if tt.wantErr != "" {
					if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
						t.Fatalf("err = %v, want %q", err, tt.wantErr)
					}
					continue
				}
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if mime != tt.wantMIME {
					t.Fatalf("mime = %q, want %q", mime, tt.wantMIME)
				}
			}
		})
	}
}

// TestConvertersReplaceCorruptImages covers the poisoned-session shape: a
// truncated image persisted in history would fail every later turn (Kimi
// 400s, Bedrock resets the stream), so both converters swap it for a note
// naming the saved path.
func TestConvertersReplaceCorruptImages(t *testing.T) {
	bc := message.BinaryContent{MIMEType: "image/png", Path: ".opencode/bridge/media/photo.png", Data: truncated(testPNG(t))}

	block := convertBinaryContent(bc, true)
	if block.OfText == nil {
		t.Fatalf("anthropic: want a text note, got %+v", block)
	}
	part := convertBinaryContentOpenAI(bc)
	if part.OfText == nil {
		t.Fatalf("openai: want a text note, got %+v", part)
	}
	for _, note := range []string{block.OfText.Text, part.OfText.Text} {
		if !strings.Contains(note, "Image omitted") || !strings.Contains(note, "corrupt png data") || !strings.Contains(note, bc.Path) {
			t.Errorf("note %q should name the corruption and the saved path", note)
		}
	}
}

// TestConvertersSendSniffedImageType covers a JPEG labeled image/png, which
// Anthropic rejects as a media-type mismatch: both converters must send the
// type the bytes actually hold.
func TestConvertersSendSniffedImageType(t *testing.T) {
	bc := message.BinaryContent{MIMEType: "image/png", Data: testJPEG(t)}

	block := convertBinaryContent(bc, true)
	if block.OfImage == nil || block.OfImage.Source.OfBase64 == nil {
		t.Fatalf("anthropic: want a base64 image block, got %+v", block)
	}
	if got := block.OfImage.Source.OfBase64.MediaType; got != "image/jpeg" {
		t.Errorf("anthropic media_type = %q, want image/jpeg", got)
	}
	part := convertBinaryContentOpenAI(bc)
	if part.OfImageURL == nil || !strings.HasPrefix(part.OfImageURL.ImageURL.URL, "data:image/jpeg;base64,") {
		t.Errorf("openai: want a data:image/jpeg URL, got %+v", part)
	}
}

// TestConvertMessagesChecksToolResultImages covers images returned by
// view_image, which replay from history like attachments: a corrupt file
// becomes a text tool result naming it, a mislabeled one is sent under its
// real type.
func TestConvertMessagesChecksToolResultImages(t *testing.T) {
	toolImage := func(data []byte, mime string) message.ToolResult {
		content, _ := json.Marshal(map[string]string{"type": "image", "data": base64.StdEncoding.EncodeToString(data), "mimeType": mime})
		metadata, _ := json.Marshal(tools.ViewImageResponseMetadata{MimeType: mime, FilePath: "/work/shot.png"})
		return message.ToolResult{ToolCallID: "call-1", Name: tools.ViewImageToolName, Type: message.ToolResultTypeImage, Content: string(content), Metadata: string(metadata)}
	}
	a := newAnthropicClient(providerClientOptions{apiKey: "test-key", model: models.SupportedModels[models.Claude46Opus]}).(*anthropicClient)
	convert := func(t *testing.T, result message.ToolResult) []any {
		t.Helper()
		converted := a.convertMessages([]message.Message{newMsg(message.Tool, result)})
		if len(converted) != 1 || len(converted[0].Content) != 1 || converted[0].Content[0].OfToolResult == nil {
			t.Fatalf("unexpected conversion shape: %+v", converted)
		}
		var out []any
		for _, c := range converted[0].Content[0].OfToolResult.Content {
			switch {
			case c.OfText != nil:
				out = append(out, c.OfText.Text)
			case c.OfImage != nil:
				out = append(out, c.OfImage.Source.OfBase64)
			}
		}
		return out
	}

	t.Run("corrupt image becomes a note", func(t *testing.T) {
		got := convert(t, toolImage(truncated(testPNG(t)), "image/png"))
		note, ok := got[0].(string)
		if len(got) != 1 || !ok || !strings.Contains(note, "corrupt png data") || !strings.Contains(note, "/work/shot.png") {
			t.Fatalf("want a note naming the corruption and the file, got %+v", got)
		}
	})
	t.Run("mislabeled image is sent under its real type", func(t *testing.T) {
		got := convert(t, toolImage(testJPEG(t), "image/png"))
		if len(got) != 1 {
			t.Fatalf("want one image, got %+v", got)
		}
		if src, ok := got[0].(*anthropic.Base64ImageSourceParam); !ok || src.MediaType != "image/jpeg" {
			t.Fatalf("want a base64 image/jpeg source, got %+v", got[0])
		}
	})
}
