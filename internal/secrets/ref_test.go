package secrets

import (
	"errors"
	"strings"
	"testing"
)

func TestExpand(t *testing.T) {
	get := func(n string) (string, error) {
		switch n {
		case "db-password":
			return "pw1", nil
		case "api-token":
			return "tok", nil
		}
		return "", errors.New("not set: " + n)
	}
	tests := []struct {
		in, want, wantErr string
	}{
		{"plain", "plain", ""},
		{"", "", ""},
		{"${secret:db-password}", "pw1", ""},
		{"postgres://u:${secret:db-password}@db/immich", "postgres://u:pw1@db/immich", ""},
		{"${secret:db-password}${secret:api-token}", "pw1tok", ""},
		{"${secret:db-password}-${secret:db-password}", "pw1-pw1", ""},
		{"a$${secret:db-password}b", "a${secret:db-password}b", ""}, // escaped: literal text
		{"$$${secret:db-password}", "$${secret:db-password}", ""},   // a $ directly before the opener escapes it
		{"cost: $5 and ${notsecret:x}", "cost: $5 and ${notsecret:x}", ""},
		{"${secret:missing}", "", "not set: missing"},
		{"${secret:db-password", "", "unterminated"},
		{"${secret:Bad Name}", "", "must be 1-64"},
		{"${secret:}", "", "must be 1-64"},
	}
	for _, tc := range tests {
		got, err := Expand(tc.in, get)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Expand(%q) err = %v, want it to contain %q", tc.in, err, tc.wantErr)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("Expand(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestRefsAndHasRefAndCheckTemplate(t *testing.T) {
	if got := Refs("${secret:b-one} ${secret:a-two} ${secret:b-one} $${secret:escaped-one}"); strings.Join(got, ",") != "b-one,a-two" {
		t.Errorf("Refs = %v (order of first appearance, deduplicated, escapes excluded)", got)
	}
	if Refs("no refs here") != nil {
		t.Error("no refs must be nil")
	}
	if !HasRef("x ${secret:y}") || !HasRef("$${secret:y}") || HasRef("plain $5") {
		t.Error("HasRef must see the opener, escaped or not (a field that expands nothing must refuse both)")
	}
	if err := CheckTemplate("${secret:ok-name}"); err != nil {
		t.Errorf("valid template rejected: %v", err)
	}
	if err := CheckTemplate("${secret:Not Valid}"); err == nil {
		t.Error("an invalid name must fail CheckTemplate")
	}
}
