// nevr-desktop: the NEVR-Anticheat desktop app. It runs the offline replay
// analysis (the same path as `nevr-ac analyze`) behind a small local web
// page: start it, drop .echoreplay files onto the page, read the players,
// scores, detections and review cases. Listens on 127.0.0.1 only, under a
// per-run random token.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/replay"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

func main() {
	configPath := flag.String("config", "", "Path to TOML config file (default: built-in defaults, database next to the executable)")
	noBrowser := flag.Bool("no-browser", false, "Do not open the default browser; only print the URL")
	port := flag.Int("port", 0, "Loopback port to listen on (default: a random free port)")
	logLevel := flag.String("log-level", "warn", "Engine log level (debug|info|warn|error); the console stays quiet unless something goes wrong")
	flag.Parse()

	if err := run(*configPath, *noBrowser, *port, *logLevel); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(configPath string, noBrowser bool, port int, logLevel string) error {
	if configPath == "" {
		// Like the CLI's drag-and-drop mode: without --config the database
		// lives next to the executable so results accumulate in one place
		// regardless of where the app was started from.
		if exe, err := os.Executable(); err == nil {
			_ = os.Chdir(filepath.Dir(exe))
		}
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	if logLevel != "" {
		cfg.General.LogLevel = logLevel
	}
	store, err := sqlite.NewStore(cfg.General.DBPath)
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}
	defer store.Close()
	engine := replay.NewEngine(cfg, store)
	config.LogStartup(engine.Logger(), cfg, nil)

	token, err := newToken()
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("listening: %w", err)
	}
	srv := newServer(engine, token)
	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- hs.Serve(ln) }()

	url := fmt.Sprintf("http://%s/%s/", ln.Addr(), token)
	fmt.Printf("NEVR-Anticheat desktop: open %s (press Ctrl+C to quit)\n", url)
	if !noBrowser {
		if err := openBrowser(url); err != nil {
			fmt.Fprintf(os.Stderr, "Could not open the browser (%v); open the URL above yourself.\n", err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	select {
	case <-ctx.Done():
	case <-srv.Done():
	case err := <-serveErr:
		return fmt.Errorf("serving: %w", err)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return hs.Shutdown(shutdownCtx)
}

// newToken is the per-run secret every URL carries.
func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// openBrowser opens url in the default browser without waiting for it.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
