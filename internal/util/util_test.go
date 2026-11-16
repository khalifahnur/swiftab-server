package util

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizePaymentMethod(t *testing.T) {
	cases := map[string]string{
		"M-Pesa":       "mpesa",
		"mpesa":        "mpesa",
		" MPESA ":      "mpesa",
		"Cash":         "cash",
		"Credit Card":  "card",
		"Debit Card":   "card",
		"card":         "card",
		"Airtel Money": "airtel_money",
		"":             "",
		"Voucher":      "voucher",
	}
	for in, want := range cases {
		if got := NormalizePaymentMethod(in); got != want {
			t.Errorf("NormalizePaymentMethod(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSlugifyAndCategoryKeys(t *testing.T) {
	slugs := map[string]string{
		"Soft Drinks":      "soft-drinks",
		"  Chef's Special": "chef-s-special",
		"Starters":         "starters",
		"Kids & Family!!":  "kids-family",
	}
	for in, want := range slugs {
		got := Slugify(in)
		if got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
		if !ValidCategoryKey(got) {
			t.Errorf("slug %q is not a valid key", got)
		}
	}

	// Keys end up inside MongoDB update paths, so anything that could change
	// the path must be refused.
	for _, bad := range []string{"", "a.b", "$set", "menu.0", "Drinks", "1st", "a b", strings.Repeat("a", 40)} {
		if ValidCategoryKey(bad) {
			t.Errorf("ValidCategoryKey(%q) = true", bad)
		}
	}
	if ValidCategoryKey(Slugify("123")) {
		t.Error("a name with no letters produced a usable key")
	}
}

func TestNormalizeEmail(t *testing.T) {
	if got, ok := NormalizeEmail("  Jane.Doe@Example.COM "); !ok || got != "jane.doe@example.com" {
		t.Errorf("got %q, %v", got, ok)
	}
	for _, bad := range []string{"", "nope", "a@b", "a@@b.com", "Jane <jane@example.com>", "a b@example.com"} {
		if _, ok := NormalizeEmail(bad); ok {
			t.Errorf("NormalizeEmail(%q) accepted", bad)
		}
	}
}

func TestTokensCarryTheirRole(t *testing.T) {
	admin, _ := GenerateToken("a1", "r1", "a@example.com", RoleAdmin, time.Hour)
	waiter, _ := GenerateWaiterToken("w1", "r1", "w@example.com", time.Hour)
	user, _ := GenerateUserToken("u1", "u@example.com", time.Hour)
	reset, _ := GenerateResetToken("u@example.com", time.Hour)

	check := func(name, token, role string) *Claims {
		claims, err := ParseToken(token)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if claims.Role != role {
			t.Errorf("%s token role = %q, want %q", name, claims.Role, role)
		}
		return claims
	}
	// The admin role used to be dropped when the token was created.
	if c := check("admin", admin, RoleAdmin); c.AdminID != "a1" || c.RestaurantID != "r1" {
		t.Errorf("admin claims = %+v", c)
	}
	if c := check("waiter", waiter, RoleWaiter); c.WaiterID != "w1" || c.AdminID != "" {
		t.Errorf("waiter claims = %+v", c)
	}
	if c := check("user", user, RoleUser); c.UserID != "u1" {
		t.Errorf("user claims = %+v", c)
	}
	if c := check("reset", reset, RoleReset); c.UserID != "" || c.Email != "u@example.com" {
		t.Errorf("reset claims = %+v", c)
	}

	expired, _ := GenerateUserToken("u1", "u@example.com", -time.Second)
	if _, err := ParseToken(expired); err == nil {
		t.Error("an expired token was accepted")
	}
	if _, err := ParseToken(user + "x"); err == nil {
		t.Error("a tampered token was accepted")
	}
}

func TestPasswordHashing(t *testing.T) {
	hash, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, "correct horse") || VerifyPassword(hash, "wrong") {
		t.Error("password verification is broken")
	}
	// Hashes created by the previous setting (cost 14) must keep working.
	const cost14 = "$2a$14$"
	if strings.HasPrefix(hash, cost14) {
		t.Error("expected the cheaper cost for new hashes")
	}
}

func TestCodesAndPushTokens(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		code := GenerateCode()
		if len(code) != 6 || code[0] == '0' {
			t.Fatalf("code %q is not six digits", code)
		}
		seen[code] = true
	}
	if len(seen) < 150 {
		t.Errorf("only %d distinct codes in 200", len(seen))
	}

	if !IsExpoPushToken("ExponentPushToken[abc123]") || !IsExpoPushToken("ExpoPushToken[abc123]") {
		t.Error("valid Expo tokens rejected")
	}
	for _, bad := range []string{"", "abc", "ExponentPushToken[abc", "fcm:whatever"} {
		if IsExpoPushToken(bad) {
			t.Errorf("IsExpoPushToken(%q) = true", bad)
		}
	}
}

func TestMailerDisabledWithoutSMTP(t *testing.T) {
	var m *Mailer
	if m.Enabled() {
		t.Error("nil mailer reports enabled")
	}
	if err := NewMailerForTest().Send("a@example.com", "s", "b"); err != ErrMailDisabled {
		t.Errorf("Send without SMTP = %v, want ErrMailDisabled", err)
	}
}
