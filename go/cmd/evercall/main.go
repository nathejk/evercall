// Command evercall is the Nathejk telephony integration service.
//
// Right now it is only the capture stage: a catch-all HTTP server that accepts
// a callback on any method and any path and dumps the full request — headers
// and body — to stdout. That output is how we discover the Evercall platform's
// actual webhook shape, which `.rules` marks as TBC.
//
// Logging whole raw bodies is deliberate here and must NOT carry over to the
// real ingest path: call data is personal data (see `.rules` → Coding
// conventions → Go).
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
	"strconv"
	"syscall"
	"time"

	"github.com/nathejk/evercall/internal/vcs"
)

type config struct {
	port         int
	maxBodyBytes int64
}

type application struct {
	config config
	logger *log.Logger
}

func main() {
	var cfg config

	flag.IntVar(&cfg.port, "port", envInt("PORT", 80), "HTTP server port")
	flag.Int64Var(&cfg.maxBodyBytes, "max-body", int64(envInt("MAX_BODY_BYTES", 1<<20)),
		"maximum request body size to read, in bytes")
	version := flag.Bool("version", false, "print build provenance and exit")
	flag.Parse()

	if *version {
		fmt.Println(vcs.String())
		return
	}

	app := &application{
		config: cfg,
		logger: log.New(os.Stdout, "", 0),
	}

	if err := app.serve(); err != nil {
		log.New(os.Stderr, "", log.LstdFlags).Fatalf("server error: %v", err)
	}
}

func (app *application) serve() error {
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", app.config.port),
		Handler: app.routes(),
		// No WriteTimeout/ReadTimeout shorter than this: a provider posting a
		// large body over a slow link should still be captured.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       time.Minute,
	}

	shutdownErr := make(chan error, 1)

	go func() {
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
		sig := <-quit

		app.logger.Printf("shutting down: signal %s", sig)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		shutdownErr <- srv.Shutdown(ctx)
	}()

	app.logger.Printf("listening on %s — dumping every request to stdout (%s)", srv.Addr, vcs.String())

	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	if err := <-shutdownErr; err != nil {
		return err
	}

	app.logger.Print("stopped")

	return nil
}

func envInt(key string, fallback int) int {
	v, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return fallback
	}

	return v
}
