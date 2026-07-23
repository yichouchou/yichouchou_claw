//go:build windows

package localcommand

func lookupDefaultPATH() (string, bool) {
	// Windows 上 PATH 由系统保证一定有,这里只是兜底。
	return "%SystemRoot%\\System32;%SystemRoot%;%SystemRoot%\\System32\\WindowsPowerShell\\v1.0\\", true
}
