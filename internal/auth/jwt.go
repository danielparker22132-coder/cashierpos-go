// Package auth — minimal HS256 JWT for the staff orders API (FIX-3 H1).
//
// The Go build authenticates its HTML UI with cookie sessions; the staff
// orders endpoints (CloudApi parity) also accept a Bearer JWT issued by
// POST /api/auth/login, so a local storefront or script can poll
// pending-count without a browser cookie. Tokens are HMAC-SHA256 over
// base64url(header).base64url(payload), standard library only, no
// external dependency. The signing secret lives in AppSettings
// ("jwt_secret"), generated once per database; the role used for
// authorization always comes from the Users table at verify time, never
// from the token claims alone.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// JWTClaims is the payload of a staff token.
type JWTClaims struct {
	Username string `json:"sub"`
	Role     string `json:"role"`
	Exp      int64  `json:"exp"`
	Iat      int64  `json:"iat"`
}

var jwtHeader = base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))

// NewJWTSecret returns a fresh random 32-byte hex secret.
func NewJWTSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func jwtSign(secret, data string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(data))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// SignJWT issues a token for username/role valid for ttl.
func SignJWT(secret, username, role string, ttl time.Duration) (string, error) {
	if secret == "" {
		return "", errors.New("لا يوجد سر توقيع")
	}
	now := time.Now()
	payload, err := json.Marshal(JWTClaims{
		Username: username, Role: role,
		Exp: now.Add(ttl).Unix(), Iat: now.Unix(),
	})
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding.EncodeToString(payload)
	data := jwtHeader + "." + enc
	return data + "." + jwtSign(secret, data), nil
}

// VerifyJWT checks the signature and expiry and returns the claims.
// It does NOT check the user still exists or is active — callers must
// load the user from the database and use the stored role.
func VerifyJWT(secret, token string) (*JWTClaims, error) {
	if secret == "" || token == "" {
		return nil, errors.New("توكن غير صحيح")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("توكن غير صحيح")
	}
	if !hmac.Equal([]byte(jwtSign(secret, parts[0]+"."+parts[1])), []byte(parts[2])) {
		return nil, errors.New("توكن غير صحيح")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("توكن غير صحيح")
	}
	var c JWTClaims
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, errors.New("توكن غير صحيح")
	}
	if c.Username == "" || c.Exp <= time.Now().Unix() {
		return nil, errors.New("التوكن منتهي الصلاحية")
	}
	return &c, nil
}
