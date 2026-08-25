-- Venue slots, meetups, participant feedback, the notification outbox, the audit
-- trail and the idempotency registry.

CREATE TABLE venue_slots (
    id           TEXT PRIMARY KEY,
    venue_code   TEXT NOT NULL,
    venue_name   TEXT NOT NULL,
    city         TEXT NOT NULL,
    start_at     TEXT NOT NULL,
    end_at       TEXT NOT NULL,
    capacity     INTEGER NOT NULL CHECK (capacity > 0),
    booked_count INTEGER NOT NULL CHECK (booked_count >= 0),
    version      INTEGER NOT NULL CHECK (version >= 0),
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    CHECK (booked_count <= capacity),
    CHECK (end_at > start_at)
);

CREATE UNIQUE INDEX ux_venue_slots_code_start ON venue_slots (venue_code, start_at);
CREATE INDEX ix_venue_slots_city_start ON venue_slots (city, start_at);

CREATE TABLE meetups (
    id            TEXT PRIMARY KEY,
    match_id      TEXT NOT NULL REFERENCES matches (id) ON DELETE CASCADE,
    slot_id       TEXT NOT NULL REFERENCES venue_slots (id),
    state         TEXT NOT NULL CHECK (state IN ('booked', 'checked_in', 'completed', 'cancelled', 'no_show')),
    version       INTEGER NOT NULL CHECK (version >= 0),
    booked_at     TEXT NOT NULL,
    checked_in_at TEXT NULL,
    completed_at  TEXT NULL,
    closed_at     TEXT NULL,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);

-- One live meetup per match. Cancelled and no-show rows stay for the history.
CREATE UNIQUE INDEX ux_meetups_active_match
    ON meetups (match_id)
    WHERE state IN ('booked', 'checked_in', 'completed');
CREATE INDEX ix_meetups_slot ON meetups (slot_id, state);
CREATE INDEX ix_meetups_state ON meetups (state);

CREATE TABLE meetup_feedbacks (
    id               TEXT PRIMARY KEY,
    meetup_id        TEXT NOT NULL REFERENCES meetups (id) ON DELETE CASCADE,
    author_member_id TEXT NOT NULL REFERENCES members (id) ON DELETE CASCADE,
    intent           TEXT NOT NULL CHECK (intent IN ('continue', 'stop')),
    rating           INTEGER NOT NULL CHECK (rating BETWEEN 1 AND 5),
    comment          TEXT NOT NULL DEFAULT '',
    created_at       TEXT NOT NULL
);

CREATE UNIQUE INDEX ux_meetup_feedbacks_author ON meetup_feedbacks (meetup_id, author_member_id);

CREATE TABLE notification_jobs (
    id              TEXT PRIMARY KEY,
    match_id        TEXT NOT NULL DEFAULT '',
    recipient_id    TEXT NOT NULL,
    kind            TEXT NOT NULL,
    payload         TEXT NOT NULL DEFAULT '',
    state           TEXT NOT NULL CHECK (state IN ('pending', 'succeeded', 'failed_permanent')),
    attempts        INTEGER NOT NULL CHECK (attempts >= 0),
    max_attempts    INTEGER NOT NULL CHECK (max_attempts > 0),
    next_attempt_at TEXT NOT NULL,
    last_error      TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);

CREATE INDEX ix_notification_jobs_due ON notification_jobs (state, next_attempt_at);
CREATE INDEX ix_notification_jobs_match ON notification_jobs (match_id);

CREATE TABLE audit_events (
    id          TEXT PRIMARY KEY,
    actor_id    TEXT NOT NULL,
    actor_role  TEXT NOT NULL,
    action      TEXT NOT NULL,
    object_type TEXT NOT NULL,
    object_id   TEXT NOT NULL,
    result      TEXT NOT NULL CHECK (result IN ('success', 'rejected')),
    detail      TEXT NOT NULL DEFAULT '',
    request_id  TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL
);

CREATE INDEX ix_audit_events_object ON audit_events (object_type, object_id, created_at);
CREATE INDEX ix_audit_events_actor ON audit_events (actor_id, created_at);
CREATE INDEX ix_audit_events_request ON audit_events (request_id);

CREATE TABLE idempotency_keys (
    actor_id     TEXT NOT NULL,
    method       TEXT NOT NULL,
    path         TEXT NOT NULL,
    idem_key     TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    status       INTEGER NOT NULL,
    response_b64 TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    expires_at   TEXT NOT NULL,
    PRIMARY KEY (actor_id, method, path, idem_key)
);

CREATE INDEX ix_idempotency_keys_expires_at ON idempotency_keys (expires_at);
