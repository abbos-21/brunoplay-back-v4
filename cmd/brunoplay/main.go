// Command brunoplay is the BrunoPlay backend: `api`, `worker`, `all` (both, for development), `migrate`, `admin-create`, `admin-reset-password`.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"brunoplay/internal/admin"
	"brunoplay/internal/api"
	"brunoplay/internal/apidoc"
	"brunoplay/internal/config"
	"brunoplay/internal/db"
	"brunoplay/internal/jobs"
	"brunoplay/internal/withdrawals"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	setupLogging()
	cmd := os.Args[1]
	cfg, err := config.Load()
	if err != nil {
		fatal("configuration", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch cmd {
	case "migrate":
		if err := db.Migrate(cfg.DatabaseURL); err != nil {
			fatal("migrate", err)
		}
		slog.Info("migrations applied")
	case "api", "worker", "all":
		if err := run(ctx, cfg, cmd); err != nil {
			fatal(cmd, err)
		}
	case "docs":
		if err := writeDocs(cfg); err != nil {
			fatal("docs", err)
		}
	case "admin-create":
		if err := adminCreate(ctx, cfg, os.Args[2:]); err != nil {
			fatal("admin-create", err)
		}
	case "admin-reset-password":
		if err := adminResetPassword(ctx, cfg, os.Args[2:]); err != nil {
			fatal("admin-reset-password", err)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: brunoplay <api|worker|all|migrate|admin-create --email you@example.com [--name Name]|admin-reset-password --email you@example.com>")
}

func fatal(what string, err error) {
	slog.Error("fatal", "what", what, "err", err)
	os.Exit(1)
}

func setupLogging() {
	lvl := slog.LevelInfo
	if strings.EqualFold(os.Getenv("LOG_LEVEL"), "debug") {
		lvl = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})))
}

func run(ctx context.Context, cfg config.Config, mode string) error {
	pool, err := db.Connect(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()
	// the schema must be current before serving (cheap when nothing is pending)
	if err := db.Migrate(cfg.DatabaseURL); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	deps := api.NewDeps(cfg, pool)
	if cfg.AutoPayout && (mode == "worker" || mode == "all") {
		sender, err := withdrawals.NewTonSender(ctx, cfg.PayoutSeed, cfg.PayoutWalletV, cfg.TonTestnet)
		if err != nil {
			return fmt.Errorf("automatic payouts are switched on but the payout wallet cannot be opened: %w", err)
		}
		deps.Withdrawals.SetSender(sender)
		slog.Warn("AUTOMATIC PAYOUTS ARE ON", "wallet", sender.Address(), "testnet", cfg.TonTestnet)
	}
	svc := newServices(deps)

	if mode == "api" || mode == "all" {
		sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		if err := deps.BotSetup(sctx); err != nil {
			slog.Error("bot setup failed (the bot keeps its previous webhook)", "err", err)
		}
		cancel()
	}
	var srv *http.Server
	errc := make(chan error, 1)
	if mode == "api" || mode == "all" {
		srv = &http.Server{
			Addr: cfg.HTTPAddr, Handler: deps.Router(),
			ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
			WriteTimeout: 30 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 16,
		}
		go func() {
			slog.Info("api listening", "addr", cfg.HTTPAddr, "env", cfg.Env, "version", api.Version)
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- err
			}
		}()
	}

	var wg interface{ Wait() }
	if mode == "worker" || mode == "all" {
		runner := jobs.NewRunner(pool)
		svc.registerJobs(runner)
		wg = runner.Start(ctx)
		slog.Info("worker started")
	}

	select {
	case <-ctx.Done():
	case err := <-errc:
		return err
	}
	slog.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownGrace)
	defer cancel()
	if srv != nil {
		if err := srv.Shutdown(sctx); err != nil {
			slog.Error("http shutdown", "err", err)
		}
	}
	if wg != nil {
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-sctx.Done():
			slog.Warn("worker did not stop in time")
		}
	}
	return nil
}

// adminCreate makes an admin account. The password is read from the ADMIN_PASSWORD variable or typed at the prompt;
// it is never a command-line argument and never written anywhere.
func adminCreate(ctx context.Context, cfg config.Config, args []string) error {
	var email, name, role string
	role = admin.RoleSuper
	for i := 0; i+1 < len(args); i += 2 {
		switch args[i] {
		case "--email":
			email = args[i+1]
		case "--name":
			name = args[i+1]
		case "--role":
			role = args[i+1]
		}
	}
	if email == "" {
		return errors.New("--email is required")
	}
	pw := os.Getenv("ADMIN_PASSWORD")
	if pw == "" {
		fmt.Fprint(os.Stderr, "Password: ")
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return err
		}
		pw = string(b)
	}
	pool, err := db.Connect(ctx, cfg.DatabaseURL, 2)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(cfg.DatabaseURL); err != nil {
		return err
	}
	deps := api.NewDeps(cfg, pool)
	a, err := deps.Admin.Create(ctx, admin.CreateInput{Email: email, Name: name, Password: pw, Role: role})
	if err != nil {
		return err
	}
	slog.Info("admin created", "id", a.ID, "email", a.Email, "role", a.Role)
	return nil
}

// writeDocs regenerates docs/API.md and docs/SETTINGS.md from the code (no database needed).
func writeDocs(cfg config.Config) error {
	d := api.NewDeps(cfg, nil)
	if err := os.MkdirAll("docs", 0o755); err != nil {
		return err
	}
	if err := os.WriteFile("docs/API.md", []byte(apidoc.API(d.Routes())), 0o644); err != nil {
		return err
	}
	return os.WriteFile("docs/SETTINGS.md", []byte(apidoc.Settings()), 0o644)
}

// adminResetPassword sets a new random password for an admin and prints it once on the terminal (passwords are stored only as
// hashes, so an old one can never be read back). The account is unlocked and signed out everywhere. Run it on the server:
//
//	docker compose run --rm api admin-reset-password --email you@example.com
func adminResetPassword(ctx context.Context, cfg config.Config, args []string) error {
	var email string
	for i := 0; i+1 < len(args); i += 2 {
		if args[i] == "--email" {
			email = strings.ToLower(strings.TrimSpace(args[i+1]))
		}
	}
	if email == "" {
		return errors.New("--email is required")
	}
	pool, err := db.Connect(ctx, cfg.DatabaseURL, 2)
	if err != nil {
		return err
	}
	defer pool.Close()
	var id int64
	if err := pool.QueryRow(ctx, `SELECT id FROM admins WHERE lower(email)=$1`, email).Scan(&id); err != nil {
		return fmt.Errorf("no admin with the email %q", email)
	}
	pw, err := randomPassword(20)
	if err != nil {
		return err
	}
	deps := api.NewDeps(cfg, pool)
	if _, err := deps.Admin.Update(ctx, id, admin.UpdateInput{Password: &pw}); err != nil {
		return err
	}
	slog.Info("admin password reset", "id", id, "email", email)
	fmt.Fprintf(os.Stderr, "\nNew password for %s (shown once, change it after you log in):\n\n%s\n\n", email, pw)
	return nil
}

// randomPassword draws from letters and digits without look-alike characters, and always has all three kinds.
func randomPassword(n int) (string, error) {
	const lower, upper, digits = "abcdefghijkmnpqrstuvwxyz", "ABCDEFGHJKLMNPQRSTUVWXYZ", "23456789"
	all := lower + upper + digits
	pick := func(set string) (byte, error) {
		i, err := rand.Int(rand.Reader, big.NewInt(int64(len(set))))
		if err != nil {
			return 0, err
		}
		return set[i.Int64()], nil
	}
	b := make([]byte, n)
	for i := range b {
		set := all
		if i < 3 {
			set = []string{lower, upper, digits}[i]
		}
		c, err := pick(set)
		if err != nil {
			return "", err
		}
		b[i] = c
	}
	for i := n - 1; i > 0; i-- { // shuffle so the three kinds are not always at the start
		j, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return "", err
		}
		b[i], b[j.Int64()] = b[j.Int64()], b[i]
	}
	return string(b), nil
}
