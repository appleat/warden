//go:build !windows

package system

import "os"

func isRoot() bool { return os.Geteuid() == 0 }
