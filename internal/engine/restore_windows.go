package engine

import (
	"fmt"
	"path/filepath"
)

// Windows links have no owner to go by, so in-place restores refuse any link
// or junction above what they write.
func resolveParent(dir string) (string, error) {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	if resolved != dir {
		return "", fmt.Errorf("%s goes through a link", dir)
	}
	return dir, nil
}
