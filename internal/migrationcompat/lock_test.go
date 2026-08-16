package migrationcompat

import (
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"math"
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/require"
)

// errScanSentinel is returned by fake rows to prove scan errors propagate.
var errScanSentinel = errors.New("sentinel scan error")

// fakeRow simulates the single-row result of pg_try_advisory_lock /
// pg_advisory_unlock.
type fakeRow struct {
	acquired bool
	err      error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*(dest[0].(*bool)) = r.acquired
	return nil
}

// fakeConn implements queryRower and Close without a networked database.
type fakeConn struct {
	row      fakeRow
	closeErr error
	closed   bool
	sqls     []string
}

func (c *fakeConn) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	c.sqls = append(c.sqls, sql)
	return c.row
}

// Begin は lock 単体の unit test が呼ばない経路のため、固定の sentinel エラーを
// 返すだけでよい。Reconcile の Begin 失敗分岐はこの返り値で検証する。
func (c *fakeConn) Begin(ctx context.Context) (pgx.Tx, error) {
	return nil, errScanSentinel
}

func (c *fakeConn) Close(ctx context.Context) error {
	c.closed = true
	return c.closeErr
}

// withConnector replaces the package connection seam for the duration of a test.
func withConnector(t *testing.T, fn func(context.Context, string) (lockConn, error)) {
	t.Helper()
	orig := connector
	connector = fn
	t.Cleanup(func() { connector = orig })
}

func TestTryAcquireAcceptsAvailableLock(t *testing.T) {
	conn := &fakeConn{row: fakeRow{acquired: true}}

	acquired, err := tryAcquire(context.Background(), conn, advisoryLockKey)

	require.NoError(t, err)
	require.True(t, acquired)
	require.Len(t, conn.sqls, 1)
	require.Contains(t, conn.sqls[0], "pg_try_advisory_lock")
}

func TestTryAcquireReturnsErrLocked(t *testing.T) {
	conn := &fakeConn{row: fakeRow{acquired: false}}

	acquired, err := tryAcquire(context.Background(), conn, advisoryLockKey)

	require.NoError(t, err)
	require.False(t, acquired)
}

func TestTryAcquirePropagatesScanError(t *testing.T) {
	conn := &fakeConn{row: fakeRow{err: errScanSentinel}}

	_, err := tryAcquire(context.Background(), conn, advisoryLockKey)

	require.ErrorIs(t, err, errScanSentinel)
}

func TestAcquirePropagatesConnectError(t *testing.T) {
	// 既定のコネクタ (pgx.Connect) を使い、URL 解析エラーでネットワークなしに
	// 失敗することを確認する。password を含む URL がエラーに出ないことも確認。
	lock, err := Acquire(context.Background(), "postgres://user:pass@localhost:notaport/mk")

	require.Error(t, err)
	require.NotContains(t, err.Error(), "pass")
	require.Nil(t, lock)
}

func TestAcquireClosesConnectionOnScanError(t *testing.T) {
	conn := &fakeConn{row: fakeRow{err: errScanSentinel}}
	withConnector(t, func(ctx context.Context, databaseURL string) (lockConn, error) {
		return conn, nil
	})

	lock, err := Acquire(context.Background(), "postgres://user:pass@db.example:5432/mk")

	require.ErrorIs(t, err, errScanSentinel)
	require.Nil(t, lock)
	require.True(t, conn.closed)
}

func TestAcquireClosesConnectionOnLocked(t *testing.T) {
	conn := &fakeConn{row: fakeRow{acquired: false}}
	withConnector(t, func(ctx context.Context, databaseURL string) (lockConn, error) {
		return conn, nil
	})

	lock, err := Acquire(context.Background(), "postgres://user:pass@db.example:5432/mk")

	require.ErrorIs(t, err, ErrLocked)
	require.Nil(t, lock)
	require.True(t, conn.closed)
}

func TestAcquireReturnsLockOnSuccess(t *testing.T) {
	conn := &fakeConn{row: fakeRow{acquired: true}}
	withConnector(t, func(ctx context.Context, databaseURL string) (lockConn, error) {
		return conn, nil
	})

	lock, err := Acquire(context.Background(), "postgres://user:pass@db.example:5432/mk")

	require.NoError(t, err)
	require.NotNil(t, lock)
	require.False(t, conn.closed)

	require.NoError(t, lock.Release(context.Background()))
	require.True(t, conn.closed)
	require.Len(t, conn.sqls, 2)
	require.Contains(t, conn.sqls[0], "pg_try_advisory_lock")
	require.Contains(t, conn.sqls[1], "pg_advisory_unlock")
}

func TestReleaseClosesConnectionOnUnlockError(t *testing.T) {
	conn := &fakeConn{row: fakeRow{err: errScanSentinel}}
	lock := &Lock{conn: conn}

	err := lock.Release(context.Background())

	require.ErrorIs(t, err, errScanSentinel)
	require.True(t, conn.closed)
}

// golang-migrate の postgres driver の自前 advisory lock キーは
// crc32.ChecksumIEEE(schema \x00 table \x00 db) に salt を uint32 乗算した値で、
// 常に [0, math.MaxUint32] に収まる。固定キーを math.MaxUint32 より大きく
// することで、migrate が取り得る全キー空間と必ず排他になる。
func TestAdvisoryLockKeyExceedsUint32(t *testing.T) {
	if advisoryLockKey <= math.MaxUint32 || advisoryLockKey <= 0 {
		t.Fatalf("advisoryLockKey %d must be positive and > math.MaxUint32 (%d)", advisoryLockKey, math.MaxUint32)
	}
}

// TestAdvisoryLockKeyDisjointFromMigrateHash は golang-migrate の自前ロックキーと
// 固定キーが値域ごと交わらないことを確認する。まず定数範囲だけで排他を示し
// (migrateキーは uint32 演算なので高々 math.MaxUint32 < advisoryLockKey)、
// さらに実PostgreSQL(隔離されたテストDB)が使える環境では実DBの名前から
// 同じ crc32 計算を再現して、そのキーが固定キー未満であることを直接確認する。
func TestAdvisoryLockKeyDisjointFromMigrateHash(t *testing.T) {
	// 定数範囲による証明: golang-migrate のキーは GenerateAdvisoryLockId の
	// uint32 演算結果なので最大でも math.MaxUint32 であり、固定キーはそれより大きい。
	if advisoryLockKey <= math.MaxUint32 {
		t.Fatalf("advisoryLockKey %d must exceed every golang-migrate uint32 key", advisoryLockKey)
	}

	// 実DBが使える場合の追加確認。使えない環境では上の定数範囲の証明だけで排他が保証される。
	db, err := testutil.OpenTestDB()
	if err != nil {
		t.Skipf("isolated PostgreSQL unavailable: %v", err)
	}
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer func() { _ = sqlDB.Close() }()

	var dbName, schema string
	if err := db.Raw("SELECT current_database(), current_schema()").Row().Scan(&dbName, &schema); err != nil {
		t.Fatalf("read db/schema: %v", err)
	}

	// database/util.go の GenerateAdvisoryLockId と同一の計算を再現する:
	//   sum = crc32.ChecksumIEEE(schema \x00 "schema_migrations" \x00 dbName)
	//   key = uint32(sum * uint32(1486364155))   // uint32 乗算は 2^32 で wrap
	sum := crc32.ChecksumIEEE([]byte(schema + "\x00" + "schema_migrations" + "\x00" + dbName))
	migrateKey := uint32(sum) * uint32(1486364155)
	if uint64(migrateKey) >= uint64(advisoryLockKey) {
		t.Fatalf("migrate lock key %d must be below fixed key %d", migrateKey, advisoryLockKey)
	}
}

// testDBURL mirrors the DSN testutil.OpenTestDB builds (testDSN) as a URL so
// Acquire can open a real connection to the package-isolated test database.
func testDBURL(t *testing.T) string {
	t.Helper()
	user := testutil.EnvOrDefault("TEST_DB_USER", "mk")
	pass := testutil.EnvOrDefault("TEST_DB_PASS", "mk")
	host := testutil.EnvOrDefault("TEST_DB_HOST", "localhost")
	port := testutil.EnvOrDefault("TEST_DB_PORT", "5432")
	name := testutil.EnvOrDefault("TEST_DB_NAME", "misskey_test")
	sslmode := testutil.EnvOrDefault("TEST_DB_SSLMODE", "disable")
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=%s",
		url.QueryEscape(user), url.QueryEscape(pass), host, port, name, sslmode)
}

// TestAcquireRealPostgresLockSemantics は隔離されたテストDBが使える環境でのみ、
// pg_try_advisory_lock の true/false を実DB相手に確認する統合テスト。
func TestAcquireRealPostgresLockSemantics(t *testing.T) {
	db, err := testutil.OpenTestDB()
	if err != nil {
		t.Skipf("isolated PostgreSQL unavailable: %v", err)
	}
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer func() { _ = sqlDB.Close() }()

	first, err := Acquire(context.Background(), testDBURL(t))
	require.NoError(t, err)

	second, err := Acquire(context.Background(), testDBURL(t))
	require.ErrorIs(t, err, ErrLocked)
	require.Nil(t, second)

	require.NoError(t, first.Release(context.Background()))

	third, err := Acquire(context.Background(), testDBURL(t))
	require.NoError(t, err)
	require.NoError(t, third.Release(context.Background()))
}
