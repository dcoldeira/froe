package provider

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// maxImageBytes bounds what gets read into memory and shipped to a model. A
// screenshot is a few hundred KB; anything past this is almost certainly the
// wrong file, and base64 already inflates it by a third on the wire.
const maxImageBytes = 20 * 1024 * 1024

// imageMediaTypes maps a file extension to its MIME type. Only formats every
// vision-capable backend in the registry (LM Studio, Anthropic) actually
// accepts - an unlisted extension is refused rather than guessed.
var imageMediaTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
}

// LoadImageFile reads a local image and returns it ready to attach to a
// Message. The caller (internal/tools or a CLI command) is responsible for
// deciding the path is one the user actually meant to share - this function
// only validates that it IS a readable, supported image.
func LoadImageFile(path string) (ImageContent, error) {
	mediaType, ok := imageMediaTypes[strings.ToLower(filepath.Ext(path))]
	if !ok {
		return ImageContent{}, fmt.Errorf("unsupported image type %q (supported: png, jpg, jpeg, gif, webp)", filepath.Ext(path))
	}

	info, err := os.Stat(path)
	if err != nil {
		return ImageContent{}, err
	}
	if info.IsDir() {
		return ImageContent{}, fmt.Errorf("%q is a directory, not an image", path)
	}
	if info.Size() > maxImageBytes {
		return ImageContent{}, fmt.Errorf("%q is %d MB, over the %d MB limit", path, info.Size()/(1<<20), maxImageBytes/(1<<20))
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return ImageContent{}, err
	}

	return ImageContent{MediaType: mediaType, Data: base64.StdEncoding.EncodeToString(data)}, nil
}
