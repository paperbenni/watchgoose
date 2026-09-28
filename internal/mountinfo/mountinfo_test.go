package mountinfo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A plausible slice of /proc/self/mountinfo. The interesting entries are
// /homeXYZ (a mount whose name merely starts like /home) and /home/data (a
// mount below /home, which does not make /home a mountpoint).
const sample = `21 27 0:20 / /sys rw,nosuid,nodev,noexec,relatime shared:7 - sysfs sysfs rw
22 27 0:5 / /proc rw,nosuid,nodev,noexec,relatime shared:13 - proc proc rw
27 1 254:1 / / rw,relatime shared:1 - ext4 /dev/vda1 rw,discard
44 27 259:1 / /homeXYZ rw,relatime shared:8 - ext4 /dev/vdb1 rw
45 27 259:2 / /home/data rw,relatime shared:9 - ext4 /dev/vdc1 rw
46 27 0:26 / /mnt/with\040space rw,relatime shared:10 - tmpfs tmpfs rw
47 27 259:3 / /mnt/trailing/slash/ rw,relatime shared:11 - ext4 /dev/vdd1 rw
short
`

func writeSample(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mountinfo")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write mountinfo: %v", err)
	}
	return path
}

func TestIsMountpointInComparesWholePathComponents(t *testing.T) {
	source := writeSample(t, sample)
	cases := map[string]struct {
		path string
		want bool
	}{
		"the volume itself":                 {"/homeXYZ", true},
		"not the volume":                    {"/home", false},
		"a mount below the volume":          {"/home/data", true},
		"a prefix of that mount":            {"/home/dat", false},
		"a path below the volume":           {"/home/data/inner", false},
		"a trailing slash is the same path": {"/home/data/", true},
		"a doubled slash is the same path":  {"/home//data", true},
		"an escaped space":                  {"/mnt/with space", true},
		"the escape itself":                 {`/mnt/with\040space`, false},
		"a truncated mountinfo line":        {"short", false},
		"an ordinary directory":             {"/etc", false},
		"proc":                              {"/proc", true},
		"a mount with a trailing slash":     {"/mnt/trailing/slash", true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := IsMountpointIn(source, tc.path)
			if err != nil {
				t.Fatalf("IsMountpointIn(%q): %v", tc.path, err)
			}
			if got != tc.want {
				t.Errorf("IsMountpointIn(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

// A mount listed as /home/ and one listed as /home are the same path.
func TestTrailingSlashOnAMountpointIsTheSamePath(t *testing.T) {
	source := writeSample(t, "27 27 259:4 / /home/ rw,relatime shared:1 - ext4 /dev/vdb1 rw\n")
	for _, path := range []string{"/home", "/home/"} {
		got, err := IsMountpointIn(source, path)
		if err != nil {
			t.Fatalf("IsMountpointIn(%q): %v", path, err)
		}
		if !got {
			t.Errorf("IsMountpointIn(%q) = false, want true", path)
		}
	}
}

func TestRootIsAlwaysAMountpoint(t *testing.T) {
	// The root filesystem is always mounted, and saying so must not depend on
	// being able to read the kernel's table: a machine with an unreadable
	// /proc still needs that one answer.
	sources := map[string]string{
		"an ordinary table":        writeSample(t, sample),
		"an empty table":           writeSample(t, ""),
		"nonsense":                 writeSample(t, "not a mountinfo file at all\n"),
		"a file that is not there": filepath.Join(t.TempDir(), "absent"),
	}
	for name, source := range sources {
		t.Run(name, func(t *testing.T) {
			got, err := IsMountpointIn(source, "/")
			if err != nil {
				t.Fatalf("IsMountpointIn(%q, \"/\"): %v", source, err)
			}
			if !got {
				t.Error("IsMountpointIn(\"/\") = false, want true")
			}
		})
	}
}

func TestIsMountpointInReportsAMissingTable(t *testing.T) {
	source := filepath.Join(t.TempDir(), "absent")
	got, err := IsMountpointIn(source, "/home")
	if err == nil {
		t.Fatal("IsMountpointIn with no mountinfo file returned no error")
	}
	if !strings.Contains(err.Error(), source) {
		t.Errorf("error %q does not name the file it could not read", err)
	}
	if got {
		t.Error("IsMountpointIn with no mountinfo file = true; the safe answer is unknown, and the caller treats that as absent")
	}
}

func TestIsMountpointInRejectsAnEmptyPath(t *testing.T) {
	if _, err := IsMountpointIn(writeSample(t, sample), ""); err == nil {
		t.Fatal("IsMountpointIn(\"\") returned no error")
	}
}

func TestIsMountpointAgainstThisMachinesOwnMounts(t *testing.T) {
	if got, err := IsMountpoint("/"); err != nil || !got {
		t.Errorf("IsMountpoint(\"/\") = %v, %v; want true, nil", got, err)
	}
	// A fresh temporary directory is on whatever filesystem holds /tmp, which
	// is not a mountpoint of its own on any sane system.
	tmp := t.TempDir()
	if got, err := IsMountpoint(tmp); err != nil || got {
		t.Errorf("IsMountpoint(%q) = %v, %v; want false, nil", tmp, got, err)
	}
	if got, err := IsMountpoint("/proc"); err != nil {
		t.Errorf("IsMountpoint(\"/proc\"): %v", err)
	} else if !got {
		t.Log("/proc is not a mountpoint here; skipping the /proc assertion")
	}
}

func TestUnescape(t *testing.T) {
	cases := map[string]string{
		"/mnt/plain":             "/mnt/plain",
		`/mnt/with\040space`:     "/mnt/with space",
		`/mnt/with\011tab`:       "/mnt/with\ttab",
		`/mnt/with\012newline`:   "/mnt/with\nnewline",
		`/mnt/with\134backslash`: `/mnt/with\backslash`,
		`/mnt/truncated\04`:      `/mnt/truncated\04`,
		// \999 is not an octal escape, so it stays as three literal digits.
		`/mnt/not\999an\040escape`: `/mnt/not\999an escape`,
		"":                         "",
	}
	for in, want := range cases {
		if got := unescape(in); got != want {
			t.Errorf("unescape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseSkipsLinesItCannotRead(t *testing.T) {
	got, err := parse(strings.NewReader(sample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{"/sys", "/proc", "/", "/homeXYZ", "/home/data", "/mnt/with space", "/mnt/trailing/slash"}
	if len(got) != len(want) {
		t.Fatalf("parse = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("parse[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
