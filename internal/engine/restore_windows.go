package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Windows links have no owner to go by, so in-place restores refuse any link
// or junction above what they write. Each part of the path is checked on its
// own: comparing with filepath.EvalSymlinks would also flag short names like
// RUNNER~1, which it expands.
func resolveParent(dir string) (string, error) {
	dir = filepath.Clean(dir)
	vol := filepath.VolumeName(dir)
	cur := vol + string(filepath.Separator)
	for _, part := range strings.Split(strings.Trim(dir[len(vol):], `\`), `\`) {
		if part == "" {
			continue
		}
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err != nil {
			return "", err
		}
		// Junctions and mount points are irregular, not symlinks, since Go 1.23.
		if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return "", fmt.Errorf("%s goes through a link", dir)
		}
	}
	return dir, nil
}
