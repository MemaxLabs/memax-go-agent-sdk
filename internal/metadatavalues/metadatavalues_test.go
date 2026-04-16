package metadatavalues

import "testing"

func TestBool(t *testing.T) {
	metadata := map[string]any{"ok": true, "wrong": "true"}
	if !Bool(metadata, "ok") {
		t.Fatal("Bool(ok) = false, want true")
	}
	if Bool(metadata, "wrong") || Bool(metadata, "missing") {
		t.Fatal("Bool should return false for wrong or missing values")
	}
}

func TestString(t *testing.T) {
	metadata := map[string]any{"name": "database-review", "wrong": 1}
	if got := String(metadata, "name"); got != "database-review" {
		t.Fatalf("String(name) = %q, want database-review", got)
	}
	if got := String(metadata, "wrong"); got != "" {
		t.Fatalf("String(wrong) = %q, want empty", got)
	}
	if got := String(metadata, "missing"); got != "" {
		t.Fatalf("String(missing) = %q, want empty", got)
	}
}

func TestInt(t *testing.T) {
	metadata := map[string]any{
		"int":     3,
		"int64":   int64(4),
		"float64": float64(5),
		"wrong":   "6",
	}
	for key, want := range map[string]int{"int": 3, "int64": 4, "float64": 5} {
		if got := Int(metadata, key); got != want {
			t.Fatalf("Int(%s) = %d, want %d", key, got, want)
		}
	}
	if got := Int(metadata, "wrong"); got != 0 {
		t.Fatalf("Int(wrong) = %d, want 0", got)
	}
	if got := Int(metadata, "missing"); got != 0 {
		t.Fatalf("Int(missing) = %d, want 0", got)
	}
}
