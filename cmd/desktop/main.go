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
	"strings"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/replay"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

const appVersion = "0.3.0"

func main() {
	configPath := flag.String("config", "", "Path to TOML config file (default: built-in defaults, database next to the executable)")
	noBrowser := flag.Bool("no-browser", false, "Do not open the app window; only print the URL")
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
		if err := openAppWindow(url); err != nil {
			fmt.Fprintf(os.Stderr, "Could not open the app window (%v); open the URL above yourself.\n", err)
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

type appWindowCandidate struct {
	command string
	args    []string
}

// appWindowCandidates returns installed-browser commands that open the local
// UI without browser chrome. The server remains an ordinary loopback web app,
// so --no-browser and the printed URL always remain an escape hatch.
func appWindowCandidates(goos, url string, getenv func(string) string) []appWindowCandidate {
	appArg := "--app=" + url
	args := []string{appArg, "--new-window", "--window-size=1280,900"}
	var out []appWindowCandidate
	add := func(command string, commandArgs ...string) {
		if command == "" {
			return
		}
		key := strings.ToLower(filepath.Clean(command) + "\x00" + strings.Join(commandArgs, "\x00"))
		for _, c := range out {
			other := strings.ToLower(filepath.Clean(c.command) + "\x00" + strings.Join(c.args, "\x00"))
			if other == key {
				return
			}
		}
		out = append(out, appWindowCandidate{command: command, args: commandArgs})
	}
	addUnder := func(root string, parts ...string) {
		if root != "" {
			add(filepath.Join(append([]string{root}, parts...)...), args...)
		}
	}
	switch goos {
	case "windows":
		// Edge is present on supported Windows installs. Keep Chrome choices
		// too, including per-user installs, for stripped-down systems.
		addUnder(getenv("ProgramFiles(x86)"), "Microsoft", "Edge", "Application", "msedge.exe")
		addUnder(getenv("ProgramFiles"), "Microsoft", "Edge", "Application", "msedge.exe")
		addUnder(getenv("LOCALAPPDATA"), "Microsoft", "Edge", "Application", "msedge.exe")
		addUnder(getenv("ProgramFiles"), "Google", "Chrome", "Application", "chrome.exe")
		addUnder(getenv("ProgramFiles(x86)"), "Google", "Chrome", "Application", "chrome.exe")
		addUnder(getenv("LOCALAPPDATA"), "Google", "Chrome", "Application", "chrome.exe")
		add("msedge.exe", args...)
		add("chrome.exe", args...)
	case "darwin":
		add("open", "-na", "Microsoft Edge", "--args", appArg, "--new-window", "--window-size=1280,900")
		add("open", "-na", "Google Chrome", "--args", appArg, "--new-window", "--window-size=1280,900")
	default:
		for _, command := range []string{"microsoft-edge", "microsoft-edge-stable", "google-chrome", "chromium", "chromium-browser"} {
			add(command, args...)
		}
	}
	return out
}

// openAppWindow prefers a dedicated Edge/Chrome application window and falls
// back to the user's default browser when neither browser can be launched.
func openAppWindow(url string) error {
	var launchErrs []error
	for _, candidate := range appWindowCandidates(runtime.GOOS, url, os.Getenv) {
		if err := exec.Command(candidate.command, candidate.args...).Start(); err == nil {
			return nil
		} else {
			launchErrs = append(launchErrs, err)
		}
	}
	if err := openBrowser(url); err != nil {
		if len(launchErrs) > 0 {
			return fmt.Errorf("dedicated window: %v; default browser: %w", launchErrs[len(launchErrs)-1], err)
		}
		return err
	}
	return nil
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
