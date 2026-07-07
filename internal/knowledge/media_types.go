package knowledge

import "strings"

// MediaTypeExtensions maps media type labels to file extensions.
// "text" covers all plain-text types via IndexableExtensions.
var MediaTypeExtensions = map[string][]string{
	"image": {".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".svg"},
	"video": {".mp4", ".mov", ".webm", ".avi"},
	"audio": {".mp3", ".wav", ".ogg", ".m4a"},
	"pdf":   {".pdf"},
}

// IsMediaFile checks whether ext is in any of the given media types.
// Returns the media type string or empty.
func IsMediaFile(ext string, mediaTypes []string) string {
	ext = strings.ToLower(ext)
	for _, mt := range mediaTypes {
		exts, ok := MediaTypeExtensions[mt]
		if !ok {
			continue
		}
		for _, e := range exts {
			if ext == e {
				return mt
			}
		}
	}
	return ""
}

// MediaTypeLabel returns a human-readable label for a media type.
func MediaTypeLabel(mt string) string {
	switch mt {
	case "image":
		return "image file"
	case "video":
		return "video file"
	case "audio":
		return "audio file"
	case "pdf":
		return "PDF document"
	default:
		return "file"
	}
}
