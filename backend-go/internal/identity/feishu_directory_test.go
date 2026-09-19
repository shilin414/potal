package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

// officialSearchUserFixture is the response documented byte-for-byte by
// feishu-doc 通讯录/用户/搜索用户.md (GET /open-apis/search/v1/user). It is
// a raw JSON literal — not a Go map — so a wrong field name in either the
// fixture or the decoder fails the assertion instead of compiling green
// (六次复审 P1-2: the old decoder read data.entities, a field that does not
// exist in this response, so every search compiled, passed CI and still
// returned zero contacts).
const officialSearchUserFixture = `{
	"code": 0,
	"msg": "ok",
	"data": {
		"has_more": true,
		"page_token": "20",
		"users": [
			{
				"open_id": "ou_xxx",
				"user_id": "on_xxx",
				"name": "张三",
				"avatar": {"avatar_72": "https://s3-imfile.feishucdn.com/static-resource/v1/v2_00xx_72.png"}
			}
		]
	}
}`

// TestSearchFeishuUsersDecodesOfficialUsersShape pins the decoder to the
// official contract: data.users / data.has_more / data.page_token.
func TestSearchFeishuUsersDecodesOfficialUsersShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/open-apis/search/v1/user" {
			t.Errorf("path=%s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(officialSearchUserFixture))
	}))
	defer srv.Close()
	client := NewFeishuClient(srv.URL, "id", "secret", srv.Client())
	page, err := client.SearchFeishuUsers(context.Background(), "token", "张三", 20, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Users) != 1 {
		t.Fatalf("users=%d, want 1 decoded from the official data.users shape", len(page.Users))
	}
	u := page.Users[0]
	if u.OpenID != "ou_xxx" || u.Name != "张三" {
		t.Fatalf("user=%+v", u)
	}
	if u.AvatarURL == "" {
		t.Fatalf("avatar not decoded from avatar.avatar_72: %+v", u)
	}
	if !page.HasMore || page.PageToken != "20" {
		t.Fatalf("pagination not decoded: has_more=%v page_token=%q", page.HasMore, page.PageToken)
	}
}

// TestSearchFeishuUsersSendsQueryPageSizePageToken pins the REQUEST contract:
// query / page_size / page_token must reach the provider, pageSize is
// clamped into the documented 1-200 range, and an empty page_token is
// omitted on the first page.
func TestSearchFeishuUsersSendsQueryPageSizePageToken(t *testing.T) {
	var gotQuery, gotPageSize, gotPageToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("query")
		gotPageSize = r.URL.Query().Get("page_size")
		gotPageToken = r.URL.Query().Get("page_token")
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"has_more":false,"users":[]}}`))
	}))
	defer srv.Close()
	client := NewFeishuClient(srv.URL, "id", "secret", srv.Client())
	if _, err := client.SearchFeishuUsers(context.Background(), "token", "张三", 50, "pt-9"); err != nil {
		t.Fatal(err)
	}
	if gotQuery != "张三" || gotPageSize != "50" || gotPageToken != "pt-9" {
		t.Fatalf("query=%q page_size=%q page_token=%q", gotQuery, gotPageSize, gotPageToken)
	}
	// Clamp into the provider range: 0 → default 20, >200 → max 200.
	if _, err := client.SearchFeishuUsers(context.Background(), "token", "张三", 0, ""); err != nil {
		t.Fatal(err)
	}
	if gotPageSize != "20" {
		t.Fatalf("page_size below range not clamped to 20: %q", gotPageSize)
	}
	if _, err := client.SearchFeishuUsers(context.Background(), "token", "张三", 999, ""); err != nil {
		t.Fatal(err)
	}
	if gotPageSize != "200" {
		t.Fatalf("page_size above range not clamped to 200: %q", gotPageSize)
	}
	// First page must omit page_token entirely.
	if gotPageToken != "" {
		t.Fatalf("empty page_token must not be sent: %q", gotPageToken)
	}
}

// TestListUserChatsSafetyCapFailsLoud (六次复审 P2-2): hitting the 100-page
// safety cap while the provider still reports has_more must ERROR — the
// first 10 000 chats would otherwise masquerade as the complete set
// (TRUNCATED != COMPLETE). Tokens are fresh every page so the cycle
// detector does not fire first; this isolates the page-count cap.
func TestListUserChatsSafetyCapFailsLoud(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0, "msg": "ok",
			"data": map[string]any{
				"items":      []any{map[string]any{"chat_id": "oc-x", "name": "群"}},
				"has_more":   true,
				"page_token": fmt.Sprintf("pt-%d", calls),
			},
		})
	}))
	defer srv.Close()
	client := NewFeishuClient(srv.URL, "id", "secret", srv.Client())
	chats, err := client.ListUserChats(context.Background(), "token")
	if err == nil {
		t.Fatalf("expected safety-limit error, got %d chats as if complete", len(chats))
	}
	if !strings.Contains(err.Error(), "safety limit") {
		t.Fatalf("err=%v", err)
	}
	if calls != 100 {
		t.Fatalf("provider calls=%d, want exactly the 100-page cap", calls)
	}
}

// TestListUserChatsRepeatedPageTokenFails (六次复审 §二十八): a provider
// cycling the same page_token must fail fast instead of re-requesting and
// re-appending the same page until the cap.
func TestListUserChatsRepeatedPageTokenFails(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0, "msg": "ok",
			"data": map[string]any{
				"items":      []any{map[string]any{"chat_id": "oc-x", "name": "群"}},
				"has_more":   true,
				"page_token": "pt-loop",
			},
		})
	}))
	defer srv.Close()
	client := NewFeishuClient(srv.URL, "id", "secret", srv.Client())
	chats, err := client.ListUserChats(context.Background(), "token")
	if err == nil {
		t.Fatalf("expected repeated-token error, got %d chats", len(chats))
	}
	if !strings.Contains(err.Error(), "repeated chat page token") {
		t.Fatalf("err=%v", err)
	}
	if calls != 2 {
		t.Fatalf("provider calls=%d, want fast-fail on the 2nd page", calls)
	}
}

// TestListUserChatsHasMoreWithoutTokenFails (七次复审 P1-1): the provider
// answering has_more=true with an EMPTY page_token claims a next page exists
// while providing no way to read it. The old code folded that into the
// normal end-of-list branch, so the first 100 chats masqueraded as the
// complete set (TRUNCATED != COMPLETE) — it must ERROR instead, after
// exactly one provider call.
func TestListUserChatsHasMoreWithoutTokenFails(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0, "msg": "ok",
			"data": map[string]any{
				"items":      []any{map[string]any{"chat_id": "oc-1", "name": "第一页群"}},
				"has_more":   true,
				"page_token": "",
			},
		})
	}))
	defer srv.Close()
	client := NewFeishuClient(srv.URL, "id", "secret", srv.Client())
	chats, err := client.ListUserChats(context.Background(), "token")
	if err == nil {
		t.Fatalf("expected has_more-without-token error, got %d chats as if complete", len(chats))
	}
	if !strings.Contains(err.Error(), "chat has_more without page_token") {
		t.Fatalf("err=%v", err)
	}
	if calls != 1 {
		t.Fatalf("provider calls=%d, want exactly 1 (no continuation possible)", calls)
	}
}

// TestSearchFeishuUsersHasMoreWithoutTokenFails (七次复审 P1-1): the user
// search must uphold the same continuation invariant — has_more=true with an
// empty page_token is a provider contract violation, not "the last page".
func TestSearchFeishuUsersHasMoreWithoutTokenFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"has_more":true,"page_token":"","users":[]}}`))
	}))
	defer srv.Close()
	client := NewFeishuClient(srv.URL, "id", "secret", srv.Client())
	page, err := client.SearchFeishuUsers(context.Background(), "token", "张三", 20, "")
	if err == nil {
		t.Fatalf("expected has_more-without-token error, got page=%+v as if complete", page)
	}
	if !strings.Contains(err.Error(), "user search has_more without page_token") {
		t.Fatalf("err=%v", err)
	}
}

// TestSearchFeishuUsersRepeatedTokenFails (七次复审 P1-1): the provider
// echoing back the SAME page_token it was given makes no progress — every
// continuation page would repeat forever. Fail loud instead of letting the
// caller loop (the frontend loadMore would spin on identical pages).
func TestSearchFeishuUsersRepeatedTokenFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"has_more":true,"page_token":"pt-stuck","users":[]}}`))
	}))
	defer srv.Close()
	client := NewFeishuClient(srv.URL, "id", "secret", srv.Client())
	page, err := client.SearchFeishuUsers(context.Background(), "token", "张三", 20, "pt-stuck")
	if err == nil {
		t.Fatalf("expected repeated-token error, got page=%+v", page)
	}
	if !strings.Contains(err.Error(), "made no progress") {
		t.Fatalf("err=%v", err)
	}
}
