CREATE TABLE overrides (
  id          INTEGER PRIMARY KEY,
  target      TEXT NOT NULL,              -- guest name or 'host'
  action      TEXT NOT NULL CHECK (action IN ('on','off','pause','ignore_weather')),
  until       INTEGER,                    -- unix seconds; NULL = until next boundary/manual
  created_by  TEXT NOT NULL,              -- user id or 'system'
  created_at  INTEGER NOT NULL,
  cancelled_at INTEGER
);

CREATE INDEX idx_overrides_target ON overrides (target, created_at);

CREATE TABLE events (
  id      INTEGER PRIMARY KEY,
  at      INTEGER NOT NULL,
  kind    TEXT NOT NULL,                  -- guest_start, host_shutdown, login_ok, login_fail, weather_level, ...
  target  TEXT,
  actor   TEXT NOT NULL,                  -- 'schedule' | 'weather' | 'user:<id>' | 'system'
  reason  TEXT,
  ip      TEXT,
  dry_run INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_events_at ON events (at);

CREATE TABLE state (key TEXT PRIMARY KEY, value TEXT NOT NULL);

CREATE TABLE users (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  password_hash TEXT,
  totp_secret_enc BLOB
);

CREATE TABLE credentials (
  id BLOB PRIMARY KEY,
  user_id TEXT NOT NULL,
  data BLOB NOT NULL,
  created_at INTEGER NOT NULL,
  last_used INTEGER
);

CREATE TABLE sessions (
  token_hash BLOB PRIMARY KEY,
  user_id TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  last_seen INTEGER NOT NULL,
  stepup_until INTEGER,
  ip TEXT,
  user_agent TEXT
);
