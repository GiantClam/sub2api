//go:build unit

package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/server/routes"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestAuthRateLimitRailwaySanitizedIP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name       string
		remoteAddr string
		realIP     string
		cfIP       string
		wantIP     string
	}{
		{"direct Railway", "100.64.0.2:443", "198.51.100.10", "203.0.113.99", "198.51.100.10"},
		{"unverified Cloudflare IPv4 header", "100.64.0.2:443", "173.245.48.10", "198.51.100.10", "173.245.48.10"},
		{"unverified Cloudflare IPv6 header", "100.64.0.2:443", "2606:4700::123", "2001:db8::10", "2606:4700::123"},
		{"untrusted direct peer", "198.51.100.10:443", "173.245.48.10", "203.0.113.99", "198.51.100.10"},
		{"missing sanitized header", "100.64.0.2:443", "", "203.0.113.99", "100.64.0.2"},
		{"invalid sanitized header", "100.64.0.2:443", "invalid", "203.0.113.99", "100.64.0.2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mr := miniredis.RunT(t)
			rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			t.Cleanup(func() { _ = rdb.Close() })
			cfg := &config.Config{Server: config.ServerConfig{
				ClientIPMode:             "railway",
				TrustedProxiesConfigured: true,
				TrustedProxies:           []string{"100.64.0.0/10"},
			}}
			// Even an accidentally restored legacy DB switch cannot override the
			// explicit Railway provenance contract.
			cfg.SetForwardedClientIPSettings(true, []string{"X-Attacker-IP"})
			router := gin.New()
			configureTrustedProxies(router, cfg.Server)
			router.Use(servermiddleware.SessionBindingContext(cfg))
			router.Use(func(c *gin.Context) {
				c.Header("X-Test-Client-IP", servermiddleware.SecurityClientIP(c))
				c.Header("X-Test-Handler-IP", ip.GetClientIP(c))
				c.Header("X-Test-ACL-IP", ip.GetSecurityClientIP(c, true))
				c.Next()
			})
			noop := func(c *gin.Context) { c.Next() }
			routes.RegisterAuthRoutes(router.Group("/api/v1"), &handler.Handlers{
				Auth: &handler.AuthHandler{}, Setting: &handler.SettingHandler{},
			}, servermiddleware.JWTAuthMiddleware(noop), servermiddleware.AuditLogMiddleware(noop), rdb, nil, nil)

			post := func(realIP, cfIP string, spoof int) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(`{}`))
				req.RemoteAddr = tc.remoteAddr
				req.Header.Set("Content-Type", "application/json")
				// Railway guarantees X-Real-IP, but may preserve arbitrary injected XFF.
				req.Header.Set("X-Real-IP", realIP)
				req.Header.Set("CF-Connecting-IP", cfIP)
				req.Header.Set("X-Forwarded-For", fmt.Sprintf("203.0.113.%d", spoof))
				req.Header.Set("X-Attacker-IP", fmt.Sprintf("192.0.2.%d", spoof))
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				return w
			}
			for i := 1; i <= 6; i++ {
				cfIP := fmt.Sprintf("192.0.2.%d", i)
				w := post(tc.realIP, cfIP, i)
				require.Equal(t, tc.wantIP, w.Header().Get("X-Test-Client-IP"))
				require.Equal(t, tc.wantIP, w.Header().Get("X-Test-Handler-IP"))
				require.Equal(t, tc.wantIP, w.Header().Get("X-Test-ACL-IP"))
				wantStatus := http.StatusBadRequest // Invalid JSON fields stop before service use.
				if i == 6 {
					wantStatus = http.StatusTooManyRequests
				}
				require.Equal(t, wantStatus, w.Code, "request %d", i)
			}
			if tc.name != "untrusted direct peer" {
				// A second client behind the same Railway peer has a fresh bucket.
				realIP, cfIP := "198.51.100.11", tc.cfIP
				w := post(realIP, cfIP, 1)
				require.Equal(t, "198.51.100.11", w.Header().Get("X-Test-Client-IP"))
				require.Equal(t, http.StatusBadRequest, w.Code)
			}
		})
	}
}
