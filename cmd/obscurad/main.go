// Command obscurad is the obscura server: a single process containing the HTTP
// server, the database and (later) media processing.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/rverst/obscura/internal/api"
	"github.com/rverst/obscura/internal/auth"
	"github.com/rverst/obscura/internal/blob"
	"github.com/rverst/obscura/internal/library"
	"github.com/rverst/obscura/internal/media"
	"github.com/rverst/obscura/internal/store"
	"github.com/rverst/obscura/internal/upload"
	"github.com/rverst/obscura/web"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "obscurad:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		addr        = flag.String("addr", "127.0.0.1:8080", "address to listen on")
		dataDir     = flag.String("data", "./data", "directory for databases and blobs")
		sessionTTL  = flag.Duration("session-ttl", 12*time.Hour, "idle time after which a session expires")
		dev         = flag.Bool("dev", false, "development mode: cookie without the Secure flag (http://localhost only)")
		showVersion = flag.Bool("version", false, "print the version and exit")
		maxUpload   = flag.Int64("max-upload", 8<<30, "largest single file accepted, in bytes")
		uploadTTL   = flag.Duration("upload-ttl", time.Hour, "how long an untouched upload survives before it is discarded")
		concurrency = flag.Int("workers", library.DefaultConcurrency, "how many files are processed at once")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("obscurad", version)
		return nil
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Must happen before anything creates a file, in particular before SQLite
	// opens a database.
	tightenUmask()

	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(*dataDir, "blobs"), 0o700); err != nil {
		return fmt.Errorf("create blob directory: %w", err)
	}

	// auth.db is unencrypted and opened at startup -- it has to be readable
	// before any key exists. photos.db, by contrast, is only opened on login,
	// because before that there is no key.
	authDB, err := store.OpenPlain(filepath.Join(*dataDir, "auth.db"))
	if err != nil {
		return err
	}
	defer authDB.Close()
	if err := store.Migrate(authDB, store.SetAuth); err != nil {
		return err
	}

	slots := auth.NewStore(authDB)
	sessions := auth.NewManager(*dataDir, *sessionTTL)
	defer sessions.CloseAll()

	blobs, err := blob.NewStore(filepath.Join(*dataDir, "blobs"))
	if err != nil {
		return err
	}
	// Fail here rather than on the first upload: a missing vipsthumbnail is a
	// deployment error, and finding out when a photo is already half uploaded
	// is a bad time to learn about it.
	thumbs, err := media.NewThumbnailer()
	if err != nil {
		return err
	}
	lib := library.New(blobs, thumbs, *concurrency)
	uploads := upload.NewManager(blobs, *maxUpload, *uploadTTL)
	// In-flight uploads hold partial blobs; shutting down without discarding
	// them would leave .part files behind that nothing ever reclaims.
	defer uploads.CloseAll()

	initialized, err := slots.IsInitialized(context.Background())
	if err != nil {
		return err
	}

	assets, err := web.Assets()
	if err != nil {
		return err
	}

	srv := &api.Server{
		Assets:      assets,
		AssetsBuilt: web.Built(),
		Slots:       slots,
		Sessions:    sessions,
		Library:     lib,
		Uploads:     uploads,
		Blobs:       blobs,
		Log:         log,
		Secure:      !*dev,
	}

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Collect expired sessions. Without this a forgotten browser tab keeps the
	// vault open indefinitely -- and with it the DEK in memory.
	stopSweeper := make(chan struct{})
	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if n := sessions.Sweep(); n > 0 {
					log.Info("removed expired sessions", "count", n)
				}
				if n := uploads.Sweep(); n > 0 {
					log.Info("discarded abandoned uploads", "count", n)
				}
			case <-stopSweeper:
				return
			}
		}
	}()

	log.Info("obscura starting",
		"version", version,
		"addr", *addr,
		"data", *dataDir,
		"initialized", initialized,
		"cookie_secure", !*dev,
		"workers", *concurrency,
		"max_upload", *maxUpload,
		"frontend", web.Built())
	if !web.Built() {
		log.Warn("no frontend embedded -- API only; run `npm run build` in web/ or use the Docker image")
	}
	if !initialized {
		log.Info(`not initialized yet -- POST /api/setup with {"username","password"}`)
	}
	if *dev {
		log.Warn("development mode: cookie without Secure flag. Never run like this in production.")
	}

	errCh := make(chan error, 1)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case s := <-sig:
		log.Info("shutting down", "signal", s.String())
	}

	close(stopSweeper)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(ctx); err != nil {
		log.Warn("unclean shutdown", "err", err)
	}
	// CloseAll runs via defer -- closes photos.db and zeroes the DEK.
	return nil
}
