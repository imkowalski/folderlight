package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
func run() error {
	configFile := flag.String("config", "config.json", "JSON configuration file")
	scanOnly := flag.Bool("scan", false, "scan library and exit")
	force := flag.Bool("force", false, "re-read all metadata during startup scan")
	version := flag.Bool("version", false, "print version")
	flag.Parse()
	if *version {
		fmt.Println("Folderlight 1.1.1")
		return nil
	}
	cfg, err := loadConfig(*configFile)
	if err != nil {
		return err
	}
	// Soft GC target; decode pixel/worker limits remain the hard allocation controls.
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(256 << 20)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	store, err := openStore(cfg.State)
	if err != nil {
		return err
	}
	defer store.db.Close()
	indexer := &Indexer{store: store, config: cfg}
	indexer.start(ctx, *force)
	if *scanOnly {
		indexer.wg.Wait()
		st := indexer.snapshot()
		fmt.Printf("Visited: %d; updated: %d; errors: %d\n", st.Visited, st.Updated, st.Errors)
		if st.Errors > 0 {
			return errors.New(st.Message)
		}
		return nil
	}
	cache, err := newCache(ctx, cfg)
	if err != nil {
		cancel()
		indexer.wg.Wait()
		return err
	}
	defer cache.close()
	app := &App{store: store, indexer: indexer, cache: cache, ctx: ctx}
	server := &http.Server{Addr: cfg.Listen, Handler: app.handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10, BaseContext: nil}
	serverDone := make(chan error, 1)
	go func() {
		log.Printf("Folderlight listening at http://%s", cfg.Listen)
		serverDone <- server.ListenAndServe()
	}()
	select {
	case err = <-serverDone:
		cancel()
	case <-ctx.Done():
	}
	shutdownCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	_ = server.Shutdown(shutdownCtx)
	cancel()
	indexer.wg.Wait()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
