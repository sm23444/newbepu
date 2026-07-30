package router

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestLimitRequestBodyCoversRoutesWithoutSignVerify(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(limitRequestBody())
	engine.POST("/unsigned", func(ctx *gin.Context) {
		_, err := ctx.GetRawData()
		if err == nil {
			ctx.String(http.StatusOK, "unexpected success")
			return
		}
		ctx.String(http.StatusRequestEntityTooLarge, err.Error())
	})

	request := httptest.NewRequest(http.MethodPost, "/unsigned", bytes.NewReader(make([]byte, maxRequestBodyBytes+1)))
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", response.Code)
	}
	if !strings.Contains(response.Body.String(), "request body too large") {
		t.Fatalf("unexpected response: %s", response.Body.String())
	}
}

func TestTrustedProxiesAreDisabledByDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	if err := configureTrustedProxies(engine, ""); err != nil {
		t.Fatalf("disable trusted proxies: %v", err)
	}
	engine.GET("/ip", func(ctx *gin.Context) {
		ctx.String(http.StatusOK, ctx.ClientIP())
	})

	request := httptest.NewRequest(http.MethodGet, "/ip", nil)
	request.RemoteAddr = "192.0.2.10:12345"
	request.Header.Set("X-Forwarded-For", "198.51.100.20")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)

	if response.Body.String() != "192.0.2.10" {
		t.Fatalf("untrusted forwarded IP was accepted: %q", response.Body.String())
	}
}

func TestConfiguredTrustedProxyCanForwardClientIP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	if err := configureTrustedProxies(engine, "192.0.2.10"); err != nil {
		t.Fatalf("configure trusted proxy: %v", err)
	}
	engine.GET("/ip", func(ctx *gin.Context) {
		ctx.String(http.StatusOK, ctx.ClientIP())
	})

	request := httptest.NewRequest(http.MethodGet, "/ip", nil)
	request.RemoteAddr = "192.0.2.10:12345"
	request.Header.Set("X-Forwarded-For", "198.51.100.20")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)

	if response.Body.String() != "198.51.100.20" {
		t.Fatalf("trusted proxy client IP=%q, want forwarded IP", response.Body.String())
	}
}
