package migrationcompat

import (
	"net"
	"net/url"
	"strconv"

	"github.com/shiroha-a/mk/internal/config"
)

// BuildDatabaseURL constructs a libpq/pgx DSN. Reserved URL characters in the
// user, password and host (IPv6 colons) are percent-encoded so that a credential
// containing e.g. '@' or '#' cannot break out of its URL component.
func BuildDatabaseURL(host string, port int, database, user, password string) string {
	q := url.Values{"sslmode": {"disable"}}
	u := &url.URL{Scheme: "postgres", Path: "/" + database}
	if config.IsUnixSocketPath(host) {
		// UDS の場合はホスト部を空にして host クエリにソケットディレクトリを渡す。
		// postgres://...@/var/run/postgresql:5432/... の形式は libpq が UDS として
		// 解釈できず TCP localhost にフォールバックするため。
		q.Set("host", host)
		q.Set("port", strconv.Itoa(port))
		q.Set("user", user)
		q.Set("password", password)
	} else {
		u.User = url.UserPassword(user, password)
		u.Host = net.JoinHostPort(host, strconv.Itoa(port))
	}
	u.RawQuery = q.Encode()
	return u.String()
}
