package editor

import "os/exec"

func IsRunning(editors []string) bool {
	for _, name := range editors {
		if name == "" {
			continue
		}

		if err := exec.Command("pgrep", "-x", name).Run(); err == nil {
			return true
		}
	}
	return false
}
