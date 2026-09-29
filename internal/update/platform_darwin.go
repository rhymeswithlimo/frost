package update

import "golang.org/x/sys/unix"

// appleSilicon reports whether this Mac has an arm64 CPU, even when this
// binary runs under Rosetta.
func appleSilicon() bool {
	v, err := unix.SysctlUint32("hw.optional.arm64")
	return err == nil && v == 1
}
