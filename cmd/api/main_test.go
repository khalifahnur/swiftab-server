package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"swiftab/server/internal/config"
	"swiftab/server/internal/events"
	"swiftab/server/internal/handler"
	"swiftab/server/internal/middleware"
	"swiftab/server/internal/util"
)

// testRouter builds the real route table with no database. Requests that the
// auth middleware rejects never reach a handler, so those paths are safe to
// exercise.
func testRouter(requireMobile bool) *http.ServeMux {
	base := &handler.Base{Cfg: &config.Config{Env: "production", Location: time.UTC}, Hub: events.NewHub()}
	return newRouter(base, &middleware.Auth{RequireMobile: requireMobile})
}

func TestRouteTableRegistersWithoutConflicts(t *testing.T) {
	// http.ServeMux panics at registration when two patterns overlap
	// ambiguously, which would stop the server from starting.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("route registration panicked: %v", r)
		}
	}()
	testRouter(false)
}

var adminRoutes = []struct{ method, path string }{
	{"GET", "/admin/info"},
	{"PUT", "/admin/profile"},
	{"PUT", "/admin/password"},
	{"POST", "/admin/restaurant"},
	{"PUT", "/admin/restaurant"},
	{"GET", "/admin/dashboard"},
	{"GET", "/admin/events"},
	{"POST", "/admin/waiters/signup"},
	{"GET", "/admin/waiters"},
	{"POST", "/admin/waiters/65f0c0c0c0c0c0c0c0c0c0c0/invite"},
	{"DELETE", "/admin/waiters/65f0c0c0c0c0c0c0c0c0c0c0"},
	{"GET", "/admin/menu"},
	{"POST", "/admin/menu/categories"},
	{"PUT", "/admin/menu/categories"},
	{"PUT", "/admin/menu/categories/drinks"},
	{"DELETE", "/admin/menu/categories/drinks"},
	{"POST", "/admin/menu/drinks"},
	{"PUT", "/admin/menu/drinks/65f0c0c0c0c0c0c0c0c0c0c0"},
	{"DELETE", "/admin/menu/drinks/65f0c0c0c0c0c0c0c0c0c0c0"},
	{"GET", "/admin/layout/info"},
	{"POST", "/admin/layout/info"},
	{"GET", "/admin/layout/tables"},
	{"POST", "/admin/layout/tables"},
	{"GET", "/admin/layout/live"},
	{"GET", "/admin/orders"},
	{"PUT", "/admin/orders/65f0c0c0c0c0c0c0c0c0c0c0"},
	{"GET", "/admin/reservations"},
	{"PUT", "/admin/reservations/65f0c0c0c0c0c0c0c0c0c0c0/status"},
	{"GET", "/admin/promotions"},
	{"POST", "/admin/promotions"},
	{"PUT", "/admin/promotions/65f0c0c0c0c0c0c0c0c0c0c0"},
	{"DELETE", "/admin/promotions/65f0c0c0c0c0c0c0c0c0c0c0"},
	{"GET", "/admin/tables/qr"},
}

func TestAdminRoutesRequireASession(t *testing.T) {
	mux := testRouter(false)
	for _, rt := range adminRoutes {
		req := httptest.NewRequest(rt.method, apiPrefix+rt.path, strings.NewReader("{}"))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a session = %d, want 401", rt.method, rt.path, rec.Code)
		}
	}
}

// Naming a restaurant in a header used to be enough to reach the dashboard's
// routes for it. A restaurant id is public, so that must not work.
func TestRestaurantHeaderIsNotACredentialForAdminRoutes(t *testing.T) {
	mux := testRouter(false)
	for _, rt := range adminRoutes {
		req := httptest.NewRequest(rt.method, apiPrefix+rt.path, strings.NewReader("{}"))
		req.Header.Set("X-Restaurant-ID", "65f0c0c0c0c0c0c0c0c0c0c0")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with only X-Restaurant-ID = %d, want 401", rt.method, rt.path, rec.Code)
		}
	}
}

func TestWaiterAndUserTokensCannotOpenTheDashboard(t *testing.T) {
	mux := testRouter(false)
	waiter, _ := util.GenerateWaiterToken("65f0c0c0c0c0c0c0c0c0c0c1", "65f0c0c0c0c0c0c0c0c0c0c0", "w@example.com", time.Hour)
	user, _ := util.GenerateUserToken("65f0c0c0c0c0c0c0c0c0c0c2", "u@example.com", time.Hour)

	for name, token := range map[string]string{"waiter": waiter, "user": user} {
		req := httptest.NewRequest("GET", apiPrefix+"/admin/orders", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s token on /admin/orders = %d, want 401", name, rec.Code)
		}

		req = httptest.NewRequest("GET", apiPrefix+"/admin/orders", nil)
		req.AddCookie(&http.Cookie{Name: middleware.AccessCookie, Value: token})
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s token as cookie on /admin/orders = %d, want 401", name, rec.Code)
		}
	}
}

func TestMobileRoutesRejectAnonymousCallersWhenTokensAreRequired(t *testing.T) {
	mux := testRouter(true)
	routes := []struct{ method, path string }{
		{"GET", "/users/65f0c0c0c0c0c0c0c0c0c0c2/orders"},
		{"POST", "/users/orders"},
		{"GET", "/users/65f0c0c0c0c0c0c0c0c0c0c2/reservations/active"},
		{"GET", "/waiters/restaurants/65f0c0c0c0c0c0c0c0c0c0c0/orders"},
		{"PUT", "/waiters/orders/65f0c0c0c0c0c0c0c0c0c0c3/status"},
	}
	for _, rt := range routes {
		req := httptest.NewRequest(rt.method, apiPrefix+rt.path, strings.NewReader("{}"))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s anonymously = %d, want 401", rt.method, rt.path, rec.Code)
		}
	}
}

func TestExpiredOrGarbageMobileTokenIsRejectedEvenWhenTokensAreOptional(t *testing.T) {
	mux := testRouter(false)
	expired, _ := util.GenerateUserToken("65f0c0c0c0c0c0c0c0c0c0c2", "u@example.com", -time.Minute)
	for name, token := range map[string]string{"expired": expired, "garbage": "not-a-token"} {
		req := httptest.NewRequest("GET", apiPrefix+"/users/65f0c0c0c0c0c0c0c0c0c0c2/orders", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s token = %d, want 401", name, rec.Code)
		}
	}
}

// A diner's token must not read another diner's data. The handler rejects
// the request before touching the database.
func TestUserCannotReadAnotherUsersData(t *testing.T) {
	mux := testRouter(false)
	token, _ := util.GenerateUserToken("65f0c0c0c0c0c0c0c0c0c0c2", "u@example.com", time.Hour)
	other := "65f0c0c0c0c0c0c0c0c0c0c9"
	for _, path := range []string{
		"/users/" + other + "/orders",
		"/users/" + other + "/reservations/active",
		"/users/" + other + "/reservations/completed",
		"/users/" + other + "/reservations/cancelled",
		"/users/" + other + "/recently-viewed",
	} {
		req := httptest.NewRequest("GET", apiPrefix+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("GET %s as another user = %d, want 403", path, rec.Code)
		}
	}
}

func TestWaiterCannotSeeAnotherRestaurantsOrders(t *testing.T) {
	mux := testRouter(false)
	token, _ := util.GenerateWaiterToken("65f0c0c0c0c0c0c0c0c0c0c1", "65f0c0c0c0c0c0c0c0c0c0c0", "w@example.com", time.Hour)
	for _, path := range []string{
		"/waiters/restaurants/65f0c0c0c0c0c0c0c0c0c0ff/orders",
		"/waiters/restaurants/65f0c0c0c0c0c0c0c0c0c0ff/floor",
	} {
		req := httptest.NewRequest("GET", apiPrefix+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("GET %s for another restaurant = %d, want 403", path, rec.Code)
		}
	}
}

func TestLogoutClearsTheCookieItSet(t *testing.T) {
	base := &handler.Base{Cfg: &config.Config{Env: "production", CookieDomain: ".swiftab.co.ke", Location: time.UTC}}
	mux := newRouter(base, &middleware.Auth{})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", apiPrefix+"/admin/logout", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("logout without a session = %d, want 200", rec.Code)
	}

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("got %d cookies, want 1", len(cookies))
	}
	c := cookies[0]
	// A browser only drops a cookie when name, domain and path match the
	// cookie that was set at sign-in.
	if c.Name != middleware.AccessCookie || c.Domain != "swiftab.co.ke" || c.Path != "/" {
		t.Errorf("cleared cookie %q domain=%q path=%q does not match the session cookie", c.Name, c.Domain, c.Path)
	}
	if c.MaxAge >= 0 || c.Value != "" {
		t.Errorf("cookie is not expired: MaxAge=%d Value=%q", c.MaxAge, c.Value)
	}
	if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteNoneMode {
		t.Errorf("cookie attributes differ from the sign-in cookie: %+v", c)
	}
}

func TestCORS(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	cases := []struct {
		name   string
		cfg    *config.Config
		origin string
		want   string
	}{
		{"production allows the dashboard", &config.Config{Env: "production"}, "https://www.swiftab.co.ke", "https://www.swiftab.co.ke"},
		{"production allows a configured origin", &config.Config{Env: "production", AllowedOrigins: []string{"https://staging.example.com"}}, "https://staging.example.com", "https://staging.example.com"},
		{"production refuses an unknown site", &config.Config{Env: "production"}, "https://evil.example.com", ""},
		{"development allows localhost", &config.Config{Env: "development"}, "http://localhost:3000", "http://localhost:3000"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest("GET", "/health", nil)
		req.Header.Set("Origin", tc.origin)
		rec := httptest.NewRecorder()
		cors(tc.cfg, ok).ServeHTTP(rec, req)
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != tc.want {
			t.Errorf("%s: Access-Control-Allow-Origin = %q, want %q", tc.name, got, tc.want)
		}
		if tc.want == "" && rec.Header().Get("Access-Control-Allow-Credentials") != "" {
			t.Errorf("%s: credentials allowed for a refused origin", tc.name)
		}
	}

	req := httptest.NewRequest("OPTIONS", apiPrefix+"/admin/orders", nil)
	req.Header.Set("Origin", "https://www.swiftab.co.ke")
	rec := httptest.NewRecorder()
	cors(&config.Config{Env: "production"}, ok).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight = %d, want 204", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Access-Control-Allow-Methods"), "PUT") {
		t.Errorf("preflight does not allow PUT: %q", rec.Header().Get("Access-Control-Allow-Methods"))
	}
}

func TestTableQRLinksNeedTheirCode(t *testing.T) {
	mux := testRouter(false)
	const rest = "65f0c0c0c0c0c0c0c0c0c0c0"
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/dine/tables/" + rest + "/t-1", ""},
		{"GET", "/dine/tables/" + rest + "/t-1?code=made-up", ""},
		{"GET", "/dine/tables/" + rest + "/t-2?code=" + util.TableCode(rest, "t-1"), ""},
		{"POST", "/dine/tables/" + rest + "/t-1/checkin", `{"code":"made-up","name":"Ann"}`},
	} {
		req := httptest.NewRequest(tc.method, apiPrefix+tc.path, strings.NewReader(tc.body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: got %d, want 404", tc.method, tc.path, rec.Code)
		}
	}
}

func TestGuestRoutesNeedAGuestToken(t *testing.T) {
	mux := testRouter(false)
	userToken, _ := util.GenerateUserToken("65f0c0c0c0c0c0c0c0c0c0c1", "a@b.co", time.Hour)
	waiterToken, _ := util.GenerateWaiterToken("65f0c0c0c0c0c0c0c0c0c0c2", "65f0c0c0c0c0c0c0c0c0c0c0", "w@b.co", time.Hour)
	for _, token := range []string{"", "garbage", userToken, waiterToken} {
		for _, rt := range []struct{ method, path string }{
			{"GET", "/dine/visit"},
			{"POST", "/dine/visit/orders"},
			{"PUT", "/dine/visit/orders/65f0c0c0c0c0c0c0c0c0c0c3/bill"},
			{"PUT", "/dine/visit/orders/65f0c0c0c0c0c0c0c0c0c0c3/cancel"},
		} {
			req := httptest.NewRequest(rt.method, apiPrefix+rt.path, strings.NewReader("{}"))
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s %s with token %q: got %d, want 401", rt.method, rt.path, token[:min(len(token), 8)], rec.Code)
			}
		}
	}
}

func TestGuestTokenCannotOpenDinerOrWaiterRoutes(t *testing.T) {
	mux := testRouter(false)
	guest, _ := util.GenerateGuestToken("65f0c0c0c0c0c0c0c0c0c0c0", "RES-AAAA-1", time.Hour)
	for _, path := range []string{
		"/users/65f0c0c0c0c0c0c0c0c0c0c1/orders",
		"/waiters/restaurants/65f0c0c0c0c0c0c0c0c0c0c0/orders?tab=served",
	} {
		req := httptest.NewRequest("GET", apiPrefix+path, nil)
		req.Header.Set("Authorization", "Bearer "+guest)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("GET %s with a guest token: got %d, want 403", path, rec.Code)
		}
	}
	req := httptest.NewRequest("GET", apiPrefix+"/admin/orders", nil)
	req.Header.Set("Authorization", "Bearer "+guest)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /admin/orders with a guest token: got %d, want 401", rec.Code)
	}
}
