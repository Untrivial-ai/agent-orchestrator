//go:build windows

package workerlauncher

import (
	"strings"

	"golang.org/x/sys/windows"
)

func platformFinalPath(path string) (string, error) {
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	handle, err := windows.CreateFile(path16, windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	buf := make([]uint16, 32768)
	n, err := windows.GetFinalPathNameByHandle(handle, &buf[0], uint32(len(buf)), 0)
	if err != nil {
		return "", err
	}
	final := windows.UTF16ToString(buf[:n])
	final = strings.TrimPrefix(final, `\\?\`)
	if strings.HasPrefix(final, `UNC\`) {
		final = `\\` + strings.TrimPrefix(final, `UNC\`)
	}
	return final, nil
}
