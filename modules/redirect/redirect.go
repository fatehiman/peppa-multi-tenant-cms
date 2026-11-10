// Package redirect is a page that sends a 301 redirect to the URL in its module
// settings (redirect_url=https://… or redirect_url=/other-page).
package redirect

import (
	"net/http"
	"strings"

	"hostkit/internal/module"
)

func init() { module.Register(&Redirect{}) }

// Redirect is the module.
type Redirect struct{}

// Name implements module.Module.
func (*Redirect) Name() string { return "redirect" }

// Admin implements module.Module.
func (*Redirect) Admin() []module.AdminScreen { return nil }

// Handle implements module.Module.
func (*Redirect) Handle(ctx *module.Ctx) (any, bool, error) {
	to := strings.TrimSpace(ctx.Page.ModuleSetting("redirect_url"))
	if to == "" {
		return nil, false, nil // no target: render the page normally
	}
	if strings.HasPrefix(to, "/") && !strings.HasPrefix(to, "//") {
		to = ctx.URL(to)
	}
	http.Redirect(ctx.W, ctx.R, to, http.StatusMovedPermanently)
	return nil, true, nil
}
