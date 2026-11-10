CREATE TABLE sites (
    id             INTEGER PRIMARY KEY,
    key            TEXT NOT NULL UNIQUE,
    name           TEXT NOT NULL,
    theme          TEXT NOT NULL DEFAULT 'default',
    lang           TEXT NOT NULL DEFAULT 'en',
    status         TEXT NOT NULL DEFAULT 'active',  -- active / disabled
    admin_enabled  INTEGER NOT NULL DEFAULT 1,
    admin_sections TEXT NOT NULL DEFAULT '',        -- override of theme.json "admin", comma list; '' = use theme
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
);

-- A host is a domain plus an optional path prefix ('' or like '/de').
CREATE TABLE domains (
    domain      TEXT NOT NULL,
    path_prefix TEXT NOT NULL DEFAULT '',
    site_id     INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    is_primary  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (domain, path_prefix)
);
CREATE INDEX domains_site ON domains(site_id);

CREATE TABLE main_admins (
    id            INTEGER PRIMARY KEY,
    email         TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL
);

-- Sessions of both panel types. scope = 'main', a site key, or 'open:<site key>' (one-time hand-over token).
CREATE TABLE sessions (
    token_hash TEXT PRIMARY KEY,
    user_ref   TEXT NOT NULL,     -- 'main:<id>' or 'site:<id>'
    scope      TEXT NOT NULL,
    csrf       TEXT NOT NULL,
    expires_at INTEGER NOT NULL
);
CREATE INDEX sessions_expires ON sessions(expires_at);
