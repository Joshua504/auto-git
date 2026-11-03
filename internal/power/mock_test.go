package power

import (
	"testing"
)

// mockProvider returns a configurable state for testing.
type mockProvider struct {
	state State
}

func (m *mockProvider) GetState() (State, error) {
	return m.state, nil
}

func TestMockProvider(t *testing.T) {
	provider := &mockProvider{
		state: State{PowerType: PowerTypeDischarging, Battery: 15},
	}

	state, err := provider.GetState()
	if err != nil {
		t.Fatalf("mockProvider.GetState() returned error: %v", err)
	}

	if !state.OnBattery() {
		t.Error("mockProvider should report on battery")
	}

	if state.Battery != 15 {
		t.Errorf("mockProvider battery = %d, want 15", state.Battery)
	}

	// Switch to AC
	provider.state = State{PowerType: PowerTypeAC, Battery: -1}
	state, err = provider.GetState()
	if err != nil {
		t.Fatalf("mockProvider.GetState() after switch returned error: %v", err)
	}

	if state.OnBattery() {
		t.Error("mockProvider should not report on battery after switch to AC")
	}
}
