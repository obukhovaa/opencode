package provider

import (
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/message"
)

// TestConvertBinaryContentOpenAIPartTypes locks in the MIME-type routing
// for binary attachments — the same defect class fixed in the anthropic
// converter: wrapping every attachment in an image_url part makes any
// request containing a PDF invalid, and since attachments persist in
// session history, one bad attachment poisons every subsequent turn.
func TestConvertBinaryContentOpenAIPartTypes(t *testing.T) {
	tests := []struct {
		name     string
		bc       message.BinaryContent
		wantKind string
	}{
		{
			name:     "png stays an image part",
			bc:       message.BinaryContent{MIMEType: "image/png", Data: []byte{1, 2, 3}},
			wantKind: "image",
		},
		{
			name:     "mime parameters are stripped",
			bc:       message.BinaryContent{MIMEType: "image/jpeg; charset=binary", Data: []byte{1}},
			wantKind: "image",
		},
		{
			name:     "pdf becomes a file part",
			bc:       message.BinaryContent{MIMEType: "application/pdf", Path: ".opencode/bridge/media/patent.pdf", Data: []byte("%PDF-1.7")},
			wantKind: "file",
		},
		{
			name:     "plain text is inlined as a text part",
			bc:       message.BinaryContent{MIMEType: "text/plain; charset=utf-8", Data: []byte("hello")},
			wantKind: "text",
		},
		{
			name:     "audio degrades to a text placeholder",
			bc:       message.BinaryContent{MIMEType: "audio/ogg", Path: ".opencode/bridge/media/voice.ogg", Data: []byte{1, 2}},
			wantKind: "text",
		},
		{
			// Zero-byte payloads must not be inlined as an empty text part;
			// they degrade to the placeholder note instead (asserted below).
			name:     "zero-byte text file degrades to a text placeholder",
			bc:       message.BinaryContent{MIMEType: "text/plain", Path: "empty.log", Data: nil},
			wantKind: "text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			part := convertBinaryContentOpenAI(tt.bc)
			var gotKind string
			switch {
			case part.OfImageURL != nil:
				gotKind = "image"
			case part.OfFile != nil:
				gotKind = "file"
			case part.OfText != nil:
				gotKind = "text"
			default:
				gotKind = "other"
			}
			if gotKind != tt.wantKind {
				t.Fatalf("got %s part, want %s", gotKind, tt.wantKind)
			}
			if tt.wantKind == "file" {
				file := part.OfFile.File
				if file.Filename.Value != "patent.pdf" {
					t.Errorf("filename = %q, want %q", file.Filename.Value, "patent.pdf")
				}
				if !strings.HasPrefix(file.FileData.Value, "data:application/pdf;base64,") {
					t.Errorf("file_data should be a data URL, got prefix %q", file.FileData.Value[:min(40, len(file.FileData.Value))])
				}
			}
			if len(tt.bc.Data) == 0 && tt.wantKind == "text" {
				// Empty payloads must carry the placeholder note, never an
				// empty inlined text part (the API rejects empty content).
				if !strings.Contains(part.OfText.Text, "[Attachment of unsupported media type") {
					t.Errorf("zero-byte payload not substituted with placeholder: %q", part.OfText.Text)
				}
			}
		})
	}
}

// TestOpenAIConvertMessagesSkipsEmptyUserText covers the caption-less
// bridge attachment: empty text must be dropped, the attachment kept, and
// a fully-blank user message skipped instead of sent with empty content.
func TestOpenAIConvertMessagesSkipsEmptyUserText(t *testing.T) {
	client := &openaiClient{}

	result := client.convertMessages([]message.Message{
		newMsg(message.User,
			message.TextContent{Text: ""},
			message.BinaryContent{MIMEType: "application/pdf", Data: []byte("%PDF-1.7")},
		),
	})
	// index 0 is the injected system message
	if len(result) != 2 {
		t.Fatalf("got %d messages, want 2 (system + user)", len(result))
	}
	user := result[1].OfUser
	if user == nil {
		t.Fatal("expected user message at index 1")
	}
	parts := user.Content.OfArrayOfContentParts
	if len(parts) != 1 {
		t.Fatalf("got %d content parts, want 1 (empty text must be dropped)", len(parts))
	}
	if parts[0].OfFile == nil {
		t.Fatal("expected the remaining part to be the pdf file part")
	}

	empty := client.convertMessages([]message.Message{
		newMsg(message.User, message.TextContent{Text: "   "}),
	})
	if len(empty) != 1 {
		t.Fatalf("got %d messages, want 1 (system only) for blank user message", len(empty))
	}
}
