package telegram

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gurcuff91/harness/types"
)

// maxAttachmentBytes is the largest file a message brings in. The Bot API
// itself serves at most 20 MB through getFile, so that is the real ceiling. A
// variable for tests.
var maxAttachmentBytes int64 = 20 << 20

var (
	errTooLarge   = errors.New("too large")
	errExecutable = errors.New("executable")
)

const skippedExecutable = "executable"

// executableExtensions are installers, packages and native libraries: skipped
// by name, never downloaded. Scripts are text and pass (reading one is fine;
// running it is what the directive forbids).
var executableExtensions = map[string]bool{
	".exe": true, ".dll": true, ".msi": true, ".scr": true, ".com": true,
	".app": true, ".dmg": true, ".pkg": true, ".deb": true, ".rpm": true,
	".apk": true, ".so": true, ".dylib": true,
}

// isNativeExecutable reports whether data starts like a native program (PE,
// ELF, Mach-O, universal), whatever the file is called.
func isNativeExecutable(data []byte) bool {
	if len(data) >= 2 && data[0] == 'M' && data[1] == 'Z' {
		return true
	}
	if len(data) < 4 {
		return false
	}
	switch string(data[:4]) {
	case "\x7fELF",
		"\xfe\xed\xfa\xce", "\xce\xfa\xed\xfe",
		"\xfe\xed\xfa\xcf", "\xcf\xfa\xed\xfe",
		"\xca\xfe\xba\xbe", "\xbe\xba\xfe\xca":
		return true
	}
	return false
}

func tooLargeReason() string {
	if maxAttachmentBytes >= 1<<20 && maxAttachmentBytes%(1<<20) == 0 {
		return fmt.Sprintf("too large (over %d MB)", maxAttachmentBytes>>20)
	}
	return fmt.Sprintf("too large (over %d bytes)", maxAttachmentBytes)
}

func skipReason(err error) string {
	switch {
	case errors.Is(err, errTooLarge):
		return tooLargeReason()
	case errors.Is(err, errExecutable):
		return skippedExecutable
	}
	return "download failed"
}

// incomingFile is one file of a message: a photo size or a document.
type incomingFile struct {
	fileID string
	name   string
	mime   string
	size   int64
	photo  bool
}

// filesOf lists the files of a message: its best photo and its document.
func filesOf(m *Message) []incomingFile {
	var out []incomingFile
	if len(m.Photo) > 0 {
		best := m.Photo[0]
		for _, p := range m.Photo[1:] {
			if p.FileSize > best.FileSize || p.Width*p.Height > best.Width*best.Height {
				best = p
			}
		}
		out = append(out, incomingFile{fileID: best.FileID, name: "photo.jpg", mime: "image/jpeg", size: best.FileSize, photo: true})
	}
	if d := m.Document; d != nil {
		name := d.FileName
		if name == "" {
			name = "file"
		}
		out = append(out, incomingFile{fileID: d.FileID, name: name, mime: d.MimeType, size: d.FileSize})
	}
	return out
}

// fetchFiles brings in the files of a message: every file is saved by path
// (the model chooses how to read it); an image is ALSO a prompt image. Nothing
// is dropped silently: a file that cannot be brought in is listed with why.
func fetchFiles(ctx context.Context, bot *Bot, files []incomingFile) (images []types.ImageData, atts []attachment) {
	for _, f := range files {
		if executableExtensions[strings.ToLower(filepath.Ext(f.name))] {
			atts = append(atts, attachment{Name: f.name, Skipped: skippedExecutable})
			continue
		}
		if f.size > maxAttachmentBytes {
			atts = append(atts, attachment{Name: f.name, Skipped: tooLargeReason()})
			continue
		}
		data, err := bot.Download(ctx, f.fileID, maxAttachmentBytes)
		if err == nil && isNativeExecutable(data) {
			err = errExecutable
		}
		if err != nil {
			atts = append(atts, attachment{Name: f.name, Skipped: skipReason(err)})
			continue
		}
		mime := imageMIME(f, data)
		path, err := writeTemp(f.name, data)
		if err != nil {
			atts = append(atts, attachment{Name: f.name, Skipped: "could not save"})
			continue
		}
		if mime != "" {
			images = append(images, types.ImageData{MimeType: mime, Base64: base64.StdEncoding.EncodeToString(data)})
			atts = append(atts, attachment{Name: f.name, Path: path, Image: true})
			continue
		}
		atts = append(atts, attachment{Name: f.name, Path: path})
	}
	return images, atts
}

// imageMIME is the image type of a file the model can see ("" when it is not
// an image): photos are JPEG; a document counts by its content.
func imageMIME(f incomingFile, data []byte) string {
	if f.photo {
		return "image/jpeg"
	}
	switch m := http.DetectContentType(data); m {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return m
	}
	return ""
}

// writeTemp saves data at a fresh temp path ending with the file's name.
func writeTemp(name string, data []byte) (string, error) {
	name = filepath.Base(name)
	if name == "" || name == "." || name == "/" {
		name = "file"
	}
	if isNativeExecutable(data) {
		return "", errExecutable
	}
	tmp, err := os.CreateTemp("", "*-"+name)
	if err != nil {
		return "", err
	}
	defer tmp.Close()
	if _, err := tmp.Write(data); err != nil {
		return "", err
	}
	return tmp.Name(), nil
}

// sendUpload sends a local file: a real photo inline, a GIF/MP4 as an
// animation, anything else as a document.
func sendUpload(ctx context.Context, bot *Bot, chatID int64, path string) error {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".png", ".webp":
		if isRealImage(path) {
			return bot.SendFile(ctx, "sendPhoto", "photo", chatID, path)
		}
	case ".gif", ".mp4":
		return bot.SendFile(ctx, "sendAnimation", "animation", chatID, path)
	}
	return bot.SendFile(ctx, "sendDocument", "document", chatID, path)
}

// isRealImage checks the file's content, so a mislabeled file goes as a
// document instead of failing sendPhoto.
func isRealImage(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := f.Read(head)
	switch http.DetectContentType(head[:n]) {
	case "image/jpeg", "image/png", "image/webp":
		return true
	}
	return false
}
