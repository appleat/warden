//go:build !windows

package system

// processThreadCounts 非 Windows 平台返回 nil：Linux 上读 /proc/<pid>/status 取线程数
// 开销很小，直接逐进程调用 gopsutil 即可，无需批量优化。
func processThreadCounts() map[int32]int32 { return nil }
