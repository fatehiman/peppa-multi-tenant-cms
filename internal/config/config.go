// Package config turns command-line flags into a Config struct.
package config

import (
	"flag"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Config holds every setting of a hostkit process.
type Config struct {
	Root      string // data root: main.db, sites/, themes/
	Listen    string // HTTP listen address
	PanelHost string // host name of the main admin panel
	LogLevel  string
	BackupDir string
	Dev       bool // http URLs, non-Secure cookies, serve static files from Go
	Static    bool // serve /_t/ and /_m/ from Go (normally Nginx/Apache does it)
	Scheme    string
	Fallback  string // HTML file served (200) for unknown hosts; empty = plain 404
}

// Parse reads flags from args and returns the config and the remaining arguments.
func Parse(args []string) (*Config, []string) {
	c := &Config{}
	fs := flag.NewFlagSet("hostkit", flag.ExitOnError)
	fs.StringVar(&c.Root, "root", "/srv/hostkit", "data root folder (main.db, sites/, themes/)")
	fs.StringVar(&c.Listen, "listen", "127.0.0.1:8080", "listen address")
	fs.StringVar(&c.PanelHost, "panel-host", "", "host name of the main admin panel, for example panel.example.com")
	fs.StringVar(&c.LogLevel, "log-level", "info", "debug, info, warn or error")
	fs.StringVar(&c.BackupDir, "backup-dir", "/srv/backups", "folder for nightly backups")
	fs.StringVar(&c.Fallback, "fallback-html", "", "HTML file served for unknown hosts (when hostkit is the web server's catch-all); empty = 404")
	fs.BoolVar(&c.Dev, "dev", false, "development mode: http URLs, cookies without Secure, static files served by hostkit")
	fs.BoolVar(&c.Static, "static", false, "serve /_t/ and /_m/ from hostkit (on by default with -dev)")
	_ = fs.Parse(args)
	c.Root, _ = filepath.Abs(c.Root)
	c.PanelHost = strings.ToLower(strings.TrimSpace(c.PanelHost))
	c.Scheme = "https"
	if c.Dev {
		c.Scheme = "http"
		c.Static = true
	}
	return c, fs.Args()
}

// SetupLogger installs a text slog logger at the configured level.
func (c *Config) SetupLogger() {
	var lvl slog.Level
	_ = lvl.UnmarshalText([]byte(c.LogLevel))
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})))
}

// Paths used everywhere.
func (c *Config) MainDB() string    { return filepath.Join(c.Root, "main.db") }
func (c *Config) SitesDir() string  { return filepath.Join(c.Root, "sites") }
func (c *Config) ThemesDir() string { return filepath.Join(c.Root, "themes") }
