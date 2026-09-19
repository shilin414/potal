package identity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListDirectoryDepartmentsUsesAppIdentityContract(t *testing.T) {
	var gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/open-apis/directory/v1/departments/filter" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if r.URL.Query().Get("department_id_type") != "open_department_id" {
			t.Errorf("department_id_type=%s", r.URL.Query().Get("department_id_type"))
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "ok", "data": map[string]any{"departments": []any{map[string]any{"department_id": "od-1", "name": map[string]any{"default_value": "财务"}, "parent_department_id": "0", "order_weight": "10"}}, "page_response": map[string]any{"has_more": false}}})
	}))
	defer srv.Close()
	client := NewFeishuClient(srv.URL, "id", "secret", srv.Client())
	page, err := client.ListDirectoryDepartments(context.Background(), "tenant-token", "0", "")
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer tenant-token" {
		t.Fatalf("auth=%q", gotAuth)
	}
	fields := gotBody["required_fields"].([]any)
	if len(fields) != 4 {
		t.Fatalf("required_fields=%v", fields)
	}
	if len(page.Departments) != 1 || page.Departments[0].Name.Display() != "财务" {
		t.Fatalf("page=%#v", page)
	}
}
func TestDirectoryPermissionErrorPreservesCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 99991672, "msg": "scope directory:department:list required", "data": nil})
	}))
	defer srv.Close()
	client := NewFeishuClient(srv.URL, "id", "secret", srv.Client())
	_, err := client.ListDirectoryDepartments(context.Background(), "token", "0", "")
	apiErr, ok := err.(*FeishuAPIError)
	if !ok || apiErr.Code != 99991672 {
		t.Fatalf("err=%T %v", err, err)
	}
}

func TestListDirectoryEmployeesUsesDepartmentAndStaffStatusFilter(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"employees": []any{map[string]any{
					"base_info": map[string]any{
						"employee_id":   "ou-1",
						"name":          map[string]any{"name": map[string]any{"default_value": "张三"}},
						"departments":   []any{map[string]any{"department_id": "od-1"}},
						"active_status": 2,
						"is_resigned":   false,
					},
				}},
				"page_response": map[string]any{"has_more": false},
			},
		})
	}))
	defer srv.Close()
	client := NewFeishuClient(srv.URL, "id", "secret", srv.Client())
	page, err := client.ListDirectoryEmployees(context.Background(), "token", []string{"od-1", "od-2"}, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	filter := body["filter"].(map[string]any)
	conditions := filter["conditions"].([]any)
	if len(conditions) != 2 {
		t.Fatalf("conditions=%v", conditions)
	}
	if len(page.Employees) != 1 || page.Employees[0].OpenID() != "ou-1" || page.Employees[0].DisplayName() != "张三" {
		t.Fatalf("page=%#v", page)
	}
}

// TestListUserChatsFollowsPageTokenContinuation pins the fix for the 100-chat
// truncation (五次复审 P1-3): the picker used to read ONE page of 100 groups,
// so chat 101+ could never be selected. The client must follow the provider's
// page_token continuation until has_more is false.
func TestListUserChatsFollowsPageTokenContinuation(t *testing.T) {
	var seenPageTokens []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/open-apis/im/v1/chats" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if r.URL.Query().Get("page_size") != "100" {
			t.Errorf("page_size=%s", r.URL.Query().Get("page_size"))
		}
		seenPageTokens = append(seenPageTokens, r.URL.Query().Get("page_token"))
		switch r.URL.Query().Get("page_token") {
		case "":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "msg": "ok",
				"data": map[string]any{
					"items":    []any{map[string]any{"chat_id": "oc-1", "name": "第一页群"}},
					"has_more": true, "page_token": "pt-2",
				},
			})
		case "pt-2":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "msg": "ok",
				"data": map[string]any{
					"items":    []any{map[string]any{"chat_id": "oc-101", "name": "第二页的第101个群"}},
					"has_more": true, "page_token": "pt-3",
				},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "msg": "ok",
				"data": map[string]any{
					"items":      []any{map[string]any{"chat_id": "oc-201", "name": "最后一页"}},
					"has_more":   false,
					"page_token": "",
				},
			})
		}
	}))
	defer srv.Close()
	client := NewFeishuClient(srv.URL, "id", "secret", srv.Client())
	chats, err := client.ListUserChats(context.Background(), "token")
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != 3 {
		t.Fatalf("chats=%d, want 3 (all provider pages consumed)", len(chats))
	}
	if chats[1].ChatID != "oc-101" || chats[1].Name != "第二页的第101个群" {
		t.Fatalf("chat 101 missing: %#v", chats)
	}
	// page_token 为空串的最后一页不得再发起下一次请求。
	if len(seenPageTokens) != 3 {
		t.Fatalf("page_token calls=%v", seenPageTokens)
	}
}

func boolPtr(v bool) *bool { return &v }
