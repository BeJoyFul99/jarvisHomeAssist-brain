package middleware

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"

	"jarvishomeassist-brain/internal/models"
)

// JWTAuth returns a Gin middleware that validates Bearer tokens.
// It verifies the JWT signature/expiry, then checks that the token matches
// the bcrypt hash stored on the user row (so clearing jwt_token instantly
// revokes the session).
// On success it sets "user_email", "user_role", and "user_name" on the context.
func JWTAuth(secret string, db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if header == "" || !strings.HasPrefix(header, "Bearer ") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "missing_token",
				"message": "Authorization header with Bearer token is required",
			})
			return
		}

		tokenStr := strings.TrimPrefix(header, "Bearer ")
		token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, jwt.ErrSignatureInvalid
			}
			return []byte(secret), nil
		})
		if err != nil || !token.Valid {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "invalid_token",
				"message": "Token is invalid or expired",
			})
			return
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "invalid_claims",
				"message": "Could not parse token claims",
			})
			return
		}

		email, _ := claims["email"].(string)

		// Verify the token is still the active one stored in the database
		var user models.User
		if err := db.Where("email = ?", email).Take(&user).Error; err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "invalid_token",
				"message": "User not found",
			})
			return
		}

		if user.IsLocked {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error":   "account_locked",
				"message": "Account is locked",
			})
			return
		}

		// If jwt_token is empty the session has been revoked (logout / admin revoke)
		if user.JWTToken == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "token_revoked",
				"message": "Session has been revoked, please login again",
			})
			return
		}

		tokenHash := sha256.Sum256([]byte(tokenStr))
		if hex.EncodeToString(tokenHash[:]) != user.JWTToken {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "token_revoked",
				"message": "Session has been revoked, please login again",
			})
			return
		}

		c.Set("user_email", claims["email"])
		c.Set("user_role", claims["role"])
		c.Set("user_name", claims["name"])
		c.Next()
	}
}

// RequireResourcePerm returns middleware that checks if the user has a specific
// resource permission. Administrators are always allowed.
func RequireResourcePerm(db *gorm.DB, perm string) gin.HandlerFunc {
	return func(c *gin.Context) {
		role, _ := c.Get("user_role")
		if role == "administrator" {
			c.Next()
			return
		}

		email, _ := c.Get("user_email")
		var user models.User
		if err := db.Where("email = ?", email).Take(&user).Error; err != nil {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error":   "forbidden",
				"message": "Access denied",
			})
			return
		}

		if !user.HasResourcePerm(perm) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error":   "insufficient_permissions",
				"message": "Missing permission: " + perm,
			})
			return
		}
		c.Next()
	}
}

// RequireRole returns middleware that checks the user_role set by JWTAuth.
func RequireRole(allowed ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		role, exists := c.Get("user_role")
		if !exists {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error":   "forbidden",
				"message": "Access denied",
			})
			return
		}

		roleStr, _ := role.(string)
		for _, a := range allowed {
			if roleStr == a {
				c.Next()
				return
			}
		}

		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error":   "insufficient_role",
			"message": "You do not have permission to access this resource",
		})
	}
}
