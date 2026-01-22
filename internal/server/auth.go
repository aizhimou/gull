package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

const (
	// SessionCookieName is the name of the session cookie
	SessionCookieName = "vget_session"
	// SessionDuration is the duration for session tokens (24 hours)
	SessionDuration = 24 * time.Hour
	// APITokenDuration is the duration for API tokens (1 year)
	APITokenDuration = 365 * 24 * time.Hour
)

// JWTClaims represents the claims in a JWT token
type JWTClaims struct {
	TokenType string         `json:"type"` // "session" or "api"
	Custom    map[string]any `json:"custom,omitempty"`
	jwt.RegisteredClaims
}

// GenerateTokenRequest is the request body for POST /api/auth/token
type GenerateTokenRequest struct {
	Payload map[string]any `json:"payload,omitempty"`
}

// generateJWT creates a new JWT token signed with the api_key
func (s *Server) generateJWT(tokenType string, duration time.Duration, customPayload map[string]any) (string, error) {
	now := time.Now()
	claims := JWTClaims{
		TokenType: tokenType,
		Custom:    customPayload,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(duration)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			Issuer:    "vget",
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(s.apiKey))
}

// validateJWT validates a JWT token and returns the claims
func (s *Server) validateJWT(tokenString string) (*JWTClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &JWTClaims{}, func(token *jwt.Token) (any, error) {
		return []byte(s.apiKey), nil
	})

	if err != nil {
		return nil, err
	}

	if claims, ok := token.Claims.(*JWTClaims); ok && token.Valid {
		return claims, nil
	}

	return nil, jwt.ErrSignatureInvalid
}

// jwtAuthMiddleware handles authentication via session cookie or Bearer token
func (s *Server) jwtAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path

		// Only API routes require auth
		if !strings.HasPrefix(path, "/api/") {
			c.Next()
			return
		}

		// Health endpoint doesn't require auth
		if path == "/api/health" {
			c.Next()
			return
		}

		// Auth endpoints don't require auth
		if strings.HasPrefix(path, "/api/auth/") {
			c.Next()
			return
		}

		// If no api_key configured, allow all requests
		if s.apiKey == "" {
			c.Next()
			return
		}

		// Check for session cookie first
		if cookie, err := c.Cookie(SessionCookieName); err == nil {
			if _, err := s.validateJWT(cookie); err == nil {
				c.Next()
				return
			}
		}

		// Check for Bearer token in Authorization header
		authHeader := c.GetHeader("Authorization")
		if token, found := strings.CutPrefix(authHeader, "Bearer "); found {
			if _, err := s.validateJWT(token); err == nil {
				c.Next()
				return
			}
		}

		// No valid authentication
		c.JSON(http.StatusUnauthorized, Response{
			Code:    401,
			Data:    nil,
			Message: "unauthorized: valid session or API token required",
		})
		c.Abort()
	}
}

// setSessionCookie sets a session cookie for browser clients
func (s *Server) setSessionCookie(c *gin.Context) {
	// Only set cookie if api_key is configured
	if s.apiKey == "" {
		return
	}

	// Check if valid session cookie already exists
	if cookie, err := c.Cookie(SessionCookieName); err == nil {
		if _, err := s.validateJWT(cookie); err == nil {
			return // Valid cookie exists, no need to set new one
		}
	}

	// Generate new session token
	token, err := s.generateJWT("session", SessionDuration, nil)
	if err != nil {
		return // Silently fail, user can still use API token
	}

	// Set cookie
	c.SetCookie(
		SessionCookieName,
		token,
		int(SessionDuration.Seconds()),
		"/",
		"",    // domain - empty means current domain
		false, // secure - false to allow HTTP
		true,  // httpOnly - prevent JS access
	)
}

// handleAuthStatus returns whether api_key is configured
func (s *Server) handleAuthStatus(c *gin.Context) {
	c.JSON(http.StatusOK, Response{
		Code: 200,
		Data: gin.H{
			"api_key_configured": s.apiKey != "",
		},
		Message: "auth status retrieved",
	})
}

// handleGenerateToken generates a new API token for external use
// Always returns HTTP 200, with status indicated in response body
func (s *Server) handleGenerateToken(c *gin.Context) {
	if s.apiKey == "" {
		c.JSON(http.StatusOK, Response{
			Code:    500,
			Data:    nil,
			Message: "API KEY is not configured",
		})
		return
	}

	// Parse optional custom payload from request body
	var req GenerateTokenRequest
	// Ignore binding errors - payload is optional
	_ = c.ShouldBindJSON(&req)

	token, err := s.generateJWT("api", APITokenDuration, req.Payload)
	if err != nil {
		c.JSON(http.StatusOK, Response{
			Code:    500,
			Data:    nil,
			Message: "failed to generate token",
		})
		return
	}

	c.JSON(http.StatusOK, Response{
		Code: 201,
		Data: gin.H{
			"jwt": token,
		},
		Message: "JWT Token generated",
	})
}
