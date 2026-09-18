package catalog

import (
	"context"
	"testing"
)

func TestCanManageIsStaffOnly(t *testing.T) {
	creator := int64(42)
	app := &Application{CreatedBy: &creator}
	if canManage(app, creator, false) {
		t.Fatal("legacy creator must not manage")
	}
	if !canManage(app, 7, true) {
		t.Fatal("staff must manage")
	}
}
func TestCreateRejectsNonStaffBeforeDatabase(t *testing.T) {
	s := &Service{}
	if _, _, err := s.Create(context.Background(), &CreateInput{Name: "x", CreatorID: 1}); err != ErrNotManageable {
		t.Fatalf("err=%v", err)
	}
}
