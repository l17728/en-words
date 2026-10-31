package api

import "testing"

func TestFirstAudioSyntheticLoginDerivationMatchesParentRunner(t *testing.T) {
	capability := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	username, password, err := firstAudioSyntheticLogin(capability, "Run_20261026")
	if err != nil {
		t.Fatal(err)
	}
	if username != "first-audio-run-20261026" {
		t.Fatalf("unexpected username derivation: %q", username)
	}
	if password != "EO4xZ-EgHe12pkvfMhGnWILHqF6TQshGXoVzypx1WT4" {
		t.Fatalf("unexpected password derivation: %q", password)
	}
}

func TestFirstAudioSyntheticLoginRejectsWeakInputs(t *testing.T) {
	for _, input := range []struct {
		capability []byte
		runID      string
	}{
		{capability: []byte("short"), runID: "run_20261026"},
		{capability: []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), runID: "../run_20261026"},
	} {
		if _, _, err := firstAudioSyntheticLogin(input.capability, input.runID); err == nil {
			t.Fatalf("expected invalid identity rejection for run ID %q", input.runID)
		}
	}
}
