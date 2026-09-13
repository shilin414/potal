package catalog

import "testing"

func appOf(id, creator int64, public, enabled bool) *Application {
	app := &Application{
		ID:       id,
		IsPublic: public,
		Enabled:  enabled,
	}
	if creator != 0 {
		app.CreatedBy = &creator
	}
	return app
}

// The 2026-09 access policy: private (仅自己可见) is admin-only, disabled
// applications vanish for regular users, staff sees everything.
func TestVisibleSemantics(t *testing.T) {
	cases := []struct {
		name    string
		app     *Application
		scope   string
		caller  int64
		isStaff bool
		want    bool
	}{
		{"public app visible to anyone", appOf(1, 9, true, true), "public", 2, false, true},
		{"private app hidden from regular users", appOf(2, 9, false, true), "public", 2, false, false},
		{"private app hidden even from its creator", appOf(3, 2, false, true), "manage", 2, false, false},
		{"private app visible to staff", appOf(4, 9, false, true), "manage", 1, true, true},
		{"disabled app hidden from regular users", appOf(5, 9, true, false), "public", 2, false, false},
		{"disabled app visible to staff for management", appOf(6, 9, true, false), "manage", 1, true, true},
		{"manage scope = public for regular users", appOf(7, 9, true, true), "manage", 2, false, true},
		{"mine scope keeps own rows", appOf(8, 2, true, true), "mine", 2, false, true},
		{"mine scope excludes other users", appOf(9, 9, true, true), "mine", 2, false, false},
		{"staff bypasses everything", appOf(10, 9, false, false), "public", 1, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := visible(tc.app, tc.scope, tc.caller, tc.isStaff); got != tc.want {
				t.Fatalf("visible(%q, caller=%d, staff=%v) = %v, want %v",
					tc.scope, tc.caller, tc.isStaff, got, tc.want)
			}
		})
	}
}
