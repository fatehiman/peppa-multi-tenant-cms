// Package list lists child pages (news, projects, ...) with pagination via ?p=2.
//
// Module settings of the page: prefix=news/ (default: the page's own slug + "/"), per_page=10.
// Template data: .Data.Items ([]PageView), .Data.Page, .Data.Pages, .Data.Total, .Data.PrevURL, .Data.NextURL.
package list

import (
	"strconv"
	"strings"

	"hostkit/internal/module"
	"hostkit/internal/store"
)

func init() { module.Register(&List{}) }

// List is the module.
type List struct{}

// Name implements module.Module.
func (*List) Name() string { return "list" }

// Admin implements module.Module.
func (*List) Admin() []module.AdminScreen { return nil }

// Cacheable: the output only depends on pages and the URL, so it may be cached.
func (*List) Cacheable() bool { return true }

// Handle implements module.Module.
func (*List) Handle(ctx *module.Ctx) (any, bool, error) {
	set := store.ParseKV(ctx.Page.ModuleSettings)
	prefix := set["prefix"]
	if prefix == "" {
		prefix = ctx.Page.Slug + "/"
	}
	prefix = strings.TrimPrefix(prefix, "/")
	per, _ := strconv.Atoi(set["per_page"])
	if per <= 0 || per > 100 {
		per = 10
	}
	page, _ := strconv.Atoi(ctx.R.URL.Query().Get("p"))
	if page < 1 {
		page = 1
	}
	total, err := store.CountByPrefix(ctx.DB, prefix)
	if err != nil {
		return nil, false, err
	}
	list, err := store.PagesByPrefix(ctx.DB, prefix, per, (page-1)*per)
	if err != nil {
		return nil, false, err
	}
	items := make([]*store.PageView, len(list))
	for i, p := range list {
		items[i] = p.View(ctx.Site.BasePath)
	}
	pages := (total + per - 1) / per
	self := store.PagePath(ctx.Site.BasePath, ctx.Page.Slug)
	data := map[string]any{"Items": items, "Page": page, "Pages": pages, "Total": total}
	if page > 1 {
		if page == 2 {
			data["PrevURL"] = self
		} else {
			data["PrevURL"] = self + "?p=" + strconv.Itoa(page-1)
		}
	}
	if page < pages {
		data["NextURL"] = self + "?p=" + strconv.Itoa(page+1)
	}
	return data, false, nil
}
