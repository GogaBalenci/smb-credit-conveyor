package auth

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"alfahack/backend/internal/domain"
)

const secret = "test-secret-test-secret-test-secret!!"

func TestPasswordHashing(t *testing.T) {
	h, err := hashWithIterations("Pa$$w0rd", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(h, "Pa$$w0rd") {
		t.Fatal("correct password rejected")
	}
	if CheckPassword(h, "pa$$w0rd") || CheckPassword(h, "") {
		t.Fatal("wrong password accepted")
	}
	h2, _ := hashWithIterations("Pa$$w0rd", 1000)
	if h == h2 {
		t.Fatal("salt must make hashes unique")
	}
	for _, bad := range []string{"", "plain", "pbkdf2-sha256$x$y$z", "md5$1$a$b"} {
		if CheckPassword(bad, "x") {
			t.Fatalf("malformed hash %q accepted", bad)
		}
	}
}

func TestIssuer_RoundTrip(t *testing.T) {
	iss, err := NewIssuer(secret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := iss.Issue(domain.User{ID: "u1", Role: domain.RoleClient, CompanyID: "c1"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := iss.Parse(tok)
	if err != nil || p.UserID != "u1" || p.Role != domain.RoleClient || p.CompanyID != "c1" {
		t.Fatalf("got %+v %v", p, err)
	}
}

func TestIssuer_RejectsExpiredTamperedAndNone(t *testing.T) {
	iss, _ := NewIssuer(secret, time.Minute)
	tok, _, _ := iss.Issue(domain.User{ID: "u1", Role: domain.RoleClient})

	// просроченный
	iss.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if _, err := iss.Parse(tok); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("expired token accepted: %v", err)
	}
	iss.now = time.Now

	// подпись другим ключом
	other, _ := NewIssuer(strings.Repeat("x", 40), time.Minute)
	forged, _, _ := other.Issue(domain.User{ID: "u1", Role: domain.RoleUnderwriter})
	if _, err := iss.Parse(forged); err == nil {
		t.Fatal("token signed with another key accepted")
	}

	// alg=none
	none := jwt.NewWithClaims(jwt.SigningMethodNone, Claims{Role: domain.RoleUnderwriter,
		RegisteredClaims: jwt.RegisteredClaims{Subject: "u1", Issuer: "credit-conveyor", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}})
	s, _ := none.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if _, err := iss.Parse(s); err == nil {
		t.Fatal("alg=none token accepted")
	}
}

func TestNewIssuer_ShortSecret(t *testing.T) {
	if _, err := NewIssuer("short", time.Hour); err == nil {
		t.Fatal("short secret must be rejected")
	}
}
