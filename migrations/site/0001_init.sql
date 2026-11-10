CREATE TABLE pages (
    id                 INTEGER PRIMARY KEY,
    slug               TEXT NOT NULL UNIQUE,      -- route without leading slash; '' is the home page
    title              TEXT NOT NULL DEFAULT '',
    body_html          TEXT NOT NULL DEFAULT '',
    template           TEXT NOT NULL DEFAULT '',  -- file in theme pages/, '' = default
    module             TEXT NOT NULL DEFAULT '',
    module_settings    TEXT NOT NULL DEFAULT '',  -- key=value per line, read by the module
    status             TEXT NOT NULL DEFAULT 'draft', -- draft / published
    sort               INTEGER NOT NULL DEFAULT 0,
    created_at         INTEGER NOT NULL,
    updated_at         INTEGER NOT NULL,
    seo_title          TEXT NOT NULL DEFAULT '',
    meta_description   TEXT NOT NULL DEFAULT '',
    canonical          TEXT NOT NULL DEFAULT '',
    robots             TEXT NOT NULL DEFAULT '',  -- '' = index,follow
    og_title           TEXT NOT NULL DEFAULT '',
    og_description     TEXT NOT NULL DEFAULT '',
    og_image           TEXT NOT NULL DEFAULT '',
    json_ld            TEXT NOT NULL DEFAULT '',
    alternates         TEXT NOT NULL DEFAULT '',  -- lang=URL per line
    sitemap_priority   TEXT NOT NULL DEFAULT '',
    sitemap_changefreq TEXT NOT NULL DEFAULT '',
    in_sitemap         INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE redirects (
    id        INTEGER PRIMARY KEY,
    from_path TEXT NOT NULL UNIQUE,
    to_url    TEXT NOT NULL DEFAULT '',
    code      INTEGER NOT NULL DEFAULT 301,  -- 301 / 302 / 410
    hits      INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE menus (
    id  INTEGER PRIMARY KEY,
    key TEXT NOT NULL UNIQUE
);

CREATE TABLE menu_items (
    id        INTEGER PRIMARY KEY,
    menu_id   INTEGER NOT NULL REFERENCES menus(id) ON DELETE CASCADE,
    parent_id INTEGER REFERENCES menu_items(id) ON DELETE CASCADE,
    label     TEXT NOT NULL DEFAULT '',
    url       TEXT NOT NULL DEFAULT '',
    page_id   INTEGER REFERENCES pages(id) ON DELETE SET NULL,
    sort      INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX menu_items_menu ON menu_items(menu_id);

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL DEFAULT ''
);

CREATE TABLE media (
    id         INTEGER PRIMARY KEY,
    file_name  TEXT NOT NULL UNIQUE,
    orig_name  TEXT NOT NULL DEFAULT '',
    mime       TEXT NOT NULL DEFAULT '',
    size       INTEGER NOT NULL DEFAULT 0,
    width      INTEGER NOT NULL DEFAULT 0,
    height     INTEGER NOT NULL DEFAULT 0,
    alt        TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);

CREATE TABLE users (
    id            INTEGER PRIMARY KEY,
    email         TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL DEFAULT 'editor'  -- admin / editor
);

CREATE TABLE module_data (
    id         INTEGER PRIMARY KEY,
    module     TEXT NOT NULL,
    key        TEXT NOT NULL DEFAULT '',
    value_json TEXT NOT NULL DEFAULT '{}',
    created_at INTEGER NOT NULL
);
CREATE INDEX module_data_mk ON module_data(module, key);

-- Full-text index for the search module (external content = pages).
CREATE VIRTUAL TABLE pages_fts USING fts5(title, body_html, content='pages', content_rowid='id');
CREATE TRIGGER pages_ai AFTER INSERT ON pages BEGIN
    INSERT INTO pages_fts(rowid, title, body_html) VALUES (new.id, new.title, new.body_html);
END;
CREATE TRIGGER pages_ad AFTER DELETE ON pages BEGIN
    INSERT INTO pages_fts(pages_fts, rowid, title, body_html) VALUES ('delete', old.id, old.title, old.body_html);
END;
CREATE TRIGGER pages_au AFTER UPDATE OF title, body_html ON pages BEGIN
    INSERT INTO pages_fts(pages_fts, rowid, title, body_html) VALUES ('delete', old.id, old.title, old.body_html);
    INSERT INTO pages_fts(rowid, title, body_html) VALUES (new.id, new.title, new.body_html);
END;
