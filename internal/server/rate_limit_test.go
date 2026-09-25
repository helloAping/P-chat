package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRateLimitMiddleware_AllowsLoopbackBurst(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(rateLimitMiddleware())
	r.GET("/api/v1/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	for i := 0; i < 64; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		req.RemoteAddr = "127.0.0.1:49152"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code == http.StatusTooManyRequests {
			t.Fatalf("loopback request %d was rate-limited: %s", i+1, w.Body.String())
		}
		if w.Code != http.StatusOK {
			t.Fatalf("loopback request %d status = %d, want 200; body=%s", i+1, w.Code, w.Body.String())
		}
	}
}

func TestRateLimitMiddleware_LimitsNonLoopbackBurst(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(rateLimitMiddleware())
	r.GET("/api/v1/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	limited := false
	for i := 0; i < 64; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		req.RemoteAddr = "203.0.113.10:49152"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
		if w.Code != http.StatusOK {
			t.Fatalf("non-loopback request %d status = %d, want 200 or 429; body=%s", i+1, w.Code, w.Body.String())
		}
	}
	if !limited {
		t.Fatal("non-loopback burst was not rate-limited")
	}
}
