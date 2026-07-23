//go:build linux || darwin

package localcommand

import "os"

func lookupDefaultPATH() (string, bool) {
	if v := os.Getenv("PATH"); v != "" {
		return v, false // 已经有 PATH,不应被覆盖
	}
	return "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", true
}
