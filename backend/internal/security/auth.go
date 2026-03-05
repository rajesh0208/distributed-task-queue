// File: internal/security/auth.go
package security

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidToken = errors.New("invalid token")
	ErrTokenExpired = errors.New("token expired")
	ErrUnauthorized = errors.New("unauthorized")
)

type Claims struct {
	UserID string   `json:"user_id"`
	Email  string   `json:"email"`
	Roles  []string `json:"roles"`
	jwt.RegisteredClaims
}

type AuthService struct {
	jwtSecret     []byte
	tokenDuration time.Duration
	redis         *redis.Client
}

// NewAuthService creates an AuthService. redisClient may be nil — if so, token
// blacklisting is disabled (useful in tests that don't need Redis).
func NewAuthService(jwtSecret string, tokenDuration time.Duration, redisClient *redis.Client) *AuthService {
	return &AuthService{
		jwtSecret:     []byte(jwtSecret),
		tokenDuration: tokenDuration,
		redis:         redisClient,
	}
}

// GenerateToken creates a signed HS256 JWT embedding the user's ID, email, and
// roles. The token expires after the duration set on the AuthService (typically
// 24 h). The "taskqueue" issuer claim is included for validation.
func (a *AuthService) GenerateToken(userID, email string, roles []string) (string, error) {
	claims := Claims{
		UserID: userID,
		Email:  email,
		Roles:  roles,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(a.tokenDuration)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "taskqueue",
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(a.jwtSecret)
}

// ValidateToken parses and verifies a JWT string. It rejects tokens with an
// unexpected signing algorithm to prevent algorithm-confusion attacks, then
// checks the Redis blacklist (if configured) to honour explicit logout requests.
// Returns the embedded Claims on success.
func (a *AuthService) ValidateToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return a.jwtSecret, nil
	})

	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, ErrInvalidToken
	}

	// Check token blacklist only when Redis is available.
	if a.redis != nil {
		var keyBuilder strings.Builder
		keyBuilder.Grow(len("blacklist:") + len(tokenString))
		keyBuilder.WriteString("blacklist:")
		keyBuilder.WriteString(tokenString)

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		blacklisted, err := a.redis.Get(ctx, keyBuilder.String()).Result()
		if err == nil && blacklisted == "1" {
			return nil, ErrInvalidToken
		}
	}

	return claims, nil
}

// GenerateAPIKey creates a cryptographically random 32-byte token encoded as
// a URL-safe base64 string. Used to issue long-lived programmatic API keys.
func (a *AuthService) GenerateAPIKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// BlacklistToken stores the token in Redis under "blacklist:{token}" with the
// same TTL as the token's validity period, so ValidateToken will reject it on
// subsequent requests. No-ops when Redis is not configured.
func (a *AuthService) BlacklistToken(tokenString string) error {
	if a.redis == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return a.redis.Set(ctx, "blacklist:"+tokenString, "1", a.tokenDuration).Err()
}

// HashPassword hashes a plaintext password with bcrypt at cost 14.
// The returned string is safe to store directly in the database.
func HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), 14)
	return string(bytes), err
}

// CheckPassword returns true when the plaintext password matches the bcrypt hash.
// The comparison is constant-time, preventing timing-based enumeration attacks.
func CheckPassword(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

// AuthMiddleware returns a Fiber middleware that enforces JWT authentication.
// It extracts the token from the "Authorization: Bearer <token>" header,
// validates it, and stores user_id, email, and roles in Fiber's request-local
// store so downstream handlers can access them via c.Locals().
func AuthMiddleware(authService *AuthService) fiber.Handler {
	return func(c *fiber.Ctx) error {
		authHeader := c.Get("Authorization")
		if authHeader == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "missing authorization header",
			})
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "invalid authorization header format",
			})
		}

		token := parts[1]
		claims, err := authService.ValidateToken(token)
		if err != nil {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "invalid or expired token",
			})
		}

		c.Locals("user_id", claims.UserID)
		c.Locals("email", claims.Email)
		// Ensure roles are always []string for consistency
		roles := make([]string, len(claims.Roles))
		copy(roles, claims.Roles)
		c.Locals("roles", roles)

		return c.Next()
	}
}

// RoleMiddleware returns a Fiber middleware that enforces role-based access
// control. It reads the "roles" local set by AuthMiddleware and returns 403 if
// the user does not hold at least one of the requiredRoles. Handles both
// []string and []interface{} role representations for robustness.
func RoleMiddleware(requiredRoles ...string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		rolesInterface := c.Locals("roles")

		if rolesInterface == nil {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
				"error": "insufficient permissions",
			})
		}

		// Handle different possible types for roles
		var userRoles []string
		switch v := rolesInterface.(type) {
		case []string:
			userRoles = v
		case []interface{}:
			// Convert []interface{} to []string
			userRoles = make([]string, 0, len(v))
			for _, r := range v {
				if str, ok := r.(string); ok {
					userRoles = append(userRoles, str)
				}
			}
		default:
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
				"error": "insufficient permissions",
			})
		}

		hasRole := false
		for _, required := range requiredRoles {
			for _, role := range userRoles {
				if role == required {
					hasRole = true
					break
				}
			}
		}

		if !hasRole {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
				"error": "insufficient permissions",
			})
		}

		return c.Next()
	}
}
