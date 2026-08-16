package migrationcompat

import (
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestBuildDatabaseURL(t *testing.T) {
	for _, tc := range []struct {
		name     string
		host     string
		port     int
		database string
		user     string
		password string
	}{
		{"tcp reserved characters", "db.example.invalid", 5432, "synthetic-db", "synthetic:user", "synthetic@pass/with?#%"},
		{"tcp ipv6", "2001:db8::1", 5432, "synthetic-db", "synthetic-user", "synthetic-pass"},
		{"unix socket", "/run/postgresql", 5432, "synthetic-db", "synthetic:user", "synthetic@pass/with?#%"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildDatabaseURL(tc.host, tc.port, tc.database, tc.user, tc.password)
			parsed, err := pgx.ParseConfig(got)
			require.NoError(t, err)
			require.Equal(t, tc.user, parsed.User)
			require.Equal(t, tc.password, parsed.Password)
			require.Equal(t, tc.database, parsed.Database)
			require.Equal(t, tc.host, parsed.Host)
			require.Equal(t, uint16(tc.port), parsed.Port)
		})
	}
}
