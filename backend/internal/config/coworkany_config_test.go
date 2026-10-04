package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadCoworkanyDefaultGroup(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		env     string
		wantID  int64
		wantErr string
	}{
		{name: "disabled by default"},
		{name: "configured through yaml", yaml: "coworkany:\n  default_group_id: 47\n", wantID: 47},
		{name: "configured through environment only", env: "83", wantID: 83},
		{name: "environment overrides yaml", yaml: "coworkany:\n  default_group_id: 47\n", env: "83", wantID: 83},
		{name: "negative IDs rejected", env: "-1", wantErr: "coworkany.default_group_id must be non-negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetViperWithJWTSecret(t)
			t.Setenv("COWORKANY_DEFAULT_GROUP_ID", tt.env)
			if tt.yaml != "" {
				path := filepath.Join(t.TempDir(), "config.yaml")
				require.NoError(t, os.WriteFile(path, []byte(tt.yaml), 0o600))
				t.Setenv("CONFIG_FILE", path)
			}
			cfg, err := Load()
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantID, cfg.Coworkany.DefaultGroupID)
		})
	}
}
