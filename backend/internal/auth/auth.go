// Package auth — хэширование паролей и выпуск/проверка JWT.
//
// Пароли хэшируются PBKDF2-SHA256 из стандартной библиотеки Go (crypto/pbkdf2),
// чтобы не тянуть внешние зависимости. Токены — JWT HS256 с коротким сроком жизни.
package auth

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"alfahack/backend/internal/domain"
)

const (
	pbkdfIterations = 210_000 // рекомендация OWASP для PBKDF2-SHA512/256 — порядок 2·10^5
	keyLen          = 32
)

// HashPassword возвращает строку вида pbkdf2-sha256$<iter>$<salt>$<hash>.
func HashPassword(password string) (string, error) {
	return hashWithIterations(password, pbkdfIterations)
}

func hashWithIterations(password string, iter int) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, iter, keyLen)
	if err != nil {
		return "", err
	}
	enc := base64.RawStdEncoding
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", iter, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// CheckPassword сравнивает пароль с хэшем за постоянное время.
func CheckPassword(hash, password string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1 {
		return false
	}
	enc := base64.RawStdEncoding
	salt, err1 := enc.DecodeString(parts[2])
	want, err2 := enc.DecodeString(parts[3])
	if err1 != nil || err2 != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, len(want))
	if err != nil {
		return false
	}
	return hmac.Equal(got, want)
}

// Claims — содержимое токена.
type Claims struct {
	Role      domain.Role `json:"role"`
	CompanyID string      `json:"cid,omitempty"`
	jwt.RegisteredClaims
}

// Principal — аутентифицированный пользователь в контексте запроса.
type Principal struct {
	UserID    string
	Role      domain.Role
	CompanyID string
}

// Issuer выпускает и проверяет токены.
type Issuer struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

// NewIssuer создаёт выпускающего токены. Секрет должен быть не короче 32 байт.
func NewIssuer(secret string, ttl time.Duration) (*Issuer, error) {
	if len(secret) < 32 {
		return nil, errors.New("JWT secret must be at least 32 bytes")
	}
	return &Issuer{secret: []byte(secret), ttl: ttl, now: time.Now}, nil
}

// Issue выпускает токен для пользователя.
func (i *Issuer) Issue(u domain.User) (string, time.Time, error) {
	exp := i.now().Add(i.ttl)
	claims := Claims{
		Role: u.Role, CompanyID: u.CompanyID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: u.ID, Issuer: "credit-conveyor",
			IssuedAt: jwt.NewNumericDate(i.now()), ExpiresAt: jwt.NewNumericDate(exp),
		},
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(i.secret)
	return tok, exp, err
}

// Parse проверяет подпись, алгоритм и срок действия токена.
func (i *Issuer) Parse(token string) (Principal, error) {
	var c Claims
	_, err := jwt.ParseWithClaims(token, &c, func(t *jwt.Token) (any, error) {
		return i.secret, nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer("credit-conveyor"), jwt.WithTimeFunc(i.now))
	if err != nil {
		return Principal{}, fmt.Errorf("%w: %v", domain.ErrUnauthorized, err)
	}
	if c.Subject == "" || (c.Role != domain.RoleClient && c.Role != domain.RoleUnderwriter) {
		return Principal{}, domain.ErrUnauthorized
	}
	return Principal{UserID: c.Subject, Role: c.Role, CompanyID: c.CompanyID}, nil
}
