package runmode

import "testing"

func TestParse(t *testing.T) {
	for _, s := range []string{"production", "dry-run", "simulation"} {
		if _, err := Parse(s); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	for _, s := range []string{"", "prod", "dryrun", "test"} {
		if _, err := Parse(s); err == nil {
			t.Errorf("%q should fail", s)
		}
	}
}

func TestGuard(t *testing.T) {
	if NewGuard(DryRun, true).AllowDestinationWrite() {
		t.Error("dry-run must deny writes")
	}
	if !NewGuard(Production, false).AllowDestinationWrite() || !NewGuard(Simulation, true).AllowDestinationWrite() {
		t.Error("production and simulation allow writes")
	}
	if NewGuard(Production, true).TestEndpointsEnabled() {
		t.Error("production never exposes test endpoints")
	}
	if NewGuard(Simulation, false).TestEndpointsEnabled() {
		t.Error("non-faultinject build never exposes test endpoints")
	}
	if !NewGuard(Simulation, true).TestEndpointsEnabled() || !NewGuard(DryRun, true).TestEndpointsEnabled() {
		t.Error("faultinject build in simulation/dry-run exposes test endpoints")
	}
}
