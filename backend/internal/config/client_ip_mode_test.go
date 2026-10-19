//go:build unit

package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadRailwayClientIPModeFromEnvironment(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv("SERVER_CLIENT_IP_MODE", "railway")
	t.Setenv("SERVER_TRUSTED_PROXIES", "100.64.0.0/10")
	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, "railway", cfg.Server.ClientIPMode)
	require.Equal(t, []string{"100.64.0.0/10"}, cfg.Server.TrustedProxies)
	require.True(t, cfg.Server.TrustedProxiesConfigured)
}

func TestLoadRejectsUnknownClientIPMode(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv("SERVER_CLIENT_IP_MODE", "railwy")
	_, err := Load()
	require.ErrorContains(t, err, "server.client_ip_mode")
}
