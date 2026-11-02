package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

// serviceTemplate is the systemd user service unit file.
// It uses %b (bin directory) and %h (home directory) expanders that
// systemd resolves at runtime.
const serviceTemplate = `[Unit]
Description=AutoGit - Automatic Git backup service
Documentation=https://github.com/Joshua504/auto-git
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart={{.BinaryPath}} --config {{.ConfigPath}}
Restart=on-failure
RestartSec=60
StartLimitIntervalSec=300
StartLimitBurst=3
Environment=GIT_TERMINAL_PROMPT=0

# Battery-friendly: reduce CPU impact when idle
Nice=10

[Install]
WantedBy=default.target
`

type serviceTemplateData struct {
	BinaryPath string
	ConfigPath string
}

// cmdInstall installs AutoGit as a systemd user service so it starts
// automatically at login and restarts after failures.
//
// Battery impact is minimal: AutoGit only does work while a configured
// editor is open. When no editor is running it sleeps between 5-second
// pgrep checks. The service uses RestartSec=60 and StartLimitBurst=3
// to avoid aggressive restart loops.
func cmdInstall(configFlag string) error {
	// Find the binary path.
	binaryPath, err := exec.LookPath("autogit")
	if err != nil {
		// Fall back to the running binary's path.
		binaryPath, err = os.Executable()
		if err != nil {
			// Last resort: assume it's in the current directory.
			binaryPath = "autogit"
		}
	}

	// Resolve the config path the same way main() does.
	configPath, err := locateConfig(configFlag)
	if err != nil {
		return fmt.Errorf("configuration file not found: %w\nRun without 'install' to configure AutoGit first.", err)
	}

	// Ensure the path is absolute for the service file.
	absConfig, err := filepath.Abs(configPath)
	if err != nil {
		absConfig = configPath
	}

	// Render the service unit file.
	tmpl, err := template.New("service").Parse(serviceTemplate)
	if err != nil {
		return fmt.Errorf("template parse error: %w", err)
	}

	var unitContent strings.Builder
	if err := tmpl.Execute(&unitContent, serviceTemplateData{
		BinaryPath: binaryPath,
		ConfigPath: absConfig,
	}); err != nil {
		return fmt.Errorf("template execution error: %w", err)
	}

	// Create the systemd user directory.
	userRuntimeDir, err := systemdUserDirectory()
	if err != nil {
		return err
	}

	serviceFile := filepath.Join(userRuntimeDir, "autogit.service")

	// Write the service file.
	if err := os.MkdirAll(userRuntimeDir, 0o755); err != nil {
		return fmt.Errorf("cannot create %s: %w", userRuntimeDir, err)
	}

	if err := os.WriteFile(serviceFile, []byte(unitContent.String()), 0o644); err != nil {
		return fmt.Errorf("cannot write %s: %w", serviceFile, err)
	}

	fmt.Printf("Installed systemd user service: %s\n", serviceFile)
	fmt.Println()
	fmt.Println("To start and enable the service:")
	fmt.Println("  systemctl --user daemon-reload")
	fmt.Println("  systemctl --user enable --now autogit.service")
	fmt.Println()
	fmt.Println("To check the status:")
	fmt.Println("  systemctl --user status autogit.service")
	fmt.Println()
	fmt.Println("To view logs:")
	fmt.Println("  journalctl --user -u autogit.service -f")
	fmt.Println()
	fmt.Println("Battery note: AutoGit only performs Git operations while a")
	fmt.Println("configured editor is open. When idle it uses minimal CPU.")

	return nil
}

// cmdUninstall removes the AutoGit systemd user service.
func cmdUninstall() error {
	userRuntimeDir, err := systemdUserDirectory()
	if err != nil {
		return err
	}

	serviceFile := filepath.Join(userRuntimeDir, "autogit.service")

	// Stop and disable the service if it's enabled.
	cmd := exec.Command("systemctl", "--user", "disable", "--now", "autogit.service")
	if err := cmd.Run(); err != nil {
		// The service may not be enabled; that's fine.
		fmt.Fprintf(os.Stderr, "Note: service may not have been enabled: %v\n", err)
	}

	// Remove the service file.
	if err := os.Remove(serviceFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("cannot remove %s: %w", serviceFile, err)
	}

	// Reload the systemd daemon.
	cmd = exec.Command("systemctl", "--user", "daemon-reload")
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Note: failed to reload daemon: %v\n", err)
	}

	fmt.Println("AutoGit systemd user service removed.")
	fmt.Printf("Removed: %s\n", serviceFile)

	return nil
}

// locateConfig replicates the config location logic from main() so the
// install command uses the same file.
func locateConfig(configFlag string) (string, error) {
	if configFlag != "" {
		if _, err := os.Stat(configFlag); err == nil {
			return configFlag, nil
		}
		return "", fmt.Errorf("%s does not exist", configFlag)
	}

	// Try ./config.yaml.
	if _, err := os.Stat("config.yaml"); err == nil {
		return "config.yaml", nil
	}

	// Try ~/.config/autogit/config.yaml.
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	userConfig := filepath.Join(home, ".config", "autogit", "config.yaml")
	if _, err := os.Stat(userConfig); err == nil {
		return userConfig, nil
	}

	return "", fmt.Errorf("no configuration file found in ./config.yaml or %s", userConfig)
}

// systemdUserDirectory returns the user systemd service directory,
// creating XDG_CONFIG_HOME/systemd if needed.
func systemdUserDirectory() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "systemd"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}

	return filepath.Join(home, ".config", "systemd"), nil
}
