// Command portholed is the Porthole daemon: it wraps the `container` CLI and
// serves the read API (and later the SPA) on loopback. Phase 0 is read-only.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/porthole/porthole/engine"
	"github.com/porthole/porthole/httpapi"
	"github.com/porthole/porthole/idlock"
	"github.com/porthole/porthole/reconcile"
	"github.com/porthole/porthole/stacks"
	"github.com/porthole/porthole/supervisor"
)

// version is overridden at build time via `-ldflags "-X main.version=<v>"`
// (see the Makefile). It defaults to "dev" for plain `go build`/`go run`.
var version = "dev"

// minContainerMajor is the lowest `container` major version Porthole targets. An
// earlier CLI only triggers a warning (some features may misbehave) — never a
// hard block (README: "targets 1.0+, warns on earlier").
const minContainerMajor = 1

func main() {
	var (
		port        = flag.Int("port", 9191, "loopback port to listen on")
		bin         = flag.String("container-bin", "container", "path to the container binary")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("porthole %s\n", version)
		return
	}

	// Bind-guard, Phase 0 form: loopback only, full stop. Non-loopback binding is
	// refused until auth lands (spec §5.2a / §8 gap 5). We bind the literal
	// 127.0.0.1 rather than a hostname so there is no ambiguity.
	addr := fmt.Sprintf("127.0.0.1:%d", *port)

	hosts := []string{
		fmt.Sprintf("127.0.0.1:%d", *port),
		fmt.Sprintf("localhost:%d", *port),
	}
	origins := []string{
		fmt.Sprintf("http://127.0.0.1:%d", *port),
		fmt.Sprintf("http://localhost:%d", *port),
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	eng := engine.NewCLIEngine(*bin)

	// Compatibility gate (warn, don't block): if the CLI reports a major version
	// below our target, note it. Skipped silently when the daemon is down (we
	// can't read a version then) — the bootstrap banner covers that case.
	if v, err := eng.SystemVersion(ctx); err == nil {
		if cli := engine.CLIVersion(v); cli != "" {
			if m := majorVersion(cli); m >= 0 && m < minContainerMajor {
				log.Printf("portholed: container %s detected — container %d.0+ recommended; some features may misbehave on earlier builds", cli, minContainerMajor)
			}
		}
	}

	// The reconcile hub turns the pull-only CLI into a push stream for /api/stream.
	hub := reconcile.NewHub(eng, 2*time.Second, 30*time.Second)

	// Supervision (Phase 3): a persistent store, a per-id lock shared with the
	// HTTP mutation handlers, and the supervisor consuming the reconcile poll.
	locks := idlock.New()
	store := openStore()
	supCfg := supervisor.DefaultConfig()
	// Optional ops override of the give-up ceiling (spec §6 max-attempts).
	if v := os.Getenv("PORTHOLE_MAX_RESTARTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			supCfg.MaxAttempts = n
		}
	}
	sup := supervisor.New(store, eng, hub, locks, supCfg, log.Default())
	hub.SetOnCycle(sup.OnCycle)     // must be set before Run
	hub.SetOnRemoved(sup.OnRemoved) // prune policy rows when a container is removed

	// Stacks (Phase 4): shares the per-id lock + the supervision SQLite db, and
	// drives the runtime through the same engine.
	stackMgr := stacks.NewManager(openStackStore(store), eng, locks)

	go hub.Run(ctx)

	// Boot reconciliation: restart always/unless-stopped containers found stopped,
	// staggered. Delayed slightly so the apiserver is reachable first.
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
		if err := sup.Reconcile(ctx); err != nil {
			log.Printf("portholed: boot reconcile: %v", err)
		}
	}()

	srv := httpapi.New(eng, httpapi.Config{
		AllowedHosts:   hosts,
		AllowedOrigins: origins,
		Hub:            hub,
		Locks:          locks,
		Supervision:    sup,
		LogWatcher:     hub,
		Stacks:         stackMgr,
		Creator:        eng, // *CLIEngine: progress-streaming run + image list (Phase 5)
		Resources:      eng, // *CLIEngine: list/remove/prune/pull (Phase 6)
		Registry:       eng, // *CLIEngine: registry login/list/logout (Phase 7)
	})

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			log.Fatalf("portholed: %s is already in use — another portholed may be running; stop it or pass -port", addr)
		}
		log.Fatalf("portholed: cannot bind %s: %v", addr, err)
	}
	log.Printf("portholed %s listening on http://%s (loopback only)", version, addr)

	go func() {
		<-ctx.Done()
		_ = httpServer.Close()
	}()
	if err := httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

// majorVersion parses the leading integer of a semver ("1.0.0" -> 1), or -1 if
// it can't (avoids a semver dependency for the lightweight compatibility gate).
func majorVersion(semver string) int {
	s := semver
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return -1
	}
	return n
}

// openStore opens the on-disk policy store, falling back to in-memory if the
// SQLite db can't be opened (supervision still works for the session). The
// parent data dir (~/Library/Application Support/porthole) is created by
// OpenSQLite (os.MkdirAll) if absent, so a fresh machine starts cleanly.
func openStore() supervisor.Store {
	path, err := supervisor.DefaultDBPath()
	if err == nil {
		if s, e := supervisor.OpenSQLite(path); e == nil {
			log.Printf("portholed: policy store at %s", path)
			return s
		} else {
			log.Printf("portholed: sqlite open failed (%v); using in-memory policy store", e)
		}
	}
	return supervisor.NewMemStore()
}

// openStackStore backs the Stacks definitions. It shares the supervisor's SQLite
// handle (one connection pool / WAL writer) when available, falling back to an
// in-memory store otherwise so Stacks still works for the session.
func openStackStore(store supervisor.Store) stacks.Store {
	if sq, ok := store.(*supervisor.SQLiteStore); ok {
		if s, err := stacks.NewSQLiteStore(sq.DB()); err == nil {
			return s
		} else {
			log.Printf("portholed: stacks sqlite init failed (%v); using in-memory stack store", err)
		}
	}
	return stacks.NewMemStore()
}
