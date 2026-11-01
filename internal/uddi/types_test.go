package uddi

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsViewID(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"dns/view/23311109-fad4-448f-a7a7-55ad791ae7eb", true},
		{"dns/view/b78e6236-1e70-11eb-8dee-acde48001122", true},
		{"dns/view/B78E6236-1E70-11EB-8DEE-ACDE48001122", false},
		{"default", false},
		{"23311109-fad4-448f-a7a7-55ad791ae7eb", false},
		{"dns/view/default", false},
		{"dns/view/23311109-fad4-448f-a7a7-55ad791ae7eb/extra", false},
		{" dns/view/23311109-fad4-448f-a7a7-55ad791ae7eb", false},
		{"dns/auth_zone/23311109-fad4-448f-a7a7-55ad791ae7eb", false},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, IsViewID(c.in), c.in)
	}
}
