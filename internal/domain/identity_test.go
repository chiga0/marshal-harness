package domain

import "testing"

func TestNewID(t *testing.T) {
	t.Parallel()
	id, err := NewID("run")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateID(id); err != nil {
		t.Fatal(err)
	}
	if _, err := NewID("Bad Prefix"); err == nil {
		t.Fatal("NewID accepted invalid prefix")
	}
}
