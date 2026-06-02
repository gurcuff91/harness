package llm

import (
	"encoding/base64"
	"fmt"
	"github.com/gurcuff91/harness/types"
	"os"
	"path/filepath"
	"strings"
)

// Supported image extensions → MIME types
var imageExtToMime = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
}

// IsImagePath returns true if the path looks like a supported image file.
func IsImagePath(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	_, ok := imageExtToMime[ext]
	return ok
}

// LoadImage reads an image file and returns types.ImageData for the LLM.
func LoadImage(path string) (types.ImageData, error) {
	ext := strings.ToLower(filepath.Ext(path))
	mime, ok := imageExtToMime[ext]
	if !ok {
		return types.ImageData{}, fmt.Errorf("unsupported image format: %s", ext)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return types.ImageData{}, fmt.Errorf("read image %s: %w", path, err)
	}

	return types.ImageData{
		MimeType: mime,
		Base64:   base64.StdEncoding.EncodeToString(data),
	}, nil
}
