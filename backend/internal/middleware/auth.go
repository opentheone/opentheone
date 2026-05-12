package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/wzyjerry/opentheone/backend/internal/auth"
)

// CtxUserIDKey stores the resolved user_id in *gin.Context.
const (
	CtxUserIDKey   = "auth.user_id"
	CtxUsernameKey = "auth.username"
	CtxRoleKey     = "auth.role"
)

// JWTAuth returns a middleware that requires a valid bearer JWT.
func JWTAuth(tm *auth.TokenManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		hdr := c.GetHeader("Authorization")
		if hdr == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"code": 401,
				"msg":  "missing authorization",
			})
			return
		}
		var token string
		if strings.HasPrefix(hdr, "Bearer ") {
			token = strings.TrimPrefix(hdr, "Bearer ")
		} else {
			token = hdr
		}
		claims, err := tm.Parse(token)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"code": 401,
				"msg":  "invalid token",
			})
			return
		}
		c.Set(CtxUserIDKey, claims.UserID)
		c.Set(CtxUsernameKey, claims.Username)
		c.Set(CtxRoleKey, claims.Role)
		c.Next()
	}
}

// AdminOnly requires role=admin.
func AdminOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		role, _ := c.Get(CtxRoleKey)
		if role != "admin" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"code": 403,
				"msg":  "admin only",
			})
			return
		}
		c.Next()
	}
}
