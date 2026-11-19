package api

import "testing"

func TestSpeechManifestRevisionStableAcrossInputOrder(t *testing.T) {
	a := []speechMaterialItem{{Key: "dog|zh", Language: "zh", Text: "dog"}, {Key: "cat|en", Language: "en", Text: "cat"}}
	b := []speechMaterialItem{{Key: "cat|en", Language: "en", Text: "cat"}, {Key: "dog|zh", Language: "zh", Text: "dog"}}
	if got, want := speechManifestRevision(a), speechManifestRevision(b); got != want {
		t.Fatalf("revision changed with ordering: %s != %s", got, want)
	}
}

func TestSpeechManifestRevisionChangesWithText(t *testing.T) {
	a := []speechMaterialItem{{Key: "cat|en", Language: "en", Text: "cat"}}
	b := []speechMaterialItem{{Key: "cat|en", Language: "en", Text: "cat."}}
	if speechManifestRevision(a) == speechManifestRevision(b) {
		t.Fatal("revision did not change")
	}
}
