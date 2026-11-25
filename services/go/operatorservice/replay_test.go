package operatorservice

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/internal/sqlitetest"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

func ledgerFixture(t *testing.T) (Ledger, *sqlitetest.DB) {
	t.Helper()
	db := sqlitetest.New(t, `CREATE TABLE mcp_rate_limits(bucket_key TEXT NOT NULL,window_start INTEGER NOT NULL,count INTEGER NOT NULL,PRIMARY KEY(bucket_key,window_start));`)
	return Ledger{DB: db, Prefix: "operatorjob:" + strings.Repeat("a", 64) + ":"}, db
}
func TestLedgerReplayAndBusyRollBackWithoutChangingOrdinaryQuota(t *testing.T) {
	l, db := ledgerFixture(t)
	ctx := context.Background()
	now := time.Unix(2000, 0)
	if _, err := db.Execute(ctx, turso.Statement{SQL: "INSERT INTO mcp_rate_limits VALUES('ordinary',100,7)"}); err != nil {
		t.Fatal(err)
	}
	release, err := l.Acquire(ctx, strings.Repeat("a", 32), 2100, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.Acquire(ctx, strings.Repeat("b", 32), 2100, now); err == nil {
		t.Fatal("concurrent lease accepted")
	}
	release()
	// Busy did not consume b, while successful a remains one-use after release.
	if _, err = l.Acquire(ctx, strings.Repeat("a", 32), 2100, now); err == nil {
		t.Fatal("replayed nonce accepted")
	}
	release, err = l.Acquire(ctx, strings.Repeat("b", 32), 2100, now)
	if err != nil {
		t.Fatal("busy attempt consumed its nonce", err)
	}
	release()
	r, err := db.Execute(ctx, turso.Statement{SQL: "SELECT count FROM mcp_rate_limits WHERE bucket_key='ordinary'", WantRows: true})
	if err != nil || fmt.Sprint(r.Rows[0][0].Value) != "7" {
		t.Fatal("ordinary quotas changed", err)
	}
	r, err = db.Execute(ctx, turso.Statement{SQL: "SELECT COUNT(*) FROM mcp_rate_limits WHERE bucket_key LIKE ?", Args: []turso.Value{turso.Text(l.Prefix + "guard:%")}, WantRows: true})
	if err != nil || fmt.Sprint(r.Rows[0][0].Value) != "0" {
		t.Fatal("temporary guard leaked", err)
	}
}
func TestLedgerConcurrentAcquisitionAndFencedRelease(t *testing.T) {
	l, _ := ledgerFixture(t)
	ctx := context.Background()
	now := time.Unix(2000, 0)
	var wg sync.WaitGroup
	var mu sync.Mutex
	releases := []func(){}
	for _, nonce := range []string{strings.Repeat("a", 32), strings.Repeat("b", 32)} {
		wg.Add(1)
		go func(nonce string) {
			defer wg.Done()
			release, err := l.Acquire(ctx, nonce, 2100, now)
			if err == nil {
				mu.Lock()
				releases = append(releases, release)
				mu.Unlock()
			}
		}(nonce)
	}
	wg.Wait()
	if len(releases) != 1 {
		t.Fatal("lease failed to serialize", len(releases))
	}
	// A later generation can claim after expiry; the old owner cannot release it.
	release, err := l.Acquire(ctx, strings.Repeat("c", 32), 2500, time.Unix(2301, 0))
	if err != nil {
		t.Fatal(err)
	}
	releases[0]()
	if _, err = l.Acquire(ctx, strings.Repeat("d", 32), 2500, time.Unix(2302, 0)); err == nil {
		t.Fatal("stale release removed newer lease")
	}
	release()
}
func TestLedgerRejectsInvalidNamespaceExpiryAndPreservesCanceledLease(t *testing.T) {
	l, db := ledgerFixture(t)
	now := time.Unix(2000, 0)
	for _, prefix := range []string{"operatorjob:", "sync:account:", l.Prefix + "extra"} {
		bad := l
		bad.Prefix = prefix
		if _, err := bad.Acquire(context.Background(), strings.Repeat("a", 32), 2100, now); err == nil {
			t.Fatal("invalid prefix accepted")
		}
	}
	for _, expiry := range []int64{2000, 1999, 2901} {
		if _, err := l.Acquire(context.Background(), strings.Repeat("a", 32), expiry, now); err == nil {
			t.Fatal("invalid expiry accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	release, err := l.Acquire(ctx, strings.Repeat("a", 32), 2100, now)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	release()
	r, err := db.Execute(context.Background(), turso.Statement{SQL: "SELECT COUNT(*) FROM mcp_rate_limits", WantRows: true})
	if err != nil || fmt.Sprint(r.Rows[0][0].Value) != "2" {
		t.Fatal("canceled uncertain command prematurely released lease", err)
	}
}
