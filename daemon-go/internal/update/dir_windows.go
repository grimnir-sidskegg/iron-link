//go:build windows

package update

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// updatesDirSDDL is the protected DACL for the download directory: SYSTEM
// and Administrators full control, Users read+execute (0x1200a9 =
// FILE_GENERIC_READ | FILE_GENERIC_EXECUTE), inheritance blocked (P). The
// unprivileged client must be able to read and launch a daemon-downloaded
// installer but never replace it (closes the TOCTOU between the daemon's
// download and the client's launch). No owner clause: the creator (SYSTEM
// for the service, Administrators for an elevated dev run) becomes the
// owner, and both pass the pre-existing-directory check below.
const updatesDirSDDL = "D:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;0x1200a9;;;BU)"

// UpdatesDir resolves (and creates on first use) the artifact download
// directory. On Windows that is %ProgramData%\iron-link\updates under the
// protected DACL above — NOT the config root, which lives in a
// user-writable profile; the argument exists for signature parity with
// the non-Windows implementation and is ignored.
func UpdatesDir(_ string) (string, error) {
	programData := os.Getenv("ProgramData")
	if programData == "" {
		return "", errors.New("update: ProgramData environment variable is not set")
	}
	parent := filepath.Join(programData, "iron-link")
	dir := filepath.Join(parent, "updates")
	if err := createProtectedDir(parent); err != nil {
		return "", err
	}
	if err := createProtectedDir(dir); err != nil {
		return "", err
	}
	return dir, nil
}

// createProtectedDir creates path with the protected DACL. A pre-existing
// path is accepted only when it is a real directory (not a reparse point)
// owned by SYSTEM or Administrators — anything else could have been
// squatted by an unprivileged user before the first elevated run, since
// %ProgramData% lets ordinary users create subdirectories. An accepted
// pre-existing directory is then re-stamped with the same DACL: ownership
// proves who created it, not what ACL it carries — a directory made by an
// installer or an older build inherits the user-writable %ProgramData%
// defaults (Users may create files), which would reopen the download/launch
// TOCTOU the protected DACL exists to close.
func createProtectedDir(path string) error {
	sd, err := windows.SecurityDescriptorFromString(updatesDirSDDL)
	if err != nil {
		return fmt.Errorf("update: parse updates dir SDDL: %w", err)
	}
	sa := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("update: %s: %w", path, err)
	}
	err = windows.CreateDirectory(name, sa)
	if err == nil {
		return nil
	}
	if !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return fmt.Errorf("update: create %s: %w", path, err)
	}
	// Open ONE handle and run every check plus the DACL stamp through it.
	// Name-based check-then-set would race: the directory could be swapped
	// for a junction between the check and the stamp, and a path-based
	// SetNamedSecurityInfo follows the junction — SYSTEM would then rewrite
	// the DACL of an attacker-chosen tree. FILE_FLAG_OPEN_REPARSE_POINT
	// pins the object itself, never a reparse target.
	h, err := windows.CreateFile(name,
		windows.READ_CONTROL|windows.WRITE_DAC,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return fmt.Errorf("update: open %s: %w", path, err)
	}
	defer windows.CloseHandle(h)
	if err := checkExistingDir(h, path); err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("update: read updates dir DACL: %w", err)
	}
	err = windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil)
	if err != nil {
		return fmt.Errorf("update: set protected DACL on %s: %w", path, err)
	}
	return nil
}

func checkExistingDir(h windows.Handle, path string) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return fmt.Errorf("update: stat %s: %w", path, err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return fmt.Errorf("update: %s exists and is not a directory", path)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("update: %s is a reparse point; refusing to use it", path)
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("update: read owner of %s: %w", path, err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("update: read owner of %s: %w", path, err)
	}
	if !owner.IsWellKnown(windows.WinLocalSystemSid) && !owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
		return fmt.Errorf("update: %s is owned by %s, not SYSTEM or Administrators; refusing to use it", path, owner)
	}
	return nil
}
