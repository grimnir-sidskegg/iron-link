//go:build windows

package update

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// The staging directory of the only downloadable channel is exercised for
// real here: UpdatesDir resolves it under %ProgramData%, so the variable is
// pointed at a scratch root and the create / re-stamp / squat paths run
// against the live security API.

// readDACL returns path's DACL as its ACE list (the "(...)(...)" tail of
// the SDDL, control flags stripped) and whether the DACL is protected.
func readDACL(t *testing.T, path string) (aces string, protected bool) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("read DACL of %s: %v", path, err)
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatalf("read control of %s: %v", path, err)
	}
	return aceList(sd.String()), control&windows.SE_DACL_PROTECTED != 0
}

// aceList strips the "D:<flags>" prefix so ACE lists compare structurally,
// however the control flags render.
func aceList(sddl string) string {
	if i := strings.Index(sddl, "("); i >= 0 {
		return sddl[i:]
	}
	return ""
}

// wantACEs renders updatesDirSDDL through the same API the read-back uses:
// a rights mask may print differently from the source text.
func wantACEs(t *testing.T) string {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(updatesDirSDDL)
	if err != nil {
		t.Fatalf("parse updatesDirSDDL: %v", err)
	}
	return aceList(sd.String())
}

func assertProtectedDACL(t *testing.T, path string) {
	t.Helper()
	aces, protected := readDACL(t, path)
	if !protected {
		t.Errorf("%s: DACL is not protected", path)
	}
	if want := wantACEs(t); aces != want {
		t.Errorf("%s: DACL ACEs = %s, want %s", path, aces, want)
	}
}

func ownerOf(t *testing.T, path string) *windows.SID {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("read owner of %s: %v", path, err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		t.Fatalf("read owner of %s: %v", path, err)
	}
	return owner
}

// trustedOwner mirrors the ownership rule of checkExistingDir.
func trustedOwner(sid *windows.SID) bool {
	return sid.IsWellKnown(windows.WinLocalSystemSid) || sid.IsWellKnown(windows.WinBuiltinAdministratorsSid)
}

func TestUpdatesDirCreatesProtected(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ProgramData", root)

	dir, err := UpdatesDir("ignored")
	if err != nil {
		t.Fatalf("UpdatesDir: %v", err)
	}
	parent := filepath.Join(root, "iron-link")
	if want := filepath.Join(parent, "updates"); dir != want {
		t.Fatalf("UpdatesDir = %q, want %q", dir, want)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("stat %s: %v", dir, err)
	}
	assertProtectedDACL(t, parent)
	assertProtectedDACL(t, dir)

	// A second resolve over the pre-existing pair, after the leaf's DACL was
	// loosened the way an installer-made directory would be: accepted and
	// re-stamped when this process's objects are owned by SYSTEM or
	// Administrators (an elevated run — the service case), refused
	// otherwise. Both outcomes are the contract; which one applies follows
	// the token the test runs under.
	loose, err := windows.SecurityDescriptorFromString("D:(A;OICI;FA;;;BU)")
	if err != nil {
		t.Fatal(err)
	}
	looseDACL, _, err := loose.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, looseDACL, nil); err != nil {
		t.Fatalf("loosen DACL: %v", err)
	}
	if _, protected := readDACL(t, dir); protected {
		t.Fatal("precondition: the loosened DACL must be unprotected")
	}

	again, err := UpdatesDir("")
	if trustedOwner(ownerOf(t, dir)) {
		if err != nil || again != dir {
			t.Fatalf("re-resolve over an owned pair = %q, %v; want %q", again, err, dir)
		}
		assertProtectedDACL(t, dir)
	} else if err == nil || !strings.Contains(err.Error(), "owned by") {
		t.Fatalf("re-resolve over a user-owned pair = %q, %v; want the ownership refusal", again, err)
	}
}

// TestUpdatesDirRefusesSquat: %ProgramData% lets ordinary users create
// subdirectories, so a pre-existing "iron-link" must be a real directory
// owned by SYSTEM or Administrators before the daemon stamps and uses it.
func TestUpdatesDirRefusesSquat(t *testing.T) {
	cases := []struct {
		name    string
		squat   func(t *testing.T, parent string)
		wantErr string
	}{
		{
			name: "junction in place of the directory",
			squat: func(t *testing.T, parent string) {
				target := t.TempDir()
				out, err := exec.Command("cmd", "/c", "mklink", "/J", parent, target).CombinedOutput()
				if err != nil {
					t.Fatalf("mklink /J: %v: %s", err, out)
				}
				t.Cleanup(func() { os.Remove(parent) })
			},
			wantErr: "reparse point",
		},
		{
			name: "file in place of the directory",
			squat: func(t *testing.T, parent string) {
				if err := os.WriteFile(parent, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "not a directory",
		},
		{
			name: "directory owned by a user account",
			squat: func(t *testing.T, parent string) {
				if err := os.Mkdir(parent, 0o700); err != nil {
					t.Fatal(err)
				}
				// Hand the directory to this process's user account (a
				// squatter's directory is owned by a user, never by SYSTEM
				// or Administrators); taking one's own user SID as owner
				// needs no privilege.
				tok, err := windows.OpenCurrentProcessToken()
				if err != nil {
					t.Fatal(err)
				}
				defer tok.Close()
				user, err := tok.GetTokenUser()
				if err != nil {
					t.Fatal(err)
				}
				if trustedOwner(user.User.Sid) {
					t.Skip("the process runs as SYSTEM/Administrators itself")
				}
				if err := windows.SetNamedSecurityInfo(parent, windows.SE_FILE_OBJECT,
					windows.OWNER_SECURITY_INFORMATION, user.User.Sid, nil, nil, nil); err != nil {
					t.Fatalf("set owner: %v", err)
				}
			},
			wantErr: "owned by",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("ProgramData", root)
			tc.squat(t, filepath.Join(root, "iron-link"))
			dir, err := UpdatesDir("")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("UpdatesDir = %q, %v; want an error mentioning %q", dir, err, tc.wantErr)
			}
		})
	}
}

func TestUpdatesDirNeedsProgramData(t *testing.T) {
	t.Setenv("ProgramData", "")
	if dir, err := UpdatesDir(""); err == nil {
		t.Fatalf("UpdatesDir = %q, want an error without ProgramData", dir)
	}
}
