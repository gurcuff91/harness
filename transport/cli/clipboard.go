package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// checkClipboardImage checks the OS clipboard for an image.
// If found, saves it to a temp file and returns the path.
// If no image, returns "".
// Works on macOS, Linux (X11/Wayland), and Windows.
func checkClipboardImage() string {
	tmpPath := filepath.Join(os.TempDir(), fmt.Sprintf("harness-clip-%d.png", time.Now().UnixNano()))

	var ok bool
	switch runtime.GOOS {
	case "darwin":
		ok = clipboardDarwin(tmpPath)
	case "linux":
		ok = clipboardLinux(tmpPath)
	case "windows":
		ok = clipboardWindows(tmpPath)
	default:
		return ""
	}

	if !ok {
		os.Remove(tmpPath)
		return ""
	}

	// Verify file exists and has content
	info, err := os.Stat(tmpPath)
	if err != nil || info.Size() == 0 {
		os.Remove(tmpPath)
		return ""
	}

	return tmpPath
}

// ── macOS ──────────────────────────────────────────────────

func clipboardDarwin(dst string) bool {
	// Swift is faster than osascript (~50ms vs ~200ms)
	// and avoids AppleScript overhead.
	swift := fmt.Sprintf(`
import AppKit
let pb = NSPasteboard.general
guard let img = pb.data(forType: .png) ?? pb.data(forType: .tiff) else { exit(1) }
let url = URL(fileURLWithPath: "%s")
// Convert TIFF to PNG if needed
if let bitmapRep = NSBitmapImageRep(data: img),
   let pngData = bitmapRep.representation(using: .png, properties: [:]) {
    try! pngData.write(to: url)
} else {
    try! img.write(to: url)
}
`, dst)

	err := exec.Command("swift", "-e", swift).Run()
	if err != nil {
		// Fallback to osascript if swift fails
		return clipboardDarwinFallback(dst)
	}
	return true
}

func clipboardDarwinFallback(dst string) bool {
	script := fmt.Sprintf(`
set tempFile to POSIX file "%s"
try
	set imgData to the clipboard as «class PNGf»
	set fileRef to open for access tempFile with write permission
	write imgData to fileRef
	close access fileRef
	return "ok"
on error
	try
		close access tempFile
	end try
	return "no"
end try
`, dst)

	err := exec.Command("osascript", "-e", script).Run()
	return err == nil
}

// ── Linux ──────────────────────────────────────────────────

func clipboardLinux(dst string) bool {
	// Try xclip first (X11)
	cmd := exec.Command("xclip", "-selection", "clipboard", "-t", "image/png", "-o")
	out, err := cmd.Output()
	if err == nil && len(out) > 0 {
		return os.WriteFile(dst, out, 0644) == nil
	}

	// Try xsel
	cmd = exec.Command("xsel", "--clipboard", "--output")
	// xsel doesn't support image types directly, skip

	// Try wl-paste (Wayland)
	cmd = exec.Command("wl-paste", "--type", "image/png")
	out, err = cmd.Output()
	if err == nil && len(out) > 0 {
		return os.WriteFile(dst, out, 0644) == nil
	}

	return false
}

// ── Windows ────────────────────────────────────────────────

func clipboardWindows(dst string) bool {
	// PowerShell: Get-Clipboard as image and save
	ps := fmt.Sprintf(`
$img = Get-Clipboard -Format Image
if ($img -ne $null) {
    $img.Save('%s', [System.Drawing.Imaging.ImageFormat]::Png)
    exit 0
} else {
    exit 1
}
`, dst)

	err := exec.Command("powershell", "-NoProfile", "-Command", ps).Run()
	return err == nil
}
