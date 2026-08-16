// Package migrationcompat serializes mk-go migration runs behind a fixed
// advisory lock that never collides with golang-migrate's own lock.
package migrationcompat

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ErrLocked is returned when another mk-go migration process already owns the
// fixed migration-wide advisory lock.
var ErrLocked = errors.New("another mk-go migration is already running")

// advisoryLockKey is the fixed positive advisory lock key that serializes
// mk-go migration runs.
//
// golang-migrate の postgres driver は自前の advisory lock キーを
// database.GenerateAdvisoryLockId (database/util.go) で導出する:
//
//	sum = crc32.ChecksumIEEE(schema \x00 table \x00 db)
//	key = sum * uint32(1486364155)   // uint32 乗算は 2^32 で wrap
//
// 結果は常に [0, math.MaxUint32] に収まる。固定キー 1<<32 + 1 を
// math.MaxUint32 より大きくすることで、migrate が取り得る全キー空間と必ず
// 排他になる。このキーは mk-go の migration 実行の直列化専用であり、
// golang-migrate の自前 crc32 lock を置き換えるものではない。
const advisoryLockKey int64 = 1<<32 + 1

// queryRower は advisory lock 文を流すための QueryRow だけの接続シーム。
// unit test が実DBなしで true / false / scan error を差し込める。
type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// lockConn is the subset of *pgx.Conn needed to hold and release an advisory
// lock and to run the detection-only migration preflight transaction.
//
// 接続をインターフェースにすることで、ネットワークなしの unit test で
// pg_try_advisory_lock / pg_advisory_unlock とクローズまで通せる。production
// では実体は常に *pgx.Conn であり、公開 API に影響しない。
type lockConn interface {
	queryRower
	// Begin opens the detection-only preflight transaction on the advisory-lock
	// connection. この transaction は行を一切変更しない。
	Begin(ctx context.Context) (pgx.Tx, error)
	Close(ctx context.Context) error
}

// connector opens the dedicated advisory-lock connection. テストは実DBに
// 依存せず全分岐を回すためこれを差し替える。
var connector = func(ctx context.Context, databaseURL string) (lockConn, error) {
	return pgx.Connect(ctx, databaseURL)
}

// Lock is a held advisory lock on the fixed mk-go migration key.
type Lock struct {
	conn lockConn
}

// Acquire takes the migration-wide advisory lock, failing fast when another
// mk-go migration process already owns it. The caller must hold the returned
// Lock until the migration finishes and then release it.
//
// databaseURL はパスワードを含むため、返す・記録するエラーには決して含めない。
func Acquire(ctx context.Context, databaseURL string) (*Lock, error) {
	conn, err := connector(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open migration lock connection: %w", err)
	}
	acquired, err := tryAcquire(ctx, conn, advisoryLockKey)
	if err != nil {
		_ = conn.Close(ctx)
		return nil, fmt.Errorf("acquire migration lock: %w", err)
	}
	if !acquired {
		_ = conn.Close(ctx)
		return nil, ErrLocked
	}
	return &Lock{conn: conn}, nil
}

// tryAcquire runs pg_try_advisory_lock for the fixed key and reports whether
// the lock was taken.
func tryAcquire(ctx context.Context, q queryRower, key int64) (bool, error) {
	var acquired bool
	err := q.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&acquired)
	return acquired, err
}

// Release unlocks the advisory lock and closes the dedicated connection.
//
// ロック解除に失敗しても接続は閉じる。接続が閉じると PostgreSQL は
// セッションレベルの advisory lock を自動解放するため、ロックは取り残されない。
func (l *Lock) Release(ctx context.Context) error {
	var released bool
	if err := l.conn.QueryRow(ctx, `SELECT pg_advisory_unlock($1)`, advisoryLockKey).Scan(&released); err != nil {
		_ = l.conn.Close(ctx)
		return fmt.Errorf("release migration lock: %w", err)
	}
	return l.conn.Close(ctx)
}
