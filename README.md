# HeartBridge Matchmaking

HeartBridge is the backend of a matchmaking agency. Consultants introduce two
enrolled singles to each other, both sides answer the introduction, an offline
meetup is arranged in a partner venue, and the paid introduction allowance of both
members is settled once the meetup actually happened.

The service is a pure Go backend: a JSON HTTP API, a real relational database with
versioned migrations, and two background loops. There is no frontend in this
repository.

## Two interdependent business paths

The whole system exists to keep these two paths consistent with each other.

**Introduction path.** A matchmaker proposes a pair. Both members answer inside
the window their service plan defines. Once both accepted, a consultant books a
venue slot. After the meetup the consultant records the completion, both
participants report back, and the introduction closes as a success or a failure.

**Allowance path.** Operations grant a service plan to a member, which produces an
allowance of introductions with a validity window. Proposing an introduction holds
one introduction on each side. Cancelling, declining or letting the answer window
elapse returns the hold. Completing the meetup converts the hold into a consumed
introduction. Every movement is appended to a ledger that is unique per
(allowance, introduction, movement kind), so a retry can never double count.

Neither path can be driven alone: an introduction without allowance is refused,
and an allowance only ever moves as a consequence of an introduction.

## Roles

| Role | What it may do |
| --- | --- |
| `member` | manage their own partner criteria and status, answer and withdraw their own introductions, report on their own meetups, read their own allowance history |
| `matchmaker` | propose introductions individually or in a batch, book, check in, complete and withdraw meetups, read venue slots and service plans |
| `admin` | everything a matchmaker may do, plus publishing venue slots, maintaining service plans, granting allowances, retiring profiles and reading the audit trail |

Authentication is a server-side session: login returns an opaque bearer token
whose SHA-256 digest is the only thing stored. Logout revokes the session, and a
revoked or expired token is refused on the very next request.

## State machines

```text
match:  pending_consent -> consented -> scheduled -> met -> closed_success
                        \-> closed_failed        \-> cancelled  \-> closed_failed
                        \-> cancelled
                        \-> expired

meetup: booked -> checked_in -> completed
              \-> cancelled  \-> no_show
              \-> no_show

allowance: active -> exhausted
                  \-> expired
```

Every transition is validated against the table above and applied with an
optimistic version guard, so a lost race is reported as `version_conflict` instead
of overwriting a concurrent decision.

## Database

SQLite through the pure Go `modernc.org/sqlite` driver. WAL journaling, enforced
foreign keys and a busy timeout are set as connection pragmas and verified at
startup; every transaction takes the write lock immediately, which removes the
deferred-to-exclusive upgrade deadlock.

Sixteen related tables across three versioned migrations:

| Migration | Tables |
| --- | --- |
| `0001_identity_and_members` | `users`, `sessions`, `members`, `member_preferences` |
| `0002_entitlements_and_matches` | `service_plans`, `entitlements`, `matches`, `match_consents`, `entitlement_ledger` |
| `0003_meetups_notifications_audit` | `venue_slots`, `meetups`, `meetup_feedbacks`, `notification_jobs`, `audit_events`, `idempotency_keys` |

The migration runner records a checksum per version. Re-running it is a no-op; a
rewritten migration or a database carrying an unknown newer version blocks
startup with an explicit message instead of guessing.

Invariants that live in the schema rather than in application code:

- one active allowance per member (partial unique index)
- one live introduction per member pair (partial unique index over a canonical
  pair key, so swapping the arguments cannot bypass it)
- one live meetup per introduction (partial unique index)
- one report per participant and meetup (unique index)
- one allowance movement of a given kind per introduction (unique index)
- `used + reserved <= total` and `booked_count <= capacity` (check constraints)

## Concurrency

Scarce resources are never taken with a read-then-write in the service layer. The
remaining venue capacity is checked inside the statement that increments the
counter, and the allowance holds are guarded by the expected version plus the
remaining quota:

```sql
UPDATE venue_slots SET booked_count = booked_count + 1, version = version + 1
WHERE id = ? AND booked_count < capacity;

UPDATE entitlements SET reserved = reserved + 1, version = version + 1
WHERE id = ? AND version = ? AND state = 'active'
  AND valid_from <= ? AND valid_until > ? AND used + reserved < total;
```

The suite races these paths with synchronisation barriers and fixed input, and
asserts on the business outcome rather than on timing.

## HTTP API

Every response is JSON. Failures use one envelope with a stable code, a readable
message and the correlation id, including the answers produced by the router
itself:

```json
{ "error": { "code": "capacity_exhausted", "message": "venue slot slt_… is fully booked", "request_id": "req-…" } }
```

| Method and path | Role | Purpose |
| --- | --- | --- |
| `GET /healthz` | public | liveness |
| `GET /readyz` | public | readiness; pings the database and checks the schema version |
| `POST /api/v1/auth/register` | public | enroll a member |
| `POST /api/v1/auth/login` | public | open a session |
| `POST /api/v1/auth/logout` | any | revoke the current session |
| `GET /api/v1/me` | any | describe the authenticated principal |
| `GET /api/v1/members/{memberID}` | any | profile, criteria and allowance |
| `PUT /api/v1/members/{memberID}/preference` | member | store partner criteria and activate the profile |
| `POST /api/v1/members/{memberID}/status` | any | pause, resume or retire a profile |
| `GET /api/v1/members/{memberID}/allowance-movements` | any | allowance history |
| `POST /api/v1/matches` | matchmaker, admin | propose one introduction |
| `POST /api/v1/matches/batch` | matchmaker, admin | propose up to 20 pairs with a verdict each |
| `GET /api/v1/matches` | any | filtered, sorted and paginated list |
| `GET /api/v1/matches/{matchID}` | any | one introduction with consents and movements |
| `POST /api/v1/matches/{matchID}/consent` | member | accept or decline |
| `POST /api/v1/matches/{matchID}/cancel` | any participant, staff | withdraw |
| `POST /api/v1/matches/{matchID}/meetup` | matchmaker, admin | book a venue slot |
| `GET /api/v1/meetups/{meetupID}` | any | one meetup |
| `POST /api/v1/meetups/{meetupID}/check-in` | matchmaker, admin | confirm arrival |
| `POST /api/v1/meetups/{meetupID}/complete` | matchmaker, admin | finish and settle |
| `POST /api/v1/meetups/{meetupID}/cancel` | matchmaker, admin | withdraw and free the seat |
| `POST /api/v1/meetups/{meetupID}/feedback` | member | report on a completed meetup |
| `GET /api/v1/venue-slots` | matchmaker, admin | bookable windows in a range |
| `GET /api/v1/service-plans` | matchmaker, admin | plan catalogue |
| `POST /api/v1/admin/staff` | admin | create a matchmaker or operations account |
| `POST /api/v1/admin/service-plans` | admin | create or update a plan |
| `POST /api/v1/admin/venue-slots` | admin | publish a bookable window |
| `POST /api/v1/admin/members/{memberID}/entitlements` | admin | grant a plan |
| `GET /api/v1/admin/audit-events` | admin | audit trail |

Cross-cutting behaviour:

- `X-Request-Id` is accepted or minted, echoed on the response, attached to every
  log record and stored on every audit row.
- Mutating endpoints accept `Idempotency-Key`. A replay of the same key and body
  returns the stored response with `Idempotent-Replay: true`; the same key with a
  different body is refused as `idempotency_mismatch`.
- List endpoints take `limit` and `offset` (capped at 100). The total is computed
  from the same predicate as the page.
- Request bodies reject unknown fields, and the request deadline is propagated all
  the way into the database driver.

## Background workers

`notification-dispatcher` drains the transactional outbox. Outbox rows are written
inside the same transaction as the change they announce, so a committed change is
never silently un-notified. A failed attempt is retried with exponential backoff
(2s, 4s, 8s, capped at 5m) until the attempt budget is spent, then retired as
`failed_permanent` with the last error stored.

`expiry-sweeper` expires introductions whose answer window elapsed and returns the
held allowance, prunes expired sessions and removes stale replay records. It skips
rows another actor changed concurrently instead of failing the round.

Both loops stop on context cancellation and are supervised by one shutdown path
together with the HTTP server.

## Configuration

Everything comes from the environment; see [`.env.example`](.env.example) for the
full list with defaults. Nothing is compiled in, and the redacted configuration
printed at startup never contains the bootstrap credentials.

## Running locally

```bash
go run ./cmd/server
```

The server creates the database directory, applies the migrations, seeds the
`starter` and `premium` service plans and starts listening on `:8080`. Set
`HEARTBRIDGE_BOOTSTRAP_ADMIN_EMAIL` and `HEARTBRIDGE_BOOTSTRAP_ADMIN_PASSWORD` to
seed the first operations account.

## Verification

```bash
go build ./...
go vet ./...
go test ./... -count=1
go test -race ./... -count=1
```

`make verify` runs the same set plus a gofmt check.

The suite covers the domain state machines and value objects, the service level
cross-entity flows, the real SQLite integration including migrations and restart
recovery, the HTTP contract with its error envelope and idempotency, transaction
rollback, concurrency races, worker retry and cancellation, pagination and the
business timezone boundaries.

## Container

```bash
docker buildx build --platform linux/amd64 --load -t heartbridge-matchmaking:amd64 .
docker run --rm -p 8080:8080 heartbridge-matchmaking:amd64
curl -fsS http://127.0.0.1:8080/healthz
curl -fsS http://127.0.0.1:8080/readyz
```

The image is built for `linux/amd64` and `linux/arm64`. The build stage runs on the
native platform of the build host and cross-compiles, which works because the
SQLite driver is pure Go and needs no C toolchain. The runtime stage runs as an
unprivileged user, keeps the database on the `/app/data` volume and ships a
`HEALTHCHECK` against `/readyz`.

## Layout

```text
cmd/server                  process entry point
internal/app                wiring, bootstrap, serving and shutdown
internal/config             environment configuration
internal/apperr             stable error codes and HTTP mapping
internal/logging            structured logging and request correlation
internal/clock              wall-clock abstraction and the business timezone
internal/security           password hashing, session tokens, identifiers
internal/domain/identity    accounts, sessions, actors
internal/domain/member      profiles and partner criteria
internal/domain/entitlement plans, allowances and the movement ledger
internal/domain/matching    introductions, consents, state machine
internal/domain/meetup      venue slots, meetups, feedback
internal/domain/notify      outbox jobs and the retry schedule
internal/domain/audit       audit vocabulary
internal/repository         persistence contracts and query types
internal/repository/sqliterepo  SQL implementations
internal/storage/sqlitedb   connection, transactions, migration runner
internal/auditlog           audit recorder
internal/idempotency        replay protection
internal/service/authsvc    registration, login, session lifecycle
internal/service/membersvc  profiles, criteria, allowance grants
internal/service/matchsvc   propose, answer, cancel, expire, batch
internal/service/schedulesvc  book, check in, complete, feedback
internal/service/adminsvc   venue slots, plans, audit
internal/httpapi            router, handlers, DTOs, rendering
internal/middleware         correlation, logging, recovery, timeout, auth
internal/worker             dispatcher, sweeper, supervision
internal/apptest            integration suite
migrations                  embedded versioned schema
```

## License

MIT. See [LICENSE](LICENSE).
