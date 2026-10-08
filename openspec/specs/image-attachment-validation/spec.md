# Image Attachment Validation

## Purpose

Defines how image data is checked before it reaches a provider. Image attachments (bridge uploads, TUI attachments) and images returned by `view_image` persist in session history and replay on every request and count_tokens call, so one image a provider rejects fails every later turn of its session. Covers the anthropic-dialect converter (Anthropic, Kimi, Bedrock Claude, Vertex Claude) and the OpenAI chat-completions converter (OpenAI-compatible providers, Bedrock Kimi). The Gemini converter is out of scope.

## Requirements

### Requirement: Undecodable images are replaced by a note
Providers reject image data they cannot decode: Kimi answers `400 failed to decode image`, Bedrock resets the HTTP/2 stream (`INTERNAL_ERROR`). Before an `image/png`, `image/jpeg`, `image/gif` or `image/webp` attachment is sent, its data SHALL be decoded; when the data is not one of those formats, its header is corrupt, its dimensions are not positive, or its pixel data fails to decode, the image SHALL be replaced by a text block `[Image omitted (<n> bytes): <reason>; the file is saved at "<path>" and can be inspected with file tools]` (the path clause only when the attachment has a saved path). Validation SHALL run at conversion time, not at ingestion, so sessions already holding such an image recover on their next turn. Results SHALL be memoized by content hash so replayed images are not decoded on every turn.

#### Scenario: Truncated upload in history
- **WHEN** a session holds a truncated PNG saved at `.opencode/bridge/media/photo.png`
- **THEN** the request carries a text block naming the corruption (`corrupt png data`) and that path instead of the image, and the turn succeeds on Kimi, Bedrock Kimi and Bedrock Claude

#### Scenario: Image a decoder cannot fully check
- **WHEN** an image exceeds 25 megapixels, is an animated WebP, or uses a JPEG/PNG feature the Go decoder does not implement
- **THEN** only its header is checked and a valid header keeps the image block

### Requirement: Images are sent under the media type their bytes hold
Anthropic-dialect endpoints reject a declared media type that disagrees with the data (`400 The image was specified using the image/png media type, but the image appears to be a image/jpeg image`). Image blocks and OpenAI data URLs SHALL carry the media type detected from the data, not the declared one.

#### Scenario: JPEG labeled image/png
- **WHEN** an attachment declared `image/png` holds JPEG data
- **THEN** the anthropic image block carries `media_type: image/jpeg` and the OpenAI image part's URL starts with `data:image/jpeg;base64,`

### Requirement: view_image results get the same check
An image returned by `view_image` SHALL be checked the same way when converted for the anthropic dialect: an undecodable one SHALL become a text tool result naming the reason and the file path from the tool's metadata, and a mislabeled one SHALL be sent under its detected type.

#### Scenario: Corrupt file viewed by the agent
- **WHEN** `view_image` returned a truncated PNG read from `/work/shot.png`
- **THEN** the tool result block carries a text note naming the corruption and `/work/shot.png`, and no image
