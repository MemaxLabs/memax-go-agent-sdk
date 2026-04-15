package identity

import "testing"

func TestWithDefaultsPreservesConfiguredFields(t *testing.T) {
	got := Identity{
		Name:        "reviewer",
		Constraints: []string{"read before writing"},
	}.WithDefaults()

	if got.Name != "reviewer" {
		t.Fatalf("Name = %q, want reviewer", got.Name)
	}
	if got.Role == "" || got.Mission == "" || got.Tone == "" || got.Autonomy == "" {
		t.Fatalf("WithDefaults left empty fields: %#v", got)
	}
	if len(got.Constraints) != 1 || got.Constraints[0] != "read before writing" {
		t.Fatalf("Constraints = %#v, want configured constraint", got.Constraints)
	}
}

func TestIsZero(t *testing.T) {
	empty := Identity{}
	if !empty.IsZero() {
		t.Fatal("empty identity should be zero")
	}
	configured := Identity{Name: "agent"}
	if configured.IsZero() {
		t.Fatal("configured identity should not be zero")
	}
}
