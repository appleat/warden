package httpx

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Resp 是全站统一响应体：code=0 表示成功，非 0 时与 HTTP 状态码一致。
type Resp struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data any    `json:"data,omitempty"`
}

type Page struct {
	Items any   `json:"items"`
	Total int64 `json:"total"`
	Page  int   `json:"page"`
	Size  int   `json:"size"`
}

func OK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, Resp{Code: 0, Msg: "ok", Data: data})
}

func OKMsg(c *gin.Context, msg string, data any) {
	c.JSON(http.StatusOK, Resp{Code: 0, Msg: msg, Data: data})
}

func Fail(c *gin.Context, status int, msg string) {
	c.AbortWithStatusJSON(status, Resp{Code: status, Msg: msg})
}

func BadRequest(c *gin.Context, msg string) { Fail(c, http.StatusBadRequest, msg) }
func Unauthorized(c *gin.Context, msg string) {
	c.AbortWithStatusJSON(http.StatusUnauthorized, Resp{Code: http.StatusUnauthorized, Msg: msg})
}
func Forbidden(c *gin.Context, msg string) {
	c.AbortWithStatusJSON(http.StatusForbidden, Resp{Code: http.StatusForbidden, Msg: msg})
}
func NotFound(c *gin.Context, msg string) { Fail(c, http.StatusNotFound, msg) }
func ServerError(c *gin.Context, msg string) { Fail(c, http.StatusInternalServerError, msg) }
