package chores

import "testing"

func TestValidCategory(t *testing.T) {
	for _, c := range []string{"general", "kitchen", "bathroom", "outdoor"} {
		if !validCategory(c) {
			t.Errorf("%q should be valid", c)
		}
	}
	for _, c := range []string{"", "nope", "Kitchen"} {
		if validCategory(c) {
			t.Errorf("%q should not be valid", c)
		}
	}
	if !validCategory(defaultCategory) {
		t.Error("the default must itself be valid")
	}
}
