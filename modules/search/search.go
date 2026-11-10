// Package search is a simple full-text search over the site's published pages (SQLite FTS5).
//
// Query parameter: ?q=words. Template data: .Data.Query, .Data.Items ([]PageView), .Data.Count.
package search

import (
	"strings"
	"unicode"

	"hostkit/internal/module"
	"hostkit/internal/store"
)

func init() { module.Register(&Search{}) }

// Search is the module.
type Search struct{}

// Name implements module.Module.
func (*Search) Name() string { return "search" }

// Admin implements module.Module.
func (*Search) Admin() []module.AdminScreen { return nil }

// MatchQuery turns user words into a safe FTS5 query: every word becomes a quoted prefix term.
func MatchQuery(q string) string {
	var terms []string
	for _, w := range strings.FieldsFunc(q, func(r rune) bool { return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '‌') }) {
		if len(terms) == 8 {
			break
		}
		terms = append(terms, `"`+w+`"*`)
	}
	return strings.Join(terms, " ")
}

// Handle implements module.Module.
func (*Search) Handle(ctx *module.Ctx) (any, bool, error) {
	q := strings.TrimSpace(ctx.R.URL.Query().Get("q"))
	data := map[string]any{"Query": q, "Items": []*store.PageView{}, "Count": 0}
	m := MatchQuery(q)
	if m == "" {
		return data, false, nil
	}
	list, err := store.SearchPages(ctx.DB, m, 30)
	if err != nil {
		return data, false, nil // bad query: show no results
	}
	items := make([]*store.PageView, len(list))
	for i, p := range list {
		items[i] = p.View(ctx.Site.BasePath)
	}
	data["Items"], data["Count"] = items, len(items)
	return data, false, nil
}
