package operatorservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"regexp"
	"sync"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

type LedgerDatabase interface {
	Execute(context.Context, turso.Statement) (*turso.Result, error)
	Batch(context.Context, []turso.Statement) ([]turso.Result, error)
}

// Ledger uses only one fresh recipient-key generation's quota namespace. The
// snapshot adapter excludes precisely this prefix, never normal quotas or other
// operator generations. No schema or permanent auxiliary table is introduced.
type Ledger struct {
	DB     LedgerDatabase
	Prefix string
}

var ErrOperatorLease = errors.New("operator request replayed or another operation is active")
var ledgerPrefix = regexp.MustCompile(`^operatorjob:[a-f0-9]{64}:$`)
var ledgerNonce = regexp.MustCompile(`^[A-Za-z0-9_-]{32,128}$`)

const operatorLeaseSeconds int64 = 300

// Acquire runs only after request signature/audience/expiry verification. Its
// lease and nonce form one native transaction. A failed guard rolls back both.
// Call release only after a known-complete command; uncertain apply errors must
// retain their lease until expiry. Nonce rows remain until deployment retirement.
func (l Ledger) Acquire(ctx context.Context, nonce string, expiresAt int64, now time.Time) (func(), error) {
	if l.DB == nil || !ledgerPrefix.MatchString(l.Prefix) || !ledgerNonce.MatchString(nonce) || expiresAt <= now.Unix() || expiresAt > now.Unix()+900 {
		return nil, errors.New("invalid operator ledger request")
	}
	sum := sha256.Sum256([]byte(nonce))
	key := l.Prefix + "nonce:" + hex.EncodeToString(sum[:])
	leaseKey := l.Prefix + "lease"
	guardKey := l.Prefix + "guard:" + hex.EncodeToString(sum[:])
	until := now.Unix() + operatorLeaseSeconds
	result, err := l.DB.Batch(ctx, []turso.Statement{
		{SQL: "INSERT INTO mcp_rate_limits(bucket_key,window_start,count) VALUES(?,?,?) ON CONFLICT(bucket_key,window_start) DO UPDATE SET count=excluded.count WHERE mcp_rate_limits.count<=? RETURNING count", Args: []turso.Value{turso.Text(leaseKey), turso.Integer(math.MaxInt64), turso.Integer(until), turso.Integer(now.Unix())}, WantRows: true},
		{SQL: "INSERT INTO mcp_rate_limits(bucket_key,window_start,count) SELECT ?,?,1 WHERE changes()=1 AND NOT EXISTS(SELECT 1 FROM mcp_rate_limits WHERE bucket_key=?) RETURNING bucket_key", Args: []turso.Value{turso.Text(key), turso.Integer(expiresAt), turso.Text(key)}, WantRows: true},
		{SQL: "INSERT INTO mcp_rate_limits(bucket_key,window_start,count) VALUES(?,?,CASE WHEN changes()=1 THEN 1 ELSE NULL END)", Args: []turso.Value{turso.Text(guardKey), turso.Integer(expiresAt)}},
		{SQL: "DELETE FROM mcp_rate_limits WHERE bucket_key=? AND window_start=?", Args: []turso.Value{turso.Text(guardKey), turso.Integer(expiresAt)}},
	})
	if err != nil {
		return nil, ErrOperatorLease
	}
	if len(result) != 4 || len(result[0].Rows) != 1 || len(result[1].Rows) != 1 {
		return nil, ErrOperatorLease
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			if ctx.Err() != nil {
				return
			}
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _ = l.DB.Execute(cleanup, turso.Statement{SQL: "DELETE FROM mcp_rate_limits WHERE bucket_key=? AND window_start=? AND count=?", Args: []turso.Value{turso.Text(leaseKey), turso.Integer(math.MaxInt64), turso.Integer(until)}})
		})
	}, nil
}
