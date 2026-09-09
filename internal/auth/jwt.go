package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var ErrUnauthorized = errors.New("unauthorized")

type Service struct {
	secret   []byte
	username string
	password string
	ttl      time.Duration
}

func New(username, password, secret string) *Service {
	s := &Service{
		username: username,
		password: password,
		ttl:      12 * time.Hour,
	}
	if secret == "" {
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		s.secret = b
	} else {
		s.secret = []byte(secret)
	}
	return s
}

func (s *Service) Login(username, password string) (string, error) {
	if username != s.username || password != s.password {
		return "", ErrUnauthorized
	}
	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": username,
		"iat": now.Unix(),
		"exp": now.Add(s.ttl).Unix(),
	})
	return tok.SignedString(s.secret)
}

func (s *Service) Parse(token string) (string, error) {
	parsed, err := jwt.Parse(token, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, ErrUnauthorized
		}
		return s.secret, nil
	})
	if err != nil || !parsed.Valid {
		return "", ErrUnauthorized
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return "", ErrUnauthorized
	}
	sub, _ := claims["sub"].(string)
	if sub == "" {
		return "", ErrUnauthorized
	}
	return sub, nil
}

func RandomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
