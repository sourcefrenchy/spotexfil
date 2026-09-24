package shared

import (
	"testing"
)

func TestDefaultsWithoutOverrides(t *testing.T) {
	// With no build-time injection, defaults come from protocol.json / stock labels
	if len(Proto.Transport.CoverNames) == 0 {
		t.Error("expected default cover names from protocol.json")
	}
	if len(Proto.Transport.FillerArtists) == 0 {
		t.Error("expected default filler artists from protocol.json")
	}
	if Proto.C2.MetaKeyLabel == "" {
		t.Error("expected default meta key label")
	}
	if got := HKDFLabel(); got != "spotexfil-session-v1" {
		t.Errorf("HKDFLabel default: got %q", got)
	}
	if got := SessionStoreLabel(); got != "spotexfil-session-store" {
		t.Errorf("SessionStoreLabel default: got %q", got)
	}
}

func TestApplyOverrides(t *testing.T) {
	// Save originals and restore after the test
	origNames := Proto.Transport.CoverNames
	origArtists := Proto.Transport.FillerArtists
	origLabel := Proto.C2.MetaKeyLabel
	defer func() {
		Proto.Transport.CoverNames = origNames
		Proto.Transport.FillerArtists = origArtists
		Proto.C2.MetaKeyLabel = origLabel
		OverrideCoverNamesCSV = ""
		OverrideFillerArtistsCSV = ""
		OverrideMetaKeyLabel = ""
		OverrideHKDFLabel = ""
		OverrideSessionStore = ""
		applyOverrides()
	}()

	OverrideCoverNamesCSV = "Jazz Brunch, Lo-Fi Beats ,, Gym Mix"
	OverrideFillerArtistsCSV = "Miles Davis, John Coltrane"
	OverrideMetaKeyLabel = "deadbeefcafe1234"
	OverrideHKDFLabel = "abc123"
	OverrideSessionStore = "def456"
	applyOverrides()

	if len(Proto.Transport.CoverNames) != 3 {
		t.Errorf("cover names: got %v", Proto.Transport.CoverNames)
	}
	if Proto.Transport.CoverNames[0] != "Jazz Brunch" ||
		Proto.Transport.CoverNames[1] != "Lo-Fi Beats" ||
		Proto.Transport.CoverNames[2] != "Gym Mix" {
		t.Errorf("cover names parsed wrong: %v", Proto.Transport.CoverNames)
	}
	if len(Proto.Transport.FillerArtists) != 2 {
		t.Errorf("filler artists: got %v", Proto.Transport.FillerArtists)
	}
	if Proto.C2.MetaKeyLabel != "deadbeefcafe1234" {
		t.Errorf("meta key label: got %q", Proto.C2.MetaKeyLabel)
	}
	if got := HKDFLabel(); got != "abc123" {
		t.Errorf("HKDFLabel override: got %q", got)
	}
	if got := SessionStoreLabel(); got != "def456" {
		t.Errorf("SessionStoreLabel override: got %q", got)
	}
}
