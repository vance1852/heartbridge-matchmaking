-- Accounts, revocable sessions, member profiles and partner criteria.

CREATE TABLE users (
    id            TEXT PRIMARY KEY,
    email         TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL CHECK (role IN ('member', 'matchmaker', 'admin')),
    status        TEXT NOT NULL CHECK (status IN ('active', 'suspended')),
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);

CREATE UNIQUE INDEX ux_users_email ON users (email);
CREATE INDEX ix_users_role_status ON users (role, status);

CREATE TABLE sessions (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash   TEXT NOT NULL,
    issued_at    TEXT NOT NULL,
    expires_at   TEXT NOT NULL,
    revoked_at   TEXT NULL,
    last_seen_at TEXT NOT NULL
);

CREATE UNIQUE INDEX ux_sessions_token_hash ON sessions (token_hash);
CREATE INDEX ix_sessions_user_id ON sessions (user_id);
CREATE INDEX ix_sessions_expires_at ON sessions (expires_at);

CREATE TABLE members (
    id             TEXT PRIMARY KEY,
    user_id        TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    display_name   TEXT NOT NULL,
    gender         TEXT NOT NULL CHECK (gender IN ('male', 'female')),
    birth_date     TEXT NOT NULL,
    city           TEXT NOT NULL,
    marital_status TEXT NOT NULL CHECK (marital_status IN ('single', 'divorced', 'widowed')),
    education      TEXT NOT NULL CHECK (education IN ('high_school', 'college', 'bachelor', 'master', 'doctor')),
    status         TEXT NOT NULL CHECK (status IN ('onboarding', 'active', 'paused', 'retired')),
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL
);

CREATE UNIQUE INDEX ux_members_user_id ON members (user_id);
CREATE INDEX ix_members_city_gender_status ON members (city, gender, status);

CREATE TABLE member_preferences (
    member_id        TEXT PRIMARY KEY REFERENCES members (id) ON DELETE CASCADE,
    seeking_gender   TEXT NOT NULL CHECK (seeking_gender IN ('male', 'female')),
    min_age          INTEGER NOT NULL CHECK (min_age >= 18),
    max_age          INTEGER NOT NULL CHECK (max_age >= min_age),
    cities           TEXT NOT NULL,
    marital_statuses TEXT NOT NULL,
    min_education    TEXT NOT NULL CHECK (min_education IN ('high_school', 'college', 'bachelor', 'master', 'doctor')),
    updated_at       TEXT NOT NULL
);
