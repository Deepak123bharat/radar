package timeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// dbProxy forwards TCP to the test database and can fail it two ways: down()
// cuts open connections and refuses new ones; stall() keeps every socket open
// and forwards nothing, like a database that stops answering.
type dbProxy struct {
	t      *testing.T
	target string
	addr   string

	mu      sync.Mutex
	ln      net.Listener
	conns   []net.Conn
	stalled bool
}

func startDBProxy(t *testing.T, target string) *dbProxy {
	t.Helper()
	p := &dbProxy{t: t, target: target}
	p.up("127.0.0.1:0")
	t.Cleanup(p.down)
	return p
}

func (p *dbProxy) up(addr string) {
	p.t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		p.t.Fatalf("proxy listen: %v", err)
	}
	p.mu.Lock()
	p.ln, p.addr = ln, ln.Addr().String()
	p.mu.Unlock()
	go func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			stalled := p.stalled
			p.conns = append(p.conns, client)
			p.mu.Unlock()
			if stalled {
				continue
			}
			server, err := net.Dial("tcp", p.target)
			if err != nil {
				client.Close()
				continue
			}
			p.mu.Lock()
			p.conns = append(p.conns, server)
			p.mu.Unlock()
			go func() { _, _ = io.Copy(server, client); server.Close() }()
			go func() {
				_, _ = io.Copy(client, server)
				p.mu.Lock()
				defer p.mu.Unlock()
				if !p.stalled {
					client.Close()
				}
			}()
		}
	}()
}

// stall cuts the link to the database but leaves the client side of every
// connection open, so reads block instead of failing.
func (p *dbProxy) stall() {
	p.mu.Lock()
	p.stalled = true
	conns := p.conns
	p.mu.Unlock()
	for _, c := range conns {
		if c.RemoteAddr().String() == p.target {
			c.Close()
		}
	}
}

func (p *dbProxy) down() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ln != nil {
		p.ln.Close()
		p.ln = nil
	}
	for _, c := range p.conns {
		c.Close()
	}
	p.conns = nil
}

// A database that goes away after a good start must read as unreachable, both
// to queries and to the reachability check diagnostics uses, and the same store
// must work again once the database is back, without being reopened.
func TestPostgresStoreReportsAnOutageAndRecovers(t *testing.T) {
	dsn, err := url.Parse(testPostgresDSN(t))
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	proxy := startDBProxy(t, dsn.Host)
	dsn.Host = proxy.addr
	q := dsn.Query()
	q.Set("connect_timeout", "2")
	dsn.RawQuery = q.Encode()

	store, err := NewPostgresStore(dsn.String())
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := t.Context()
	event := TimelineEvent{ID: "before", Timestamp: time.Now().UTC(), Source: SourceInformer, Kind: "Deployment", Namespace: "default", Name: "web", EventType: EventTypeAdd}
	if err := store.Append(ctx, event); err != nil {
		t.Fatalf("Append before the outage: %v", err)
	}

	proxy.down()

	_, err = store.Query(ctx, QueryOptions{Limit: 10})
	if err == nil {
		t.Fatal("Query succeeded with the database gone")
	}
	if !DatabaseUnreachable(err) {
		t.Fatalf("Query error %q not classed as unreachable; the page would show a generic failure", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = store.Ping(pingCtx)
	cancel()
	if !DatabaseUnreachable(err) {
		t.Fatalf("Ping error %v not classed as unreachable; diagnostics would report a healthy, empty timeline", err)
	}

	proxy.up(proxy.addr)

	deadline := time.Now().Add(10 * time.Second)
	for {
		events, err := store.Query(ctx, QueryOptions{Limit: 10})
		if err == nil {
			if len(events) != 1 || events[0].ID != "before" {
				t.Fatalf("after recovery got %d events, want the one written before the outage", len(events))
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("store did not recover after the database came back: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := store.Append(ctx, TimelineEvent{ID: "after", Timestamp: time.Now().UTC(), Source: SourceInformer, Kind: "Deployment", Namespace: "default", Name: "web", EventType: EventTypeUpdate}); err != nil {
		t.Fatalf("Append after recovery: %v", err)
	}
}

// A database that stops answering, rather than refusing, must still fail the
// reachability check within its budget. Diagnostics relies on that to report the
// outage instead of waiting out every stats query and showing an empty store.
func TestPostgresStorePingFailsWhenTheDatabaseStopsAnswering(t *testing.T) {
	dsn, err := url.Parse(testPostgresDSN(t))
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	proxy := startDBProxy(t, dsn.Host)
	dsn.Host = proxy.addr

	store, err := NewPostgresStore(dsn.String())
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	proxy.stall()

	const budget = 2 * time.Second
	ctx, cancel := context.WithTimeout(t.Context(), budget)
	defer cancel()
	start := time.Now()
	err = store.Ping(ctx)
	if err == nil {
		t.Fatal("Ping succeeded against a database that is not answering")
	}
	if elapsed := time.Since(start); elapsed > budget+2*time.Second {
		t.Fatalf("Ping took %s, past its %s budget; diagnostics would stall", elapsed, budget)
	}
}

func TestDatabaseUnreachableIgnoresOtherErrors(t *testing.T) {
	if DatabaseUnreachable(errors.New("query events: syntax error")) {
		t.Fatal("a rejected query was classed as an unreachable database")
	}
	if DatabaseUnreachable(nil) {
		t.Fatal("nil classed as unreachable")
	}
	if DatabaseUnreachable(&pgconn.PgError{Code: "42601"}) {
		t.Fatal("a syntax error from the server was classed as an unreachable database")
	}
	if !DatabaseUnreachable(fmt.Errorf("query events: %w", &pgconn.PgError{Code: "57P01"})) {
		t.Fatal("the server terminating the connection was not classed as unreachable")
	}
}

// A resource deleted while no store is installed keeps its seen mark in the
// database. Once the store reconnects that mark would make a new resource with
// the same name look already seen, and its creation would be dropped. The
// reconnect has to clear it, in memory and in the database.
func TestReconnectClearsMarksOfResourcesDeletedDuringTheOutage(t *testing.T) {
	ResetStore()
	t.Cleanup(ResetStore)
	dsn := testPostgresDSN(t)

	before, err := NewPostgresStore(dsn)
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	before.MarkResourceSeen("ctx", "apps", "Deployment", "shop", "web")
	before.MarkResourceSeen("ctx", "apps", "Deployment", "shop", "api")
	if err := before.Close(); err != nil { // Close drains the queued marks.
		t.Fatalf("Close: %v", err)
	}

	NoteDeletedWhileUnavailable("ctx", "apps", "Deployment", "shop", "web")

	reconnected, err := NewPostgresStore(dsn)
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	if !reconnected.IsResourceSeen("ctx", "apps", "Deployment", "shop", "web") {
		t.Fatal("setup: the stale mark was not loaded from the database")
	}
	installPostgresStore(reconnected, StoreConfig{})

	if reconnected.IsResourceSeen("ctx", "apps", "Deployment", "shop", "web") {
		t.Fatal("the deleted resource is still marked seen; recreating it would not be recorded")
	}
	if !reconnected.IsResourceSeen("ctx", "apps", "Deployment", "shop", "api") {
		t.Fatal("a resource that was not deleted lost its mark")
	}

	ResetStore()
	fresh, err := NewPostgresStore(dsn)
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	t.Cleanup(func() { _ = fresh.Close() })
	if fresh.IsResourceSeen("ctx", "apps", "Deployment", "shop", "web") {
		t.Fatal("the mark is gone in memory but still in the database; the next restart would bring it back")
	}
}

// Deletes from a cluster context that has been switched away from must not be
// applied to the next one's store.
func TestResetStoreDropsDeletesNotedDuringTheOutage(t *testing.T) {
	ResetStore()
	t.Cleanup(ResetStore)
	NoteDeletedWhileUnavailable("old", "apps", "Deployment", "shop", "web")
	ResetStore()
	if keys, _ := takeDeletedWhileUnavailable(); len(keys) != 0 {
		t.Fatalf("ResetStore kept %d delete(s) from the previous context", len(keys))
	}
}

// A database that stops answering times a query out rather than failing its
// connection, so the query error alone does not say "down". The availability
// check that follows a timeout has to.
func TestStalledQueryTimesOutAndTheCheckReportsTheOutage(t *testing.T) {
	ResetStore()
	t.Cleanup(ResetStore)
	dsn, err := url.Parse(testPostgresDSN(t))
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	proxy := startDBProxy(t, dsn.Host)
	dsn.Host = proxy.addr
	store, err := NewPostgresStore(dsn.String())
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	installPostgresStore(store, StoreConfig{})

	proxy.stall()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	_, err = store.Query(ctx, QueryOptions{Limit: 10})
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stalled query returned %v, want a timeout", err)
	}
	ctx, cancel = context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if Unavailable(ctx) == nil {
		t.Fatal("availability check passed with the database not answering")
	}
}
