package tools

import (
	"encoding/base64"
	"fmt"
	"os"

	"github.com/gurcuff91/harness/types"
)

// LoadImages reads local image files to attach to a message (the colleague
// channel's ColleagueAsk): each path must be a supported image (png, jpg,
// jpeg, gif, webp) by its extension AND by its content.
func LoadImages(paths []string) ([]types.ImageData, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	images := make([]types.ImageData, 0, len(paths))
	for _, p := range paths {
		if !isImagePath(p) {
			return nil, fmt.Errorf("%q is not a supported image type (png, jpg, jpeg, gif, webp)", p)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("reading image %q: %w", p, err)
		}
		mime, ok := sniffImageMime(data)
		if !ok {
			return nil, fmt.Errorf("%q has an image extension but its content is not a supported image format (detected: %s)", p, mime)
		}
		images = append(images, types.ImageData{
			MimeType: mime,
			Base64:   base64.StdEncoding.EncodeToString(data),
		})
	}
	return images, nil
}
