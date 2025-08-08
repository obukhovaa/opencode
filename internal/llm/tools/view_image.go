package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/logging"
)

type ViewImageParams struct {
	FilePath string `json:"file_path"`
}

type viewImageTool struct{}

type ViewImageResponseMetadata struct {
	MimeType string `json:"mime_type"`
	FilePath string `json:"file_path"`
}

type imageContent struct {
	Type     string `json:"type"`
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
}

const (
	ViewImageToolName    = "view_image"
	MaxImageSize         = 5 * 1024 * 1024 // 5MB
	viewImageDescription = `Read an image file as base64 encoded data and MIME type.

WHEN TO USE THIS TOOL:
- Use when you need to analyze or examine image files
- Helpful for understanding visual content in the codebase
- Perfect for processing screenshots, diagrams, or other visual assets

HOW TO USE:
- Provide the path to the image file you want to view
- The tool will return the image as base64 encoded content and MIME type

SUPPORTED FILE FORMATS:
- PNG, JPEG, GIF, WebP, BMP

LIMITATIONS:
- Maximum file size is 5MB

TIPS:
- Use with Glob tool to first find image files you want to view
- Check file size before processing very large images
- Combine with other tools for comprehensive image analysis`
)

var supportedImageTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",
}

func NewViewImageTool() BaseTool {
	return &viewImageTool{}
}

func (v *viewImageTool) Info() ToolInfo {
	return ToolInfo{
		Name:        ViewImageToolName,
		Description: viewImageDescription,
		Parameters: map[string]any{
			"file_path": map[string]any{
				"type":        "string",
				"description": "The path to the image file to read",
			},
		},
		Required: []string{"file_path"},
	}
}

func (v *viewImageTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params ViewImageParams
	logging.Debug("view_image tool params", "params", call.Input)
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("error parsing parameters: %s", err)), nil
	}

	if params.FilePath == "" {
		return NewTextErrorResponse("file_path is required"), nil
	}

	// Handle relative paths
	filePath := params.FilePath
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(config.WorkingDirectory(), filePath)
	}

	// Check if file exists
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return NewTextErrorResponse(fmt.Sprintf("File not found: %s", filePath)), nil
		}
		return ToolResponse{}, fmt.Errorf("error accessing file: %w", err)
	}

	// Check if it's a directory
	if fileInfo.IsDir() {
		return NewTextErrorResponse(fmt.Sprintf("Path is a directory, not a file: %s", filePath)), nil
	}

	// Check file size
	if fileInfo.Size() > MaxImageSize {
		return NewTextErrorResponse(fmt.Sprintf("Image file is too large (%d bytes). Maximum size is %d bytes",
			fileInfo.Size(), MaxImageSize)), nil
	}

	// Verify file format
	ext := strings.ToLower(filepath.Ext(filePath))
	mimeType, supported := supportedImageTypes[ext]
	if !supported {
		return NewTextErrorResponse(fmt.Sprintf("Unsupported image format: %s. Supported formats: %s",
			ext, getSupportedFormats())), nil
	}

	// Read file content
	fileContent, err := os.ReadFile(filePath)
	if err != nil {
		return ToolResponse{}, fmt.Errorf("error reading image file: %w", err)
	}

	// Convert to base64
	base64Content := base64.StdEncoding.EncodeToString(fileContent)
	imgContent := imageContent{
		Type:     "image",
		Data:     base64Content,
		MimeType: mimeType,
	}
	content, err := json.Marshal(imgContent)
	if err != nil {
		return ToolResponse{}, fmt.Errorf("error marshaling image content: %w", err)
	}

	return WithResponseMetadata(
		NewImageResponse(string(content)),
		ViewImageResponseMetadata{
			MimeType: mimeType,
			FilePath: filePath,
		},
	), nil
}

func getSupportedFormats() string {
	var formats []string
	for ext := range supportedImageTypes {
		formats = append(formats, ext)
	}
	return strings.Join(formats, ", ")
}

