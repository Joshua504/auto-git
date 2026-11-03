package power

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PowerType describes the kind of power source.
type PowerType int

const (
	PowerTypeUnknown PowerType = iota
	PowerTypeCharging
	PowerTypeDischarging
	PowerTypeFull
	PowerTypeAC // plugged in, no battery present
)

func (p PowerType) String() string {
	switch p {
	case PowerTypeCharging:
		return "charging"
	case PowerTypeDischarging:
		return "discharging"
	case PowerTypeFull:
		return "full"
	case PowerTypeAC:
		return "ac"
	default:
		return "unknown"
	}
}

// State holds the current power state of the system.
type State struct {
	PowerType PowerType
	Battery   int // percentage, 0-100; -1 if no battery
}

// OnBattery reports whether the system is running on battery power (discharging).
func (s State) OnBattery() bool {
	return s.PowerType == PowerTypeDischarging
}

// Provider determines the current power state of the system.
type Provider interface {
	// GetState returns the current power state.
	GetState() (State, error)
}

// LinuxProvider reads power state from sysfs (/sys/class/power_supply/).
type LinuxProvider struct {
	mu          sync.Mutex
	cache       State
	cacheTime   time.Time
	cacheTTL    time.Duration
	supplyPath  string
	hasBattery  bool
}

// NewLinuxProvider creates a provider that reads from Linux sysfs.
// If supplyPath is empty, it auto-detects the battery and AC adapter.
func NewLinuxProvider(supplyPath string) (*LinuxProvider, error) {
	p := &LinuxProvider{
		cacheTTL:   5 * time.Second,
		supplyPath: supplyPath,
	}

	if p.supplyPath == "" {
		detected, err := detectSupplyPath()
		if err != nil {
			// Fall back to empty; GetState will try again each time.
			p.supplyPath = ""
		} else {
			p.supplyPath = detected
		}
	}

	// Check if a battery exists.
	p.hasBattery = hasBatteryDevice()

	return p, nil
}

// GetState returns the current power state. Results are cached for cacheTTL.
func (p *LinuxProvider) GetState() (State, error) {
	p.mu.Lock()
	if time.Since(p.cacheTime) < p.cacheTTL && p.cacheTime.Unix() != 0 {
		defer p.mu.Unlock()
		return p.cache, nil
	}
	p.mu.Unlock()

	state, err := p.readState()
	if err != nil {
		return State{}, err
	}

	p.mu.Lock()
	p.cache = state
	p.cacheTime = time.Now()
	p.mu.Unlock()

	return state, nil
}

func (p *LinuxProvider) readState() (State, error) {
	if !p.hasBattery {
		return State{
			PowerType: PowerTypeAC,
			Battery:   -1,
		}, nil
	}

	battery, err := findBattery()
	if err != nil {
		return State{}, err
	}

	ac, err := findAC()
	if err != nil {
		return State{}, err
	}

	capacity, err := readBatteryCapacity(battery)
	if err != nil {
		return State{}, err
	}

	status, err := readBatteryStatus(battery)
	if err != nil {
		return State{}, err
	}

	acOnline, err := readACOnline(ac)
	if err != nil {
		// If we can't read AC, rely on battery status alone.
		acOnline = false
	}

	var powerType PowerType
	switch strings.TrimSpace(status) {
	case "Charging":
		powerType = PowerTypeCharging
	case "Discharging":
		powerType = PowerTypeDischarging
	case "Full":
		powerType = PowerTypeFull
	case "Not charging":
		// Battery present but not charging and not discharging — treat as plugged in.
		powerType = ifACOnline(acOnline, PowerTypeFull)
	default:
		powerType = ifACOnline(acOnline, PowerTypeUnknown)
	}

	return State{
		PowerType: powerType,
		Battery:   capacity,
	}, nil
}

func ifACOnline(acOnline bool, fallback PowerType) PowerType {
	if acOnline {
		return PowerTypeAC
	}
	return fallback
}

func detectSupplyPath() (string, error) {
	base := "/sys/class/power_supply"
	entries, err := os.ReadDir(base)
	if err != nil {
		return "", err
	}

	for _, entry := range entries {
		path := filepath.Join(base, entry.Name())
		typ, err := os.ReadFile(filepath.Join(path, "type"))
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(typ)) == "Battery" {
			return path, nil
		}
	}

	return "", fmt.Errorf("no battery found in %s", base)
}

func hasBatteryDevice() bool {
	base := "/sys/class/power_supply"
	entries, err := os.ReadDir(base)
	if err != nil {
		return false
	}

	for _, entry := range entries {
		typ, err := os.ReadFile(filepath.Join(base, entry.Name(), "type"))
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(typ)) == "Battery" {
			return true
		}
	}

	return false
}

func findBattery() (string, error) {
	base := "/sys/class/power_supply"
	entries, err := os.ReadDir(base)
	if err != nil {
		return "", err
	}

	for _, entry := range entries {
		path := filepath.Join(base, entry.Name())
		typ, err := os.ReadFile(filepath.Join(path, "type"))
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(typ)) == "Battery" {
			return path, nil
		}
	}

	return "", fmt.Errorf("no battery found in %s", base)
}

func findAC() (string, error) {
	base := "/sys/class/power_supply"
	entries, err := os.ReadDir(base)
	if err != nil {
		return "", err
	}

	for _, entry := range entries {
		path := filepath.Join(base, entry.Name())
		typ, err := os.ReadFile(filepath.Join(path, "type"))
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(typ)) == "Mains" {
			return path, nil
		}
	}

	// Fallback: look for a device with an "online" file but no "capacity" file.
	for _, entry := range entries {
		path := filepath.Join(base, entry.Name())
		if _, err := os.Stat(filepath.Join(path, "online")); err == nil {
			if _, err := os.Stat(filepath.Join(path, "capacity")); err != nil {
				return path, nil
			}
		}
	}

	return "", fmt.Errorf("no AC adapter found in %s", base)
}

func readBatteryCapacity(batteryPath string) (int, error) {
	data, err := os.ReadFile(filepath.Join(batteryPath, "capacity"))
	if err != nil {
		return -1, err
	}

	capacity, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return -1, fmt.Errorf("invalid battery capacity %q: %w", strings.TrimSpace(string(data)), err)
	}

	return capacity, nil
}

func readBatteryStatus(batteryPath string) (string, error) {
	data, err := os.ReadFile(filepath.Join(batteryPath, "status"))
	if err != nil {
		return "", err
	}

	return string(data), nil
}

func readACOnline(acPath string) (bool, error) {
	data, err := os.ReadFile(filepath.Join(acPath, "online"))
	if err != nil {
		return false, err
	}

	value := strings.TrimSpace(string(data))
	return value == "1", nil
}

// NopProvider always reports AC power with no battery. Useful for testing
// or when power state detection is unavailable.
type NopProvider struct{}

func (NopProvider) GetState() (State, error) {
	return State{PowerType: PowerTypeAC, Battery: -1}, nil
}
