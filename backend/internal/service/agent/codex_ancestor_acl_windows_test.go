package agent

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// Exercise real NTFS descriptors, not only synthetic ACEs. The ordinary Write
// grant used by Windows sandbox tooling permits sibling creation, not deletion.
func TestWindowsCredentialVaultUnderWritableAncestor(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rights   string
		empty    bool
		wantSafe bool
	}{
		{"sibling creation", "0x1201bf", false, true},
		{"empty read only", "0x1200a9", true, true},
		{"removable only child", "0x1201bf", false, false},
		{"empty writable junction target", "0x1201bf", true, false},
		{"delete child", "0x1201ff", false, false},
		{"change ACL", "0x1601bf", false, false},
		{"take ownership", "0x1a01bf", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if !tc.empty {
				if err := os.WriteFile(filepath.Join(root, "existing-child"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			token, err := windows.OpenCurrentProcessToken()
			if err != nil {
				t.Fatal(err)
			}
			defer token.Close()
			user, err := token.GetTokenUser()
			if err != nil {
				t.Fatal(err)
			}
			sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;;" + tc.rights + ";;;WD)")
			if err != nil {
				t.Fatal(err)
			}
			dacl, _, err := sd.DACL()
			if err != nil {
				t.Fatal(err)
			}
			if err := windows.SetNamedSecurityInfo(root, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
				t.Fatal(err)
			}
			if tc.name == "removable only child" {
				childSD, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;SD;;;WD)")
				if err != nil {
					t.Fatal(err)
				}
				childACL, _, err := childSD.DACL()
				if err != nil {
					t.Fatal(err)
				}
				if err := windows.SetNamedSecurityInfo(filepath.Join(root, "existing-child"), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, childACL, nil); err != nil {
					t.Fatal(err)
				}
			}
			vault := filepath.Join(root, "vault")
			err = ensurePrivateDirectory(vault)
			if (err == nil) != tc.wantSafe {
				t.Fatalf("ensurePrivateDirectory = %v, want safe=%v", err, tc.wantSafe)
			}
			if tc.wantSafe {
				if err := validateCodexDirectory(vault, true); err != nil {
					t.Fatal(err)
				}
				if err := writePrivateFileAtomic(filepath.Join(vault, "test.json"), []byte("{}")); err != nil {
					t.Fatal(err)
				}
				if _, err := readOpaqueCredential(filepath.Join(vault, "test.json")); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
