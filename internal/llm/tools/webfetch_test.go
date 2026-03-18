package tools

import (
	"testing"
)

func TestIsBinaryContent(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        []byte
		want        bool
	}{
		{
			name:        "JAR file by content type",
			contentType: "application/java-archive",
			body:        []byte("PK\x03\x04"),
			want:        true,
		},
		{
			name:        "octet-stream",
			contentType: "application/octet-stream",
			body:        []byte{0x00, 0x01, 0x02},
			want:        true,
		},
		{
			name:        "zip file",
			contentType: "application/zip",
			body:        []byte("PK\x03\x04"),
			want:        true,
		},
		{
			name:        "PDF file",
			contentType: "application/pdf",
			body:        []byte("%PDF-1.4"),
			want:        true,
		},
		{
			name:        "image PNG",
			contentType: "image/png",
			body:        []byte{0x89, 0x50, 0x4E, 0x47},
			want:        true,
		},
		{
			name:        "audio mpeg",
			contentType: "audio/mpeg",
			body:        []byte{0xFF, 0xFB},
			want:        true,
		},
		{
			name:        "video mp4",
			contentType: "video/mp4",
			body:        []byte{0x00, 0x00},
			want:        true,
		},
		{
			name:        "font woff2",
			contentType: "font/woff2",
			body:        []byte{0x77, 0x4F, 0x46, 0x32},
			want:        true,
		},
		{
			name:        "content type with charset",
			contentType: "application/pdf; charset=binary",
			body:        []byte("%PDF"),
			want:        true,
		},
		{
			name:        "plain text",
			contentType: "text/plain",
			body:        []byte("Hello, world!"),
			want:        false,
		},
		{
			name:        "HTML",
			contentType: "text/html; charset=utf-8",
			body:        []byte("<html><body>test</body></html>"),
			want:        false,
		},
		{
			name:        "JSON",
			contentType: "application/json",
			body:        []byte(`{"key": "value"}`),
			want:        false,
		},
		{
			name:        "unknown content type but valid UTF-8 body",
			contentType: "",
			body:        []byte("This is valid UTF-8 text"),
			want:        false,
		},
		{
			name:        "unknown content type with invalid UTF-8 body",
			contentType: "",
			body:        []byte{0x80, 0x81, 0x82, 0xFF, 0xFE, 0x00, 0x01},
			want:        true,
		},
		{
			name:        "text content type but binary body",
			contentType: "text/plain",
			body:        []byte{0x80, 0x81, 0x82, 0xFF, 0xFE},
			want:        true,
		},
		{
			name:        "empty body with text content type",
			contentType: "text/plain",
			body:        []byte{},
			want:        false,
		},
		{
			name:        "empty body with no content type",
			contentType: "",
			body:        []byte{},
			want:        false,
		},
		{
			name:        "wasm binary",
			contentType: "application/wasm",
			body:        []byte{0x00, 0x61, 0x73, 0x6D},
			want:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isBinaryContent(tt.contentType, tt.body)
			if got != tt.want {
				t.Errorf("isBinaryContent(%q, body) = %v, want %v", tt.contentType, got, tt.want)
			}
		})
	}
}
