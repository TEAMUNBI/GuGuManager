package files

import (
	"errors"
	"path"
	"path/filepath"
	"strings"
)

var ErrPathEscape = errors.New("path escapes the server root")
var ErrPathLimit = errors.New("path exceeds the portable length limit")

func ResolveWithinRoot(root string, requested string) (string, error) {
	clean, err := NormalizeRelative(requested)
	if err != nil {
		return "", err
	}
	if clean == "" {
		return filepath.Clean(root), nil
	}

	resolved := filepath.Join(root, filepath.FromSlash(clean))
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", ErrPathEscape
	}
	return resolved, nil
}

func NormalizeRelative(requested string) (string, error) {
	if strings.ContainsRune(requested, '\x00') || filepath.IsAbs(requested) || filepath.VolumeName(requested) != "" {
		return "", ErrPathEscape
	}

	portable := strings.ReplaceAll(requested, "\\", "/")
	if len([]rune(portable)) > 1024 {
		return "", ErrPathLimit
	}
	if strings.HasPrefix(portable, "/") || hasWindowsDrivePrefix(portable) {
		return "", ErrPathEscape
	}
	for _, component := range strings.Split(portable, "/") {
		if len([]rune(component)) > 255 {
			return "", ErrPathLimit
		}
		if component == ".." {
			return "", ErrPathEscape
		}
	}
	clean := path.Clean(portable)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", ErrPathEscape
	}
	if clean == "." {
		return "", nil
	}
	return clean, nil
}

func hasWindowsDrivePrefix(value string) bool {
	if len(value) < 2 || value[1] != ':' {
		return false
	}
	first := value[0]
	return first >= 'A' && first <= 'Z' || first >= 'a' && first <= 'z'
}
