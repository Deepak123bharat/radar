package timeline

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

var (
	postgresReconnectInitialInterval = 5 * time.Second
	postgresReconnectMaxInterval     = time.Minute
)

// maxDeletedWhileUnavailable bounds how many deletes an outage remembers. Past
// it, a resource deleted during the outage and later recreated under the same
// name may not have its creation recorded.
const maxDeletedWhileUnavailable = 100_000

var (
	deletedWhileUnavailableMu       sync.Mutex
	deletedWhileUnavailable         = map[string]struct{}{}
	deletedWhileUnavailableOverflow bool
)

var (
	// Guards the worker's lifetime. ResetStore retires the current worker before
	// a new InitStore can start one, so a context switch cannot have the
	// previous context's connection installed over the new one.
	postgresReconnectMu   sync.Mutex
	postgresReconnectQuit chan struct{}

	// Why no store is installed, for diagnostics. Empty once one is.
	postgresUnavailableReason atomic.Value // string

	// Test hook: replaced to drive the worker without a real database.
	postgresDialForTest func(StoreConfig) (*PostgresStore, error)
)

// dialPostgres opens a connection without publishing it, so a caller that has
// lost its claim can close the result instead of installing it.
func dialPostgres(cfg StoreConfig) (*PostgresStore, error) {
	if postgresDialForTest != nil {
		return postgresDialForTest(cfg)
	}
	return NewPostgresStore(cfg.DSN)
}

// recordPostgresUnavailable notes why the timeline has no store.
func recordPostgresUnavailable(err error) {
	postgresUnavailableReason.Store(err.Error())
	log.Printf("[timeline] PostgreSQL timeline unavailable, cluster views are unaffected: %v", err)
}

// recordWorkerUnavailable records a worker's failed attempt. A retired worker
// must not overwrite the reason: its failure describes a connection nobody is
// waiting on any more. The ownership check and the write share the lock that
// stopPostgresReconnect takes, so a reset cannot land between them.
func recordWorkerUnavailable(err error, quit chan struct{}) {
	postgresReconnectMu.Lock()
	defer postgresReconnectMu.Unlock()
	if postgresReconnectQuit != quit {
		return
	}
	recordPostgresUnavailable(err)
}

// installPostgresStore publishes an opened store. The first attempt and every
// retry go through here, so the retention loop, the observation start and the
// log line cannot drift between the two paths.
func installPostgresStore(store *PostgresStore, cfg StoreConfig) {
	// Clear the marks of resources deleted during the outage before the store
	// takes events, then once more after: a delete that lands between the two
	// went to the list, not to the store.
	forgetDeletedWhileUnavailable(store)
	// Observation starts when events begin landing. Marking it at process start
	// would claim coverage for the window the store was down. It is set before
	// the store is published so no reader sees a store without its epoch.
	observationStartNanos.Store(time.Now().UnixNano())
	setGlobalStore(store)
	forgetDeletedWhileUnavailable(store)
	postgresUnavailableReason.Store("")
	if cfg.RetentionAge > 0 {
		store.StartCleanupLoop(cfg.RetentionAge, time.Hour, 0)
		log.Printf("Initialized PostgreSQL timeline store (retention: %s)", cfg.RetentionAge)
	} else {
		log.Printf("Initialized PostgreSQL timeline store (retention: disabled - events table will grow unbounded)")
	}
}

// openPostgresStore dials and installs in one step, for the first attempt where
// no other worker is competing for the claim.
func openPostgresStore(cfg StoreConfig) bool {
	store, err := dialPostgres(cfg)
	if err != nil {
		recordPostgresUnavailable(err)
		return false
	}
	installPostgresStore(store, cfg)
	return true
}

// startPostgresReconnect retries until the database answers or the store is
// reset. Without it the timeline stays down for the life of the process, since
// nothing else reopens it.
func startPostgresReconnect(cfg StoreConfig) {
	postgresReconnectMu.Lock()
	defer postgresReconnectMu.Unlock()
	if postgresReconnectQuit != nil {
		return
	}
	quit := make(chan struct{})
	postgresReconnectQuit = quit
	// Captured under the lock: the worker must not read these again once it is
	// running, or a caller changing them races the loop.
	interval, maxInterval := postgresReconnectInitialInterval, postgresReconnectMaxInterval
	dial := dialPostgres

	go func() {
		for {
			select {
			case <-quit:
				return
			case <-time.After(interval):
			}

			store, err := dial(cfg)
			if err != nil {
				recordWorkerUnavailable(err, quit)
				interval = min(interval*2, maxInterval)
				continue
			}
			// Claim and install under one lock. A ResetStore landing between the
			// dial and the install has already retired this worker, and
			// publishing here would undo the reset.
			postgresReconnectMu.Lock()
			if postgresReconnectQuit != quit {
				postgresReconnectMu.Unlock()
				if closeErr := store.Close(); closeErr != nil {
					log.Printf("[timeline] closing a superseded PostgreSQL connection: %v", closeErr)
				}
				return
			}
			installPostgresStore(store, cfg)
			postgresReconnectQuit = nil
			postgresReconnectMu.Unlock()
			return
		}
	}()
}

// stopPostgresReconnect retires the worker. It claims and installs under the
// lock this takes, so once this returns no store can appear from it.
func stopPostgresReconnect() {
	postgresReconnectMu.Lock()
	defer postgresReconnectMu.Unlock()
	if postgresReconnectQuit != nil {
		close(postgresReconnectQuit)
		postgresReconnectQuit = nil
	}
	postgresUnavailableReason.Store("")
	takeDeletedWhileUnavailable()
}

// TimelineUnavailableReason reports why no timeline store is installed, or an
// empty string when one is.
func TimelineUnavailableReason() string {
	reason, _ := postgresUnavailableReason.Load().(string)
	return reason
}

// Unavailable reports why the timeline cannot record or serve history right
// now, or nil when it can. A PostgreSQL store is pinged: a database that goes
// away after a good start leaves the store in place, and only a round trip
// shows the outage. Any ping failure counts, a refused connection or a
// database that stops answering alike.
func Unavailable(ctx context.Context) error {
	store := GetStore()
	if store == nil {
		if reason := TimelineUnavailableReason(); reason != "" {
			return errors.New(reason)
		}
		return nil
	}
	pg, ok := store.(*PostgresStore)
	if !ok {
		return nil
	}
	err := pg.Ping(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("PostgreSQL did not answer in time: %w", err)
	}
	return err
}

// NoteDeletedWhileUnavailable remembers a resource deleted while no store was
// installed. Its seen mark is still in the database, and once the store comes
// back the mark would make a new resource with the same name look already seen,
// so its creation would be dropped. The reconnected store clears these marks.
func NoteDeletedWhileUnavailable(clusterContext, group, kind, namespace, name string) {
	deletedWhileUnavailableMu.Lock()
	if len(deletedWhileUnavailable) >= maxDeletedWhileUnavailable {
		deletedWhileUnavailableOverflow = true
	} else {
		deletedWhileUnavailable[SeenResourceKey(clusterContext, group, kind, namespace, name)] = struct{}{}
	}
	deletedWhileUnavailableMu.Unlock()

	// The caller saw no store, but one may have been installed since, and its
	// last pass over this list may already have run. installPostgresStore
	// publishes the store before that pass, so either the pass sees this entry
	// or this check sees the store.
	if pg, ok := GetStore().(*PostgresStore); ok {
		forgetDeletedWhileUnavailable(pg)
	}
}

// DeletedWhileUnavailableCountForTest reports how many deletes are waiting to
// have their seen marks cleared.
func DeletedWhileUnavailableCountForTest() int {
	deletedWhileUnavailableMu.Lock()
	defer deletedWhileUnavailableMu.Unlock()
	return len(deletedWhileUnavailable)
}

// takeDeletedWhileUnavailable empties the list and reports whether it overflowed.
func takeDeletedWhileUnavailable() ([]string, bool) {
	deletedWhileUnavailableMu.Lock()
	defer deletedWhileUnavailableMu.Unlock()
	keys := make([]string, 0, len(deletedWhileUnavailable))
	for k := range deletedWhileUnavailable {
		keys = append(keys, k)
	}
	overflow := deletedWhileUnavailableOverflow
	deletedWhileUnavailable = map[string]struct{}{}
	deletedWhileUnavailableOverflow = false
	return keys, overflow
}

func forgetDeletedWhileUnavailable(store *PostgresStore) {
	keys, overflow := takeDeletedWhileUnavailable()
	if overflow {
		log.Printf("[timeline] more than %d resources were deleted while PostgreSQL was unreachable; "+
			"one of the others recreated under the same name may not have its creation recorded", maxDeletedWhileUnavailable)
	}
	if len(keys) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), postgresOperationTimeout)
	defer cancel()
	if err := store.forgetSeen(ctx, keys); err != nil {
		log.Printf("[timeline] failed to clear seen marks for %d resource(s) deleted while PostgreSQL was unreachable: %v", len(keys), err)
	}
}
