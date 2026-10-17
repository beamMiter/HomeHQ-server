package notes

import "testing"

func TestCategoryValid(t *testing.T) {
	for _, c := range []Category{CatGeneral, CatBills, CatChores, CatAppointment} {
		if !c.Valid() {
			t.Errorf("%q should be valid", c)
		}
	}
	for _, c := range []Category{"", "nope", "General", "bills "} {
		if c.Valid() {
			t.Errorf("%q should not be valid", c)
		}
	}
}
