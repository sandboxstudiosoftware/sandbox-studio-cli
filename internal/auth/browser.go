package auth

import (
	"os/exec"
	"runtime"
)

// browserCommand returns the platform command that opens url in the default
// browser. It does not start the command; the caller does.
func browserCommand(url string) *exec.Cmd {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url)
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return exec.Command("xdg-open", url)
	}
}
