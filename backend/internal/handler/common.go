package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/opentheone/opentheone/backend/internal/middleware"
)

func ok(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, gin.H{"code": 0, "msg": "ok", "data": data})
}

func fail(c *gin.Context, status int, code int, msg string) {
	c.JSON(status, gin.H{"code": code, "msg": msg, "data": nil})
}

func currentUserID(c *gin.Context) string {
	v, _ := c.Get(middleware.CtxUserIDKey)
	s, _ := v.(string)
	return s
}
