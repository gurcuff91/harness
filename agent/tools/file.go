package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gurcuff91/harness/types"
)

// imageExtToMime maps supported image extensions to MIME types. Used ONLY
// to decide whether a path LOOKS like an image worth attempting to read as
// one (isImagePath) — never to decide the mime_type actually sent to the
// provider or persisted to the session's .jsonl. See sniffImageMime's doc
// comment for why: a mismatched extension (e.g. a JPEG saved as
// "screenshot.png") silently corrupted a session permanently until this was
// fixed — the provider rejects the byte/label mismatch on every future turn
// that replays the history, and there's no way to un-persist it short of
// hand-editing the .jsonl.
var imageExtToMime = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
}

func isImagePath(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	_, ok := imageExtToMime[ext]
	return ok
}

// supportedImageMimes is the set sniffImageMime will ever return — the same
// four formats imageExtToMime lists, just keyed by MIME type instead of
// extension, so a real screenshot util's/pdf-preview's/random other image
// container http.DetectContentType might recognize (bmp, tiff, ico, …)
// still gets rejected with a clear error instead of silently reaching the
// provider as an unsupported type.
var supportedImageMimes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

// sniffImageMime determines an image's REAL mime type from its bytes (via
// http.DetectContentType — stdlib, no new dependency), never from the
// file's extension or a caller-supplied label. Returns ok=false if the
// content isn't one of the four types this tool (and the providers it
// feeds) actually supports, regardless of what the file's name claims.
//
// This exists because of a real, reproduced-in-the-field bug: a file named
// "campaign_setup_current.png" whose actual bytes were JPEG got tagged
// mime_type: "image/png" (inferred from the ".png" extension alone) and
// permanently corrupted a session — Anthropic rejects the byte/label
// mismatch, and since the full history replays on every turn, the SAME
// session kept failing forever until the .jsonl was hand-edited to fix the
// one bad entry. Sniffing the real bytes at read time makes that class of
// corruption impossible to persist in the first place.
func sniffImageMime(data []byte) (mime string, ok bool) {
	detected := http.DetectContentType(data)
	// DetectContentType can return a mime with a "; charset=..." suffix for
	// text-ish types (never for the binary image formats we care about, but
	// guard against it anyway rather than assume).
	if i := strings.IndexByte(detected, ';'); i >= 0 {
		detected = detected[:i]
	}
	return detected, supportedImageMimes[detected]
}

// maxImageFileBytes caps the RAW file size Read will base64-encode as an
// image. Anthropic rejects a tool_result image whose base64 payload exceeds
// 10MB (10_485_760 bytes) — base64 expands raw bytes by 4/3, so encoding is
// rejected well before that cap unless the source file itself is bounded
// well under it. 7.5MB * 4/3 = 10MB exactly, so this is the largest raw file
// that can never produce an over-limit payload for ANY correctly-encoding
// base64 implementation (padding only ever adds up to 2 extra bytes, not
// enough to matter at this scale). Enforced via os.Stat BEFORE reading the
// file into memory or encoding it — a provider 400 after committing an
// 11MB+ base64 string to the session's persisted history is not just a
// failed call, it corrupts that history file for every future turn (the
// oversized tool_result is replayed on every resume, guaranteeing the same
// 400 again) — this guard exists specifically to make that unrepresentable.
const maxImageFileBytes = 10_485_760 * 3 / 4 // 7,864,320 bytes (7.5MB)

type readFileInput struct {
	Path   string `json:"path" validate:"required"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// ReadFile returns the Read tool. cwd is the session's logical working
// directory — a relative path the model passes resolves against it (see
// resolvePath); an absolute path is used as-is.
func ReadFile(cwd string) Tool {
	return Tool{
		Def: types.ToolDef{
			Name:        "Read",
			Description: "Read the contents of a file. Supports text files and images (jpg, png, gif, webp) — images are sent as attachments. For text files, use offset and limit to read specific line ranges. Always prefer this over bash cat/head/tail for reading file content.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path":   {"type": "string",  "description": "Path to the file to read"},
					"offset": {"type": "integer", "description": "Line offset to start reading from (0-based)"},
					"limit":  {"type": "integer", "description": "Maximum number of lines to read"}
				},
				"required": ["path"]
			}`),
		},
		ExecuteRich: func(ctx context.Context, input json.RawMessage) (string, []types.ImageData, error) {
			var args readFileInput
			if err := json.Unmarshal(input, &args); err != nil {
				return fmt.Sprintf("Error parsing input: %v", err), nil, err
			}
			if err := requireFields(&args); err != nil {
				return err.Error(), nil, err
			}
			path := resolvePath(cwd, args.Path)

			// Image file — return as ImageData
			if isImagePath(path) {
				// Check size via Stat BEFORE reading/encoding — see
				// maxImageFileBytes' comment for why this must happen before
				// the file is ever loaded into memory or base64-encoded.
				if info, statErr := os.Stat(path); statErr == nil && info.Size() > maxImageFileBytes {
					err := fmt.Errorf("image too large: %d bytes exceeds the %d byte limit (base64 encoding would exceed the provider's 10MB payload cap) — resize or compress the image before reading it", info.Size(), maxImageFileBytes)
					return err.Error(), nil, err
				}

				data, err := os.ReadFile(path)
				if err != nil {
					return fmt.Sprintf("Error reading image: %v", err), nil, err
				}

				// mime_type sent to the provider (and persisted to the
				// session's .jsonl) MUST reflect the REAL bytes, never the
				// file's extension — see sniffImageMime's doc comment for
				// the exact corruption this prevents.
				mime, ok := sniffImageMime(data)
				if !ok {
					err := fmt.Errorf("%q has an image extension but its content is not a supported image format (detected: %s) — supported: png, jpeg, gif, webp", path, mime)
					return err.Error(), nil, err
				}

				img := types.ImageData{
					MimeType: mime,
					Base64:   base64.StdEncoding.EncodeToString(data),
				}
				return fmt.Sprintf("Image loaded: %s (%s, %d bytes)", path, mime, len(data)), []types.ImageData{img}, nil
			}

			// Text file
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Sprintf("Error reading file: %v", err), nil, err
			}
			lines := strings.Split(string(data), "\n")
			totalLines := len(lines)
			if args.Offset > 0 {
				if args.Offset >= totalLines {
					return fmt.Sprintf("Offset %d beyond end of file (%d lines total)", args.Offset, totalLines), nil, nil
				}
				lines = lines[args.Offset:]
			}
			if args.Limit > 0 && args.Limit < len(lines) {
				lines = lines[:args.Limit]
			}
			content := strings.Join(lines, "\n")
			// Keep the HEAD. Read already supports offset/limit for paging, so a
			// truncated read tells the model to continue with a higher offset.
			content = ApplyTruncation("read", content, true)
			if args.Offset > 0 || args.Limit > 0 {
				content = fmt.Sprintf("[lines %d-%d of %d]\n", args.Offset+1, args.Offset+len(lines), totalLines) + content
			}
			return content, nil, nil
		},
	}
}

type writeFileInput struct {
	Path string `json:"path" validate:"required"`
	// Content deliberately has NO validate:"required" tag — an empty string
	// is a legitimate call (create an empty file), unlike Path.
	Content string `json:"content"`
}

// WriteFile returns the Write tool. cwd is the session's logical working
// directory — a relative path the model passes resolves against it (see
// resolvePath); an absolute path is used as-is.
func WriteFile(cwd string) Tool {
	return Tool{
		Def: types.ToolDef{
			Name:        "Write",
			Description: "Create or overwrite a file with the given content. WARNING: replaces the entire file — use edit for partial changes. Creates parent directories if needed.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Path to write the file"},
					"content": {"type": "string", "description": "Content to write to the file"}
				},
				"required": ["path", "content"]
			}`),
		},
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			var args writeFileInput
			if err := json.Unmarshal(input, &args); err != nil {
				return fmt.Sprintf("Error parsing input: %v", err), err
			}
			if err := requireFields(&args); err != nil {
				return err.Error(), err
			}
			path := resolvePath(cwd, args.Path)
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				return fmt.Sprintf("Error creating directory: %v", err), err
			}
			if err := os.WriteFile(path, []byte(args.Content), 0644); err != nil {
				return fmt.Sprintf("Error writing file: %v", err), err
			}
			return fmt.Sprintf("Wrote %d bytes to %s", len(args.Content), path), nil
		},
	}
}
