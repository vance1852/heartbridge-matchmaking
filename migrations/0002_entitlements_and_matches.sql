-- Service plans, per-member introduction allowances, the movement ledger and the
-- matches that reserve those allowances.

CREATE TABLE service_plans (
    code             TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    intro_quota      INTEGER NOT NULL CHECK (intro_quota > 0),
    valid_days       INTEGER NOT NULL CHECK (valid_days > 0),
    max_active_match INTEGER NOT NULL CHECK (max_active_match > 0),
    consent_hours    INTEGER NOT NULL CHECK (consent_hours > 0),
    active           INTEGER NOT NULL CHECK (active IN (0, 1)),
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);

CREATE TABLE entitlements (
    id          TEXT PRIMARY KEY,
    member_id   TEXT NOT NULL REFERENCES members (id) ON DELETE CASCADE,
    plan_code   TEXT NOT NULL REFERENCES service_plans (code),
    total       INTEGER NOT NULL CHECK (total > 0),
    used        INTEGER NOT NULL CHECK (used >= 0),
    reserved    INTEGER NOT NULL CHECK (reserved >= 0),
    state       TEXT NOT NULL CHECK (state IN ('active', 'exhausted', 'expired')),
    valid_from  TEXT NOT NULL,
    valid_until TEXT NOT NULL,
    version     INTEGER NOT NULL CHECK (version >= 0),
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    CHECK (used + reserved <= total)
);

-- A member may hold at most one active entitlement at a time. The partial index
-- makes the rule a storage invariant instead of a service-layer convention.
CREATE UNIQUE INDEX ux_entitlements_active_member
    ON entitlements (member_id) WHERE state = 'active';
CREATE INDEX ix_entitlements_member_state ON entitlements (member_id, state);
CREATE INDEX ix_entitlements_valid_until ON entitlements (valid_until);

CREATE TABLE matches (
    id               TEXT PRIMARY KEY,
    matchmaker_id    TEXT NOT NULL REFERENCES users (id),
    member_a_id      TEXT NOT NULL REFERENCES members (id),
    member_b_id      TEXT NOT NULL REFERENCES members (id),
    pair_key         TEXT NOT NULL,
    state            TEXT NOT NULL CHECK (state IN (
                        'pending_consent', 'consented', 'scheduled', 'met',
                        'closed_success', 'closed_failed', 'cancelled', 'expired')),
    version          INTEGER NOT NULL CHECK (version >= 0),
    consent_deadline TEXT NOT NULL,
    closing_note     TEXT NOT NULL DEFAULT '',
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL,
    closed_at        TEXT NULL,
    CHECK (member_a_id < member_b_id)
);

-- The same pair of members may only have one live introduction at a time.
CREATE UNIQUE INDEX ux_matches_active_pair
    ON matches (pair_key)
    WHERE state IN ('pending_consent', 'consented', 'scheduled', 'met');
CREATE INDEX ix_matches_state_created_at ON matches (state, created_at);
CREATE INDEX ix_matches_member_a ON matches (member_a_id, state);
CREATE INDEX ix_matches_member_b ON matches (member_b_id, state);
CREATE INDEX ix_matches_matchmaker ON matches (matchmaker_id, state);
CREATE INDEX ix_matches_consent_deadline ON matches (state, consent_deadline);

CREATE TABLE match_consents (
    match_id   TEXT NOT NULL REFERENCES matches (id) ON DELETE CASCADE,
    member_id  TEXT NOT NULL REFERENCES members (id) ON DELETE CASCADE,
    decision   TEXT NOT NULL CHECK (decision IN ('pending', 'accepted', 'declined')),
    decided_at TEXT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (match_id, member_id)
);

CREATE INDEX ix_match_consents_member ON match_consents (member_id, decision);

CREATE TABLE entitlement_ledger (
    id             TEXT PRIMARY KEY,
    entitlement_id TEXT NOT NULL REFERENCES entitlements (id) ON DELETE CASCADE,
    match_id       TEXT NOT NULL REFERENCES matches (id) ON DELETE CASCADE,
    reason         TEXT NOT NULL CHECK (reason IN ('reserve', 'release', 'consume')),
    delta_reserved INTEGER NOT NULL,
    delta_used     INTEGER NOT NULL,
    created_at     TEXT NOT NULL
);

-- One movement of a given kind per match. A retried release therefore cannot
-- return the same introduction twice.
CREATE UNIQUE INDEX ux_entitlement_ledger_movement
    ON entitlement_ledger (entitlement_id, match_id, reason);
CREATE INDEX ix_entitlement_ledger_match ON entitlement_ledger (match_id);
