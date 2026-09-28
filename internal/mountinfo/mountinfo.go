// Package mountinfo answers one question about the filesystem: is this path
// a mountpoint?
//
// The switch needs the answer for exactly one decision. Repair of the ordinary
// accounts is only meaningful when the volume that holds their home
// directories is actually mounted; when it is not, /home is an empty directory
// on the root disk and anything written there is shadowed the moment the volume
// mounts on a later boot
// (see docs/adr/0004-repair-skips-accounts-when-volume-absent.md).
package mountinfo

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// procPath is the kernel's own table of what is mounted where, for this
// process. Read from /proc/self rather than a system-wide path so that the
// answer reflects this process's mount namespace.
const procPath = "/proc/self/mountinfo"

// mountPointField is the zero-based index of the mount point in a mountinfo
// line. The fields before it are the mount ID, the parent ID, the
// major:minor device, the root of the filesystem within the device, and then
// the mount point. Escaping is why the fields can be split on spaces at all.
const mountPointField = 4

// IsMountpoint reports whether path is a mountpoint, according to
// /proc/self/mountinfo.
//
// Comparison is by whole path components, so a mount of /homeXYZ does not
// make /home a mountpoint and /home/data is not the mountpoint /home. The
// root filesystem is always a mountpoint and is answered without consulting
// the kernel at all.
func IsMountpoint(path string) (bool, error) {
	return IsMountpointIn(procPath, path)
}

// IsMountpointIn is IsMountpoint against a mountinfo file of the caller's
// choosing, which is what makes the parsing testable.
func IsMountpointIn(source, path string) (bool, error) {
	if path == "" {
		return false, fmt.Errorf("mountinfo: empty path")
	}
	want, err := filepath.Abs(path)
	if err != nil {
		return false, fmt.Errorf("mountinfo: %w", err)
	}
	want = filepath.Clean(want)

	// The root filesystem is always mounted. Say so without opening anything,
	// so that a machine with an unreadable /proc still gets the one answer it
	// needs.
	if want == "/" {
		return true, nil
	}

	mounts, err := mountpoints(source)
	if err != nil {
		return false, err
	}
	for _, m := range mounts {
		if m == want {
			return true, nil
		}
	}
	return false, nil
}

// mountpoints returns every mount point listed in a mountinfo file, with
// kernel octal escapes decoded and the paths cleaned.
func mountpoints(source string) ([]string, error) {
	f, err := os.Open(source)
	if err != nil {
		return nil, fmt.Errorf("mountinfo: open %s: %w", source, err)
	}
	defer f.Close()
	return parse(bufio.NewReader(f))
}

func parse(r io.Reader) ([]string, error) {
	var mounts []string
	sc := bufio.NewScanner(r)
	// A mountinfo line can be long, and the default 64KiB cap is a limit the
	// kernel does not promise to respect; the buffer is not the interesting
	// part.
	sc.Buffer(make([]byte, 0, 8*1024), 1<<20)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) <= mountPointField {
			// A truncated line cannot be interpreted; skipping it loses a
			// mountpoint but cannot invent one.
			continue
		}
		mounts = append(mounts, filepath.Clean(unescape(fields[mountPointField])))
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("mountinfo: read: %w", err)
	}
	return mounts, nil
}

// unescape decodes the octal escapes the kernel uses for space, tab, newline
// and backslash inside mountinfo fields. Without this a mountpoint containing
// a space would never match the path we are asked about.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == '\\' && i+3 < len(s) && isOctal(s[i+1]) && isOctal(s[i+2]) && isOctal(s[i+3]) {
			b.WriteByte(octalValue(s[i+1], s[i+2], s[i+3]))
			i += 4
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func isOctal(c byte) bool { return c >= '0' && c <= '7' }

func octalValue(hi, mid, lo byte) byte {
	return (hi-'0')<<6 | (mid-'0')<<3 | (lo - '0')
}
