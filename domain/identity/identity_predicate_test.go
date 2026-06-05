package identity

import "testing"

func TestInDepartment(t *testing.T) {
	id := Identity{Departments: []string{"engineering", "platform"}}
	if !id.InDepartment("engineering") {
		t.Error("InDepartment(engineering) = false, want true")
	}
	if id.InDepartment("sales") {
		t.Error("InDepartment(sales) = true, want false")
	}
	// Match is on the normalized value; callers pass normalized departments.
	if id.InDepartment("Engineering") {
		t.Error("InDepartment(Engineering) matched un-normalized; want exact-normalized match only")
	}
}

func TestLeadsDepartment(t *testing.T) {
	id := Identity{LedDepartments: []string{"platform"}}
	if !id.LeadsDepartment("platform") {
		t.Error("LeadsDepartment(platform) = false, want true")
	}
	if id.LeadsDepartment("engineering") {
		t.Error("LeadsDepartment(engineering) = true, want false")
	}
}

// edge: nil/empty membership slices must not panic and must return false
func TestInDepartment_NilMembership(t *testing.T) {
	var id Identity // zero value — Departments nil
	if id.InDepartment("engineering") {
		t.Error("InDepartment on nil Departments returned true, want false")
	}
}

func TestLeadsDepartment_NilMembership(t *testing.T) {
	var id Identity // zero value — LedDepartments nil
	if id.LeadsDepartment("platform") {
		t.Error("LeadsDepartment on nil LedDepartments returned true, want false")
	}
}
