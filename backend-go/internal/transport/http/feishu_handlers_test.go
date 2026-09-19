package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	genapi "github.com/creation-agent-studio/backend-go/internal/gen/api"
	"github.com/creation-agent-studio/backend-go/internal/identity"
)

// fakeFeishuTokenResolver stands in for the aily.AuthResolver surface the
// forwarding handlers consume.
type fakeFeishuTokenResolver struct {
	token string
	err   error
}

func (f fakeFeishuTokenResolver) UserAccessToken(context.Context, int64) (string, error) {
	return f.token, f.err
}

// fakeProviderServer plays the Feishu provider: search/v1/user answers with
// one user, records the request it served, and counts requests served.
type fakeProviderServer struct {
	*httptest.Server
	calls       int
	gotQuery    string
	gotPageSize string
}

func forwardTargetsFixtureServer(t *testing.T) *fakeProviderServer {
	t.Helper()
	fp := &fakeProviderServer{}
	fp.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fp.calls++
		fp.gotQuery = r.URL.Query().Get("query")
		fp.gotPageSize = r.URL.Query().Get("page_size")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0, "msg": "ok",
			"data": map[string]any{
				"has_more": false,
				"users": []any{map[string]any{
					"open_id": "ou-1", "user_id": "on-1", "name": "张三", "avatar": nil,
				}},
			},
		})
	}))
	t.Cleanup(fp.Server.Close)
	return fp
}

func newForwardTargetsTestServer(fp *fakeProviderServer) *Server {
	return &Server{
		Feishu:     identity.NewFeishuClient(fp.URL, "id", "secret", fp.Server.Client()),
		FeishuAuth: fakeFeishuTokenResolver{token: "uat"},
	}
}

func callListFeishuForwardTargets(t *testing.T, srv *Server, params genapi.ListFeishuForwardTargetsParams) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/v2/feishu/forward/targets", nil)
	ctx := context.WithValue(r.Context(), userCtxKey, &AuthenticatedUser{
		User: &identity.User{ID: 7, Username: "caller", IsActive: true},
	})
	w := httptest.NewRecorder()
	srv.ListFeishuForwardTargets(w, r.WithContext(ctx), params)
	return w
}

// TestListFeishuForwardTargetsRequiresUserQuery (七次复审 P2-4): the Feishu
// search/v1/user contract marks query as REQUIRED. An empty query must be
// rejected with 400 at the HTTP boundary — forwarding it upstream gets a
// provider parameter error that maps to 502 and looks like a Feishu outage.
func TestListFeishuForwardTargetsRequiresUserQuery(t *testing.T) {
	provider := forwardTargetsFixtureServer(t)
	srv := newForwardTargetsTestServer(provider)
	w := callListFeishuForwardTargets(t, srv, genapi.ListFeishuForwardTargetsParams{
		Type: genapi.ListFeishuForwardTargetsParamsTypeUser,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 (empty query on type=user)", w.Code)
	}
	if provider.calls != 0 {
		t.Fatalf("provider calls=%d, want 0 (rejected before the provider)", provider.calls)
	}
}

// TestListFeishuForwardTargetsValidatesLimitRange (七次复审 P2-5): limit is
// declared 1-200 in the OpenAPI contract — out-of-range values get a 400,
// not a silent clamp into a "valid" 200.
func TestListFeishuForwardTargetsValidatesLimitRange(t *testing.T) {
	provider := forwardTargetsFixtureServer(t)
	srv := newForwardTargetsTestServer(provider)
	for _, limit := range []int{0, -1, 201, 9999} {
		l := limit
		w := callListFeishuForwardTargets(t, srv, genapi.ListFeishuForwardTargetsParams{
			Type:  genapi.ListFeishuForwardTargetsParamsTypeUser,
			Query: strPtr("张三"),
			Limit: &l,
		})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("limit=%d: status=%d, want 400", limit, w.Code)
		}
	}
	if provider.calls != 0 {
		t.Fatalf("provider calls=%d, want 0 (rejected before the provider)", provider.calls)
	}
}

// TestListFeishuForwardTargetsForwardsQueryAndLimit: a valid user search
// passes the query and the requested page size through to the provider, and
// the response envelope carries the pagination fields.
func TestListFeishuForwardTargetsForwardsQueryAndLimit(t *testing.T) {
	provider := forwardTargetsFixtureServer(t)
	srv := newForwardTargetsTestServer(provider)
	limit := 50
	w := callListFeishuForwardTargets(t, srv, genapi.ListFeishuForwardTargetsParams{
		Type:  genapi.ListFeishuForwardTargetsParamsTypeUser,
		Query: strPtr("张三"),
		Limit: &limit,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", w.Code)
	}
	if provider.gotQuery != "张三" || provider.gotPageSize != "50" {
		t.Fatalf("provider received query=%q page_size=%q, want 张三/50", provider.gotQuery, provider.gotPageSize)
	}
	var body struct {
		Items      []map[string]any `json:"items"`
		NextCursor string           `json:"next_cursor"`
		HasMore    bool             `json:"has_more"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 || body.Items[0]["id"] != "ou-1" {
		t.Fatalf("items=%v", body.Items)
	}
	if body.NextCursor != "" || body.HasMore {
		t.Fatalf("pagination envelope: next_cursor=%q has_more=%v", body.NextCursor, body.HasMore)
	}
}
