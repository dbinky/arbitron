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
