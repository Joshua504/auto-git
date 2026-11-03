package power

import (
	"testing"
)

func TestPowerTypeString(t *testing.T) {
	tests := []struct {
		pt   PowerType
		want string
	}{
		{PowerTypeUnknown, "unknown"},
		{PowerTypeCharging, "charging"},
		{PowerTypeDischarging, "discharging"},
		{PowerTypeFull, "full"},
		{PowerTypeAC, "ac"},
	}

	for _, tt := range tests {
		if got := tt.pt.String(); got != tt.want {
			t.Errorf("PowerType(%d).String() = %q, want %q", tt.pt, got, tt.want)
		}
	}
}

func TestStateOnBattery(t *testing.T) {
	tests := []struct {
		state State
		want  bool
	}{
		{State{PowerType: PowerTypeDischarging, Battery: 50}, true},
		{State{PowerType: PowerTypeCharging, Battery: 50}, false},
		{State{PowerType: PowerTypeFull, Battery: 100}, false},
		{State{PowerType: PowerTypeAC, Battery: -1}, false},
		{State{PowerType: PowerTypeUnknown, Battery: -1}, false},
	}

	for _, tt := range tests {
		if got := tt.state.OnBattery(); got != tt.want {
			t.Errorf("State{PowerType: %s}.OnBattery() = %v, want %v",
				tt.state.PowerType, got, tt.want)
		}
	}
}

func TestNopProvider(t *testing.T) {
	provider := NopProvider{}
	state, err := provider.GetState()
	if err != nil {
		t.Fatalf("NopProvider.GetState() returned error: %v", err)
	}

	if state.PowerType != PowerTypeAC {
		t.Errorf("NopProvider.GetState().PowerType = %s, want %s",
			state.PowerType, PowerTypeAC)
	}

	if state.Battery != -1 {
		t.Errorf("NopProvider.GetState().Battery = %d, want -1", state.Battery)
	}

	if state.OnBattery() {
		t.Error("NopProvider should never report on battery")
	}
}

func TestLinuxProvider_Nop(t *testing.T) {
	// On a system without /sys/class/power_supply, the provider should
	// still be creatable and return AC state.
	provider, err := NewLinuxProvider("")
	if err != nil {
		t.Skip("LinuxProvider creation failed (expected on systems without sysfs):", err)
	}

	state, err := provider.GetState()
	if err != nil {
		t.Skip("GetState failed (expected on systems without sysfs):", err)
	}

	// On a system without a battery, it should report AC.
	if !provider.hasBattery {
		if state.PowerType != PowerTypeAC {
			t.Errorf("LinuxProvider without battery: PowerType = %s, want %s",
				state.PowerType, PowerTypeAC)
		}
		if state.Battery != -1 {
			t.Errorf("LinuxProvider without battery: Battery = %d, want -1", state.Battery)
		}
	}
}

func TestLinuxProvider_Caching(t *testing.T) {
	provider, err := NewLinuxProvider("")
	if err != nil {
		t.Skip("LinuxProvider creation failed:", err)
	}

	// First call should populate the cache.
	state1, err := provider.GetState()
	if err != nil {
		t.Skip("GetState failed:", err)
	}

	// Second call should return the cached value.
	state2, err := provider.GetState()
	if err != nil {
		t.Fatalf("Cached GetState failed: %v", err)
	}

	if state1.PowerType != state2.PowerType || state1.Battery != state2.Battery {
		t.Errorf("Cached state differs: first=%+v, second=%+v", state1, state2)
	}
}
