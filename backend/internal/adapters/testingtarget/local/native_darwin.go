//go:build darwin && cgo

package local

/*
#cgo LDFLAGS: -framework CoreGraphics -framework CoreFoundation
#include <CoreGraphics/CoreGraphics.h>
#include <stdlib.h>

typedef struct { int count; uint32_t *ids; } target_window_list;

static target_window_list target_windows(int pid) {
    target_window_list result = { -1, NULL };
    CFArrayRef windows = CGWindowListCopyWindowInfo(kCGWindowListOptionAll, kCGNullWindowID);
    if (windows == NULL) return result;
    CFIndex length = CFArrayGetCount(windows);
    result.ids = calloc(length ? length : 1, sizeof(uint32_t));
    if (result.ids == NULL) { CFRelease(windows); return result; }
    int count = 0;
    for (CFIndex i = 0; i < length; i++) {
        CFDictionaryRef window = CFArrayGetValueAtIndex(windows, i);
        CFNumberRef owner = CFDictionaryGetValue(window, kCGWindowOwnerPID);
        CFNumberRef number = CFDictionaryGetValue(window, kCGWindowNumber);
        int owner_pid = 0; uint32_t number_id = 0;
        if (owner && number && CFNumberGetValue(owner, kCFNumberIntType, &owner_pid)
            && owner_pid == pid && CFNumberGetValue(number, kCFNumberSInt32Type, &number_id)) {
            result.ids[count++] = number_id;
        }
    }
    CFRelease(windows);
    result.count = count;
    return result;
}

static uint32_t target_window_at(target_window_list list, int index) {
    return list.ids[index];
}

static void target_window_free(target_window_list list) { free(list.ids); }
*/
import "C"

import (
	"context"
	"errors"
	"strconv"
	"syscall"
	"time"

	processutil "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

func nativeStartTime(pid int) (time.Time, error) {
	return processutil.StartTime(pid)
}

func nativeWindows(ctx context.Context, pid int) ([]string, error) {
	err := ctx.Err()
	if err != nil {
		return nil, err
	}
	list := C.target_windows(C.int(pid))
	if list.count < 0 {
		return nil, errors.New("native window inventory unavailable")
	}
	defer C.target_window_free(list)
	windows := make([]string, 0, int(list.count))
	for i := 0; i < int(list.count); i++ {
		id := C.target_window_at(list, C.int(i))
		windows = append(windows, strconv.FormatUint(uint64(id), 10))
	}
	return windows, nil
}

// Bind without listen reserves a free port without creating another listener.
func unusedPort() (int, error) {
	socket, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		return 0, err
	}
	defer func() { _ = syscall.Close(socket) }()
	if err := syscall.Bind(socket, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		return 0, err
	}
	address, err := syscall.Getsockname(socket)
	if err != nil {
		return 0, err
	}
	inet, ok := address.(*syscall.SockaddrInet4)
	if !ok {
		return 0, errors.New("reserved port is not IPv4")
	}
	return inet.Port, nil
}
