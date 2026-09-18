package contract

import "testing"

func TestParseValid(t *testing.T) {
	major, err := Parse("game-forge/v1")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if major != 1 {
		t.Fatalf("major = %d, want 1", major)
	}
	if !Supported("game-forge/v1") {
		t.Fatal("Supported(game-forge/v1) = false")
	}
}

func TestParseInvalid(t *testing.T) {
	for _, in := range []string{"", "game-forge", "game-forge/", "/v1", "other/v1", "game-forge/1", "game-forge/vX", "game-forge/v2"} {
		if _, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) = nil error, want error", in)
		}
		if Supported(in) {
			t.Errorf("Supported(%q) = true, want false", in)
		}
	}
}
