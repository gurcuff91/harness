package store

import "testing"

// TestCwdSlugStripsWindowsIllegalChars is the regression test for a real
// bug reported live on Windows: a cwd like "C:\Users\gustavo\Downloads"
// used to slug to "C:-Users-gustavo-Downloads" — the drive-letter colon
// survived the separator replacement and landed INSIDE the directory name,
// which NTFS rejects outright ("The directory name is invalid.") on
// mkdir. The colon (and the other NTFS-illegal characters) must never
// appear in the slug, on any OS the store happens to run on.
func TestCwdSlugStripsWindowsIllegalChars(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "windows drive letter backslash path",
			in:   `C:\Users\gustavo\Downloads`,
			want: "C-Users-gustavo-Downloads",
		},
		{
			name: "windows drive letter, different drive",
			in:   `D:\projects\my app`,
			want: "D-projects-my_app",
		},
		{
			name: "unix path unaffected",
			in:   "/Users/gustavo/Workspace/harness",
			want: "Users-gustavo-Workspace-harness",
		},
		{
			name: "other NTFS-illegal characters stripped",
			in:   `C:\weird<name>|dir?*"quoted"`,
			want: "C-weirdnamedirquoted",
		},
		{
			name: "empty path falls back to root",
			in:   "",
			want: "root",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := cwdSlug(c.in); got != c.want {
				t.Errorf("cwdSlug(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestCwdSlugNeverContainsIllegalChars is a broader property check: no
// matter what a slug is built from, the illegal-character set must be
// entirely absent from the result.
func TestCwdSlugNeverContainsIllegalChars(t *testing.T) {
	inputs := []string{
		`C:\Users\test`,
		`\\server\share\path`,
		`relative/unix/path`,
		`weird:mixed\path/chars<>"|?*`,
	}
	for _, in := range inputs {
		slug := cwdSlug(in)
		for _, illegal := range windowsIllegalDirChars {
			for _, r := range slug {
				if r == illegal {
					t.Errorf("cwdSlug(%q) = %q still contains illegal char %q", in, slug, string(illegal))
				}
			}
		}
	}
}
