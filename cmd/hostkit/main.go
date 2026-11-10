// Command hostkit serves every website on the server from one process.
//
//	hostkit [flags] serve                  run the web server (default)
//	hostkit [flags] admin create <email>   create a main admin (password from stdin)
//	hostkit [flags] admin password <email> set a new main admin password (from stdin)
//	hostkit [flags] site create -key k -name n -hosts a.com,www.a.com [-theme t] [-lang l] [-admin-email e]
//	hostkit [flags] site sql <key> <file.sql>   run a SQL file on a site database (seeding)
//	hostkit [flags] site list
//	hostkit theme check <folder>           check a theme before you assign it
//	hostkit [flags] backup                 nightly backup into -backup-dir
//
// Flags come before the command: -root, -listen, -panel-host, -log-level, -backup-dir, -dev, -static.
// After "site create" or "site sql", run `systemctl reload hostkit` (SIGHUP) so a running server sees the change.
package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"hostkit/internal/auth"
	"hostkit/internal/backup"
	"hostkit/internal/config"
	"hostkit/internal/core"
	"hostkit/internal/db"
	"hostkit/internal/mainadmin"
	"hostkit/internal/module"
	"hostkit/internal/render"
	"hostkit/internal/siteadmin"
	"hostkit/internal/tenant"
	"hostkit/internal/web"
	"hostkit/migrations"

	_ "hostkit/modules/contact"
	_ "hostkit/modules/list"
	_ "hostkit/modules/redirect"
	_ "hostkit/modules/search"
)

// version is set at build time with -ldflags "-X main.version=…".
var version = "dev"

func main() {
	cfg, args := config.Parse(os.Args[1:])
	cfg.SetupLogger()
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve(cfg)
	case "admin":
		err = adminCmd(cfg, args[1:])
	case "site":
		err = siteCmd(cfg, args[1:])
	case "theme":
		if len(args) < 3 || args[1] != "check" {
			err = errors.New("usage: hostkit theme check <folder>")
			break
		}
		os.Exit(themeCheck(args[2]))
	case "backup":
		err = backupCmd(cfg)
	case "version":
		fmt.Println("hostkit", version)
	default:
		err = fmt.Errorf("unknown command %q (serve, admin, site, theme, backup, version)", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// openMain opens main.db and runs its migrations.
func openMain(cfg *config.Config) (*sql.DB, error) {
	if err := os.MkdirAll(cfg.Root, 0o755); err != nil {
		return nil, err
	}
	d, err := db.Open(cfg.MainDB())
	if err != nil {
		return nil, err
	}
	if err := db.Migrate(d, migrations.FS, "main"); err != nil {
		d.Close()
		return nil, err
	}
	_ = os.Chmod(cfg.MainDB(), 0o600)
	return d, nil
}

func openRegistry(cfg *config.Config) (*sql.DB, *tenant.Registry, error) {
	mainDB, err := openMain(cfg)
	if err != nil {
		return nil, nil, err
	}
	reg, err := tenant.New(mainDB, cfg.SitesDir())
	if err != nil {
		return nil, nil, err
	}
	reg.OnOpen = module.RunMigrations
	return mainDB, reg, nil
}

func serve(cfg *config.Config) error {
	mainDB, reg, err := openRegistry(cfg)
	if err != nil {
		return err
	}
	defer mainDB.Close()
	if err := os.MkdirAll(cfg.ThemesDir(), 0o755); err != nil {
		return err
	}
	am, err := auth.NewManager(mainDB, !cfg.Dev, filepath.Join(cfg.Root, "secret.key"))
	if err != nil {
		return err
	}
	app := &core.App{Cfg: cfg, Reg: reg, Auth: am, Themes: render.NewSet(cfg.ThemesDir()), Cache: render.NewCache(8 << 20),
		LoginLimit: auth.NewLimiter(5, 15*time.Minute), FormLimit: auth.NewLimiter(5, 10*time.Minute)}
	for _, t := range app.Themes.List() {
		if t.Error != "" {
			slog.Warn("theme not loaded", "theme", t.Name, "err", t.Error)
		}
	}
	ma, err := mainadmin.New(app)
	if err != nil {
		return err
	}
	sa, err := siteadmin.New(app)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           &web.Router{App: app, MainAdmin: ma, SiteAdmin: sa},
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
	}
	stop := make(chan struct{})
	go reg.RunIdleCloser(10*time.Minute, stop)

	sig := make(chan os.Signal, 2)
	signal.Notify(sig, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		for s := range sig {
			if s == syscall.SIGHUP {
				slog.Info("SIGHUP: reloading themes and sites")
				mainadmin.Reload(app)
				continue
			}
			slog.Info("shutting down")
			close(stop)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = srv.Shutdown(ctx)
			cancel()
			return
		}
	}()
	slog.Info("hostkit listening", "version", version, "addr", cfg.Listen, "root", cfg.Root, "panel", cfg.PanelHost,
		"sites", len(reg.All()), "themes", strings.Join(app.Themes.Names(), ","))
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	reg.CloseAll()
	return nil
}

func readPassword() (string, error) {
	fmt.Fprint(os.Stderr, "Password (at least 8 characters): ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", errors.New("no password on stdin")
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func adminCmd(cfg *config.Config, args []string) error {
	if len(args) < 2 || (args[0] != "create" && args[0] != "password") {
		return errors.New("usage: hostkit admin create <email> | hostkit admin password <email>")
	}
	email := strings.ToLower(strings.TrimSpace(args[1]))
	pw, err := readPassword()
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	d, err := openMain(cfg)
	if err != nil {
		return err
	}
	defer d.Close()
	if args[0] == "create" {
		if _, err := d.Exec("INSERT INTO main_admins (email, password_hash) VALUES (?, ?)", email, hash); err != nil {
			return fmt.Errorf("could not create admin (exists already?): %w", err)
		}
		fmt.Println("main admin created:", email)
		return nil
	}
	res, err := d.Exec("UPDATE main_admins SET password_hash = ? WHERE email = ?", hash, email)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("no main admin with this email")
	}
	_, _ = d.Exec("DELETE FROM sessions WHERE scope = 'main'")
	fmt.Println("password changed:", email)
	return nil
}

func siteCmd(cfg *config.Config, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: hostkit site create|sql|list")
	}
	mainDB, reg, err := openRegistry(cfg)
	if err != nil {
		return err
	}
	defer mainDB.Close()
	defer reg.CloseAll()
	switch args[0] {
	case "list":
		for _, s := range reg.All() {
			var hs []string
			for _, h := range s.Hosts {
				hs = append(hs, h.String())
			}
			fmt.Printf("%-20s %-10s %-8s %-6s %s\n", s.Key, s.Theme, s.Status, s.Lang, strings.Join(hs, " "))
		}
		return nil
	case "create":
		fs := flag.NewFlagSet("site create", flag.ExitOnError)
		key := fs.String("key", "", "site key (folder name)")
		name := fs.String("name", "", "site name")
		hosts := fs.String("hosts", "", "hosts, comma separated; first is primary (domain.com or domain.com/de)")
		theme := fs.String("theme", "default", "theme folder")
		lang := fs.String("lang", "en", "language code")
		email := fs.String("admin-email", "", "first site admin (password from stdin)")
		_ = fs.Parse(args[1:])
		hl, err := tenant.ParseHosts(strings.ReplaceAll(*hosts, ",", "\n"))
		if err != nil {
			return err
		}
		in := tenant.SiteInput{Key: *key, Name: *name, Theme: *theme, Lang: *lang, Hosts: hl, AdminEnabled: true}
		if *email != "" {
			pw, err := readPassword()
			if err != nil {
				return err
			}
			if in.AdminPasswordHash, err = auth.HashPassword(pw); err != nil {
				return err
			}
			in.AdminEmail = *email
		}
		s, err := reg.CreateSite(in)
		if err != nil {
			return err
		}
		fmt.Println("site created:", s.Key, "— run `systemctl reload hostkit` if the server is running")
		return nil
	case "sql":
		if len(args) < 3 {
			return errors.New("usage: hostkit site sql <key> <file.sql>")
		}
		s := reg.ByKey(args[1])
		if s == nil {
			return fmt.Errorf("no site %q", args[1])
		}
		body, err := os.ReadFile(args[2])
		if err != nil {
			return err
		}
		d, err := reg.DB(s)
		if err != nil {
			return err
		}
		if _, err := d.Exec(string(body)); err != nil {
			return err
		}
		fmt.Println("done — run `systemctl reload hostkit` to clear the page cache")
		return nil
	}
	return fmt.Errorf("unknown site command %q", args[0])
}

func themeCheck(dir string) int {
	problems, warnings := render.Check(dir)
	for _, w := range warnings {
		fmt.Println("warning:", w)
	}
	for _, p := range problems {
		fmt.Println("problem:", p)
	}
	if len(problems) > 0 {
		fmt.Printf("theme %s: %d problem(s)\n", filepath.Base(dir), len(problems))
		return 1
	}
	fmt.Printf("theme %s: OK\n", filepath.Base(dir))
	return 0
}

func backupCmd(cfg *config.Config) error {
	mainDB, reg, err := openRegistry(cfg)
	if err != nil {
		return err
	}
	defer mainDB.Close()
	defer reg.CloseAll()
	if err := backup.Nightly(mainDB, reg, cfg.BackupDir, 14); err != nil {
		return err
	}
	fmt.Println("backup written to", cfg.BackupDir)
	return nil
}
