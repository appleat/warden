//go:build windows

package system

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// processThreadCounts 用一次系统线程快照统计每个进程的线程数。
// gopsutil 的 Process.NumThreads 在 Windows 上每次都要遍历全系统线程表（约 13ms/进程），
// 几百个进程就是好几秒，所以这里改成一次快照批量统计。
// 返回 nil 表示快照失败，调用方回落到逐进程读取。
func processThreadCounts() map[int32]int32 {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)

	var e windows.ThreadEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	counts := make(map[int32]int32, 256)
	for err := windows.Thread32First(snap, &e); err == nil; err = windows.Thread32Next(snap, &e) {
		counts[int32(e.OwnerProcessID)]++
	}
	return counts
}
