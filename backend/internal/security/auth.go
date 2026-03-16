// Package security provides authentication, authorisation, and request-validation
// utilities for the distributed task queue API.
//
// This file implements:
//   - JWT (JSON Web Token) generation and validation
//   - Token blacklisting via Redis (to support explicit logout)
//   - Password hashing and verification with bcrypt
//   - Two Fiber middleware functions: AuthMiddleware (identity) and RoleMiddleware (permissions)
//
// Connected to:
//   - internal/models        — uses User struct fields (UserID, Roles)
//   - cmd/api/main.go        — wires AuthService and attaches the middlewares to routes
//   - internal/storage       — handlers call c.Locals("user_id") set here
//
// Called by:
//   - cmd/api/main.go        — NewAuthService, AuthMiddleware, RoleMiddleware
//   - API handlers           — read c.Locals("user_id"), c.Locals("roles")
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

// Sentinel errors returned by ValidateToken.
// Callers can use errors.Is() to distinguish rejection reasons.
var (
	// ErrInvalidToken is returned when the token cannot be parsed, has a bad
	// signature, or has been explicitly blacklisted via logout.
	ErrInvalidToken = errors.New("invalid token")

	// ErrTokenExpired is returned when the token's ExpiresAt claim is in the past.
	ErrTokenExpired = errors.New("token expired")

	// ErrUnauthorized is a generic sentinel for callers that don't need to
	// distinguish between expired and malformed tokens.
	ErrUnauthorized = errors.New("unauthorized")
)

// Claims embeds the standard JWT RegisteredClaims and adds application-specific
// fields that every handler needs after authentication succeeds.
//
// Fields:
//
//	UserID — the database primary key of the authenticated user; used to scope
//	         queries so users can only see their own tasks
//	Email  — stored in the token so handlers can display it without a DB lookup
//	Roles  — a list of role strings (e.g. ["admin", "user"]); used by
//	         RoleMiddleware to enforce RBAC without touching the database
//
// Why embed RegisteredClaims?
// The jwt library validates standard claims (ExpiresAt, Issuer, etc.) automatically
// during ParseWithClaims, so we get expiry enforcement for free.
type Claims struct {
	UserID string   `json:"user_id"`
	Email  string   `json:"email"`
	Roles  []string `json:"roles"`
	jwt.RegisteredClaims
}

// AuthService holds the JWT signing key and the Redis client used for token
// blacklisting. It is created once at startup and shared across all goroutines.
//
// Fields:
//
//	jwtSecret     — the HMAC-SHA256 signing key; loaded from the JWT_SECRET env var;
//	                must remain secret — anyone with this key can forge tokens
//	tokenDuration — how long newly issued tokens are valid (typically 24 h);
//	                shorter = more secure, longer = better UX
//	redis         — pointer to the Redis client used to store blacklisted tokens;
//	                may be nil in tests, in which case blacklisting is disabled
type AuthService struct {
	jwtSecret     []byte
	tokenDuration time.Duration
	redis         *redis.Client
}

// NewAuthService creates an AuthService.
//
// Parameters:
//
//	jwtSecret     — plaintext secret string; converted to []byte for HMAC signing
//	tokenDuration — lifetime for new tokens (e.g. 24*time.Hour)
//	redisClient   — Redis client for blacklisting; pass nil to disable blacklisting
//
// Returns:
//
//	*AuthService — ready to use; no background goroutines started
//
// Called by: cmd/api/main.go during startup dependency wiring.
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
//
// Why JWT for stateless auth?
// With JWT the server does not need to store session state. Any API instance
// can verify a token independently using only the shared jwtSecret, which is
// critical when the API is horizontally scaled across multiple containers.
//
// Parameters:
//
//	userID — database UUID of the authenticated user
//	email  — user's email address, embedded for display purposes
//	roles  — list of role strings assigned to the user in the database
//
// Returns:
//
//	string — signed JWT string, ready to be returned to the client
//	error  — non-nil if HMAC signing fails (e.g. invalid key length)
//
// Called by: login and OAuth callback handlers.
func (a *AuthService) GenerateToken(userID, email string, roles []string) (string, error) {
	claims := Claims{
		UserID: userID,
		Email:  email,
		Roles:  roles,
		RegisteredClaims: jwt.RegisteredClaims{
			// ExpiresAt triggers automatic rejection of old tokens by ParseWithClaims.
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(a.tokenDuration)),
			// IssuedAt lets the server detect replay attacks with unusually old tokens.
			IssuedAt: jwt.NewNumericDate(time.Now()),
			// Issuer is a sanity check that this token came from our service, not a
			// different system that happens to use the same signing key.
			Issuer: "taskqueue",
		},
	}

	// NewWithClaims constructs an unsigned token; SignedString adds the HMAC-SHA256
	// signature using our secret key.
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(a.jwtSecret)
}

// ValidateToken parses and verifies a JWT string. It rejects tokens with an
// unexpected signing algorithm to prevent algorithm-confusion attacks, then
// checks the Redis blacklist (if configured) to honour explicit logout requests.
// Returns the embedded Claims on success.
//
// Why check the signing method explicitly?
// The "alg:none" attack lets a malicious client set alg=none in the header to
// bypass signature verification. Asserting *jwt.SigningMethodHMAC prevents this.
//
// Why check a Redis blacklist?
// JWTs are stateless — once issued they are valid until they expire. To support
// logout we store invalidated tokens in Redis. The key uses the full token string
// so it is globally unique and expires automatically when the token would have
// expired anyway (no Redis cleanup needed).
//
// Parameters:
//
//	tokenString — raw JWT string from the Authorization header
//
// Returns:
//
//	*Claims — decoded claims if the token is valid and not blacklisted
//	error   — ErrInvalidToken if the token fails any check
//
// Called by: AuthMiddleware on every protected request.
func (a *AuthService) ValidateToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		// Guard: reject any token that uses a signing method other than HMAC.
		// This prevents algorithm-confusion attacks (e.g. alg=none or RSA downgrade).
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		// Return the signing key so the library can verify the signature.
		return a.jwtSecret, nil
	})

	if err != nil {
		// ParseWithClaims returns an error for expired tokens, bad signatures, etc.
		return nil, err
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		// token.Valid is false if any registered claim validation failed.
		return nil, ErrInvalidToken
	}

	// Check token blacklist only when Redis is available.
	// A blacklisted token is one that was explicitly revoked at logout.
	// We use strings.Builder to avoid a heap allocation for the key string.
	if a.redis != nil {
		var keyBuilder strings.Builder
		keyBuilder.Grow(len("blacklist:") + len(tokenString)) // pre-allocate exact capacity
		keyBuilder.WriteString("blacklist:")
		keyBuilder.WriteString(tokenString)

		// 2-second timeout prevents auth from blocking if Redis is slow.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		blacklisted, err := a.redis.Get(ctx, keyBuilder.String()).Result()
		if err == nil && blacklisted == "1" {
			// Token was revoked — treat it as invalid even if the signature checks out.
			return nil, ErrInvalidToken
		}
		// If Redis returns an error (e.g. key not found), we allow the request through.
		// This is a deliberate trade-off: availability > strict revocation on Redis failure.
	}

	return claims, nil
}

// GenerateAPIKey creates a cryptographically random 32-byte token encoded as
// a URL-safe base64 string. Used to issue long-lived programmatic API keys.
//
// Why 32 bytes?
// 32 bytes = 256 bits of entropy. At current computing speeds, brute-forcing
// 256-bit keys is computationally infeasible (2^256 combinations).
//
// Returns:
//
//	string — URL-safe base64 string (~43 characters), safe to pass in headers
//	error  — non-nil if the OS random source fails (extremely rare)
//
// Called by: admin API key generation endpoints.
func (a *AuthService) GenerateAPIKey() (string, error) {
	b := make([]byte, 32)
	// crypto/rand reads from the OS CSPRNG (/dev/urandom on Linux).
	// math/rand must never be used for security-sensitive tokens.
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// BlacklistToken stores the token in Redis under "blacklist:{token}" with the
// same TTL as the token's validity period, so ValidateToken will reject it on
// subsequent requests. No-ops when Redis is not configured.
//
// Why set the TTL to tokenDuration?
// The blacklist entry only needs to live as long as the token would otherwise be
// valid. Once the token would have expired naturally, rejecting it again is
// redundant. Setting the same TTL keeps Redis memory usage bounded.
//
// Parameters:
//
//	tokenString — the JWT string to invalidate
//
// Returns:
//
//	error — non-nil if the Redis SET command fails
//
// Called by: logout handler.
func (a *AuthService) BlacklistToken(tokenString string) error {
	if a.redis == nil {
		// Redis not configured — blacklisting disabled, logout is best-effort.
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// SET key "1" EX <tokenDuration seconds> — the "1" value is arbitrary;
	// what matters is the key's existence, not its value.
	return a.redis.Set(ctx, "blacklist:"+tokenString, "1", a.tokenDuration).Err()
}

// HashPassword hashes a plaintext password with bcrypt at cost 14.
// The returned string is safe to store directly in the database.
//
// Why bcrypt?
// bcrypt is a slow, adaptive hashing algorithm designed for passwords. Its cost
// factor (14 here) controls the number of iterations: cost 14 means 2^14 = 16384
// iterations. This makes brute-force and dictionary attacks impractical even with
// GPU hardware. MD5/SHA-256 are too fast for passwords — do not use them.
//
// Parameters:
//
//	password — plaintext password from the user's registration form
//
// Returns:
//
//	string — bcrypt hash, includes the salt and cost factor; safe to store in DB
//	error  — non-nil if bcrypt fails (extremely rare; would indicate memory issues)
//
// Called by: user registration handler.
func HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), 14)
	return string(bytes), err
}

// CheckPassword returns true when the plaintext password matches the bcrypt hash.
// The comparison is constant-time, preventing timing-based enumeration attacks.
//
// Why constant-time comparison?
// A timing attack measures how long a comparison takes. If the server returns
// faster for wrong passwords that differ early, an attacker can determine
// characters one at a time. bcrypt.CompareHashAndPassword is designed to take
// the same time regardless of where the mismatch occurs.
//
// Parameters:
//
//	password — plaintext password submitted in the login request
//	hash     — bcrypt hash stored in the database
//
// Returns:
//
//	bool — true if the password matches, false otherwise (also false on error)
//
// Called by: login handler.
func CheckPassword(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

// AuthMiddleware returns a Fiber middleware that enforces JWT authentication.
// It extracts the token from the "Authorization: Bearer <token>" header,
// validates it, and stores user_id, email, and roles in Fiber's request-local
// store so downstream handlers can access them via c.Locals().
//
// Middleware pattern: a Fiber middleware is a function that wraps the next handler.
// The middleware can inspect/modify the request, call c.Next() to proceed, or
// return early with an error response to block the request. The entire middleware
// chain runs in the same goroutine as the handler, so c.Locals() is goroutine-safe.
//
// Parameters:
//
//	authService — the shared AuthService with the signing key and Redis client
//
// Returns:
//
//	fiber.Handler — a middleware function; attach with app.Use() or group.Use()
//
// Called by: cmd/api/main.go when building the protected route group.
func AuthMiddleware(authService *AuthService) fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Read the Authorization header; it must be present for protected routes.
		authHeader := c.Get("Authorization")
		if authHeader == "" {
			// 401 Unauthorized — the client must provide credentials.
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "missing authorization header",
			})
		}

		// The header must follow the "Bearer <token>" format defined in RFC 6750.
		// Splitting on " " gives ["Bearer", "<token>"]; any other shape is rejected.
		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "invalid authorization header format",
			})
		}

		token := parts[1]
		claims, err := authService.ValidateToken(token)
		if err != nil {
			// Covers expired, malformed, blacklisted, and algorithm-confused tokens.
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "invalid or expired token",
			})
		}

		// Store decoded identity in the request context so downstream handlers
		// can call c.Locals("user_id") without re-parsing the token.
		c.Locals("user_id", claims.UserID)
		c.Locals("email", claims.Email)
		// Copy roles into a fresh slice so downstream code cannot mutate the Claims.
		roles := make([]string, len(claims.Roles))
		copy(roles, claims.Roles)
		c.Locals("roles", roles)

		// Pass control to the next middleware or handler in the chain.
		return c.Next()
	}
}

// RoleMiddleware returns a Fiber middleware that enforces role-based access
// control. It reads the "roles" local set by AuthMiddleware and returns 403 if
// the user does not hold at least one of the requiredRoles. Handles both
// []string and []interface{} role representations for robustness.
//
// Why RBAC (Role-Based Access Control)?
// Instead of hard-coding per-user permissions, RBAC assigns permissions to roles
// ("admin", "user") and roles to users. This makes it easy to grant admin access
// without modifying handler logic. For example, only "admin" can delete another
// user's task.
//
// Parameters:
//
//	requiredRoles — variadic list of role names; user needs at least ONE of these
//
// Returns:
//
//	fiber.Handler — attach after AuthMiddleware in the route chain
//
// Called by: admin route group in cmd/api/main.go.
func RoleMiddleware(requiredRoles ...string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		rolesInterface := c.Locals("roles")

		if rolesInterface == nil {
			// AuthMiddleware was not applied before RoleMiddleware — misconfigured chain.
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
				"error": "insufficient permissions",
			})
		}

		// Roles can arrive as []string (set by AuthMiddleware above) or as
		// []interface{} (if deserialized from JSON elsewhere). Handle both.
		var userRoles []string
		switch v := rolesInterface.(type) {
		case []string:
			userRoles = v
		case []interface{}:
			// Convert []interface{} to []string by asserting each element.
			userRoles = make([]string, 0, len(v))
			for _, r := range v {
				if str, ok := r.(string); ok {
					userRoles = append(userRoles, str)
				}
			}
		default:
			// Unexpected type — deny access to be safe.
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
				"error": "insufficient permissions",
			})
		}

		// Linear scan: check if the user holds any of the required roles.
		// Role lists are tiny (1–3 elements), so O(n²) is fine here.
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
			// 403 Forbidden — identity is known but access is denied.
			// (401 would be wrong here because the user IS authenticated.)
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
				"error": "insufficient permissions",
			})
		}

		// User has the required role — proceed to the handler.
		return c.Next()
	}
}
