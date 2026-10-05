// Package store holds all Postgres access for FeatureVote.
//
// Scores are computed at read time (COUNT FILTER over votes per idea) rather
// than denormalised onto ideas: boards are small (hundreds of ideas, thousands
// of votes), a computed score cannot drift, and merge / GDPR delete / vote
// switch recounts are correct by construction with no triggers. Revisit only
// if list latency matters (then add a counter cache maintained in the same tx).
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Idea statuses.
const (
	StatusUnderReview = "under_review"
	StatusPlanned     = "planned"
	StatusInProgress  = "in_progress"
	StatusShipped     = "shipped"
	StatusDeclined    = "declined"
)

// Moderation states.
const (
	ModPending  = "pending"
	ModApproved = "approved"
	ModRejected = "rejected"
	ModMerged   = "merged"
)

// ValidStatus reports whether s is a known status label.
func ValidStatus(s string) bool {
	switch s {
	case StatusUnderReview, StatusPlanned, StatusInProgress, StatusShipped, StatusDeclined:
		return true
	}
	return false
}

// ValidModerationState reports whether s is a known moderation state.
func ValidModerationState(s string) bool {
	switch s {
	case ModPending, ModApproved, ModRejected, ModMerged:
		return true
	}
	return false
}

var (
	ErrNotFound     = errors.New("not found")
	ErrOwnIdea      = errors.New("cannot vote on own idea")
	ErrVotingClosed = errors.New("voting closed")
	// ErrConflict is an invalid state transition; the message explains it.
	ErrConflict = errors.New("invalid state")
)

// ConflictError wraps ErrConflict with a stable code (for callers that map
// it to fixed messages) and a developer message.
type ConflictError struct{ Code, Msg string }

// Conflict codes.
const (
	ConflictTransition    = "transition"
	ConflictSelfMerge     = "self_merge"
	ConflictAlreadyMerged = "already_merged"
	ConflictBadTarget     = "bad_target"
)

func (e *ConflictError) Error() string { return e.Msg }
func (e *ConflictError) Unwrap() error { return ErrConflict }

func conflict(code, format string, args ...any) error {
	return &ConflictError{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// RateLimitedError is returned when a submission limit is hit.
type RateLimitedError struct{ RetryAfter time.Duration }

func (e *RateLimitedError) Error() string { return "rate limited" }

// Idea is a full idea row plus its computed vote counts.
type Idea struct {
	ID              int64
	Title           string
	Body            string
	AuthorSub       *string
	Status          string
	ModerationState string
	MergedIntoID    *int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
	Up              int64
	Down            int64
}

// Score is up minus down.
func (i Idea) Score() int64 { return i.Up - i.Down }

// Vote is one user's vote on one idea.
type Vote struct {
	IdeaID int64 `json:"idea_id"`
	Value  int   `json:"value"`
}

// DB is satisfied by *pgxpool.Pool and pgx.Tx.
type DB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Store wraps a connection pool.
type Store struct {
	pool *pgxpool.Pool
}

// New returns a Store backed by pool.
func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Ping checks database connectivity.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

const ideaCols = `i.id, i.title, i.body, i.author_sub, i.status, i.moderation_state, i.merged_into_id,
	i.created_at, i.updated_at, COALESCE(v.up, 0), COALESCE(v.down, 0)`

// ideaFrom joins every idea with its computed vote counts.
const ideaFrom = ` FROM ideas i LEFT JOIN LATERAL (
	SELECT count(*) FILTER (WHERE value = 1) AS up, count(*) FILTER (WHERE value = -1) AS down
	FROM votes WHERE idea_id = i.id
) v ON true `

func scanIdea(row pgx.Row) (Idea, error) {
	var i Idea
	err := row.Scan(&i.ID, &i.Title, &i.Body, &i.AuthorSub, &i.Status, &i.ModerationState, &i.MergedIntoID,
		&i.CreatedAt, &i.UpdatedAt, &i.Up, &i.Down)
	if errors.Is(err, pgx.ErrNoRows) {
		return Idea{}, ErrNotFound
	}
	return i, err
}

func queryIdeas(ctx context.Context, db DB, where string, args ...any) ([]Idea, error) {
	rows, err := db.Query(ctx, `SELECT `+ideaCols+ideaFrom+where, args...)
	if err != nil {
		return nil, err
	}
	ideas, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Idea, error) { return scanIdea(r) })
	if ideas == nil {
		ideas = []Idea{}
	}
	return ideas, err
}

func getIdea(ctx context.Context, db DB, id int64) (Idea, error) {
	return scanIdea(db.QueryRow(ctx, `SELECT `+ideaCols+ideaFrom+`WHERE i.id = $1`, id))
}

// GetIdea returns an idea in any moderation state.
func (s *Store) GetIdea(ctx context.Context, id int64) (Idea, error) {
	return getIdea(ctx, s.pool, id)
}

// ListParams filters and pages the public list.
type ListParams struct {
	Sort   string // "top" or "new"
	Status string // "" for all
	Limit  int
	Offset int
}

// ListApproved returns approved ideas and the total count matching the filter.
func (s *Store) ListApproved(ctx context.Context, p ListParams) ([]Idea, int, error) {
	order := `ORDER BY (COALESCE(v.up,0) - COALESCE(v.down,0)) DESC, COALESCE(v.up,0) DESC, i.created_at DESC, i.id DESC`
	if p.Sort == "new" {
		order = `ORDER BY i.created_at DESC, i.id DESC`
	}
	where := `WHERE i.moderation_state = 'approved' AND ($1 = '' OR i.status = $1) `
	ideas, err := queryIdeas(ctx, s.pool, where+order+` LIMIT $2 OFFSET $3`, p.Status, p.Limit, p.Offset)
	if err != nil {
		return nil, 0, err
	}
	var total int
	err = s.pool.QueryRow(ctx,
		`SELECT count(*) FROM ideas i WHERE i.moderation_state = 'approved' AND ($1 = '' OR i.status = $1)`,
		p.Status).Scan(&total)
	return ideas, total, err
}

// ListByModeration returns ideas in the given moderation state ("all" for
// every state), newest first.
func (s *Store) ListByModeration(ctx context.Context, state string) ([]Idea, error) {
	return queryIdeas(ctx, s.pool, `WHERE ($1 = 'all' OR i.moderation_state = $1) ORDER BY i.created_at DESC, i.id DESC`, state)
}

// VotesBySub returns every vote cast by sub on approved ideas.
func (s *Store) VotesBySub(ctx context.Context, sub string) ([]Vote, error) {
	rows, err := s.pool.Query(ctx, `SELECT v.idea_id, v.value FROM votes v JOIN ideas i ON i.id = v.idea_id
		WHERE v.voter_sub = $1 AND i.moderation_state = 'approved' ORDER BY v.idea_id`, sub)
	if err != nil {
		return nil, err
	}
	votes, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Vote, error) {
		var v Vote
		var val int16
		err := r.Scan(&v.IdeaID, &val)
		v.Value = int(val)
		return v, err
	})
	if votes == nil {
		votes = []Vote{}
	}
	return votes, err
}

// IdeasBySub returns sub's own pending or approved ideas, newest first.
func (s *Store) IdeasBySub(ctx context.Context, sub string) ([]Idea, error) {
	return queryIdeas(ctx, s.pool, `WHERE i.author_sub = $1 AND i.moderation_state IN ('pending','approved')
		ORDER BY i.created_at DESC, i.id DESC`, sub)
}

// SubmitIdea creates a pending idea authored by sub, unless sub already
// submitted limit ideas in the 24h before now. The count comes from the ideas
// table itself so it is durable across restarts and replicas.
func (s *Store) SubmitIdea(ctx context.Context, sub, title, body string, limit int, now time.Time) (Idea, error) {
	var idea Idea
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Serialise submissions per sub so concurrent requests cannot exceed the limit.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('fv-submit:' || $1, 0))`, sub); err != nil {
			return err
		}
		windowStart := now.Add(-24 * time.Hour)
		var n int
		var oldest *time.Time
		// The oldest of the most recent `limit` submissions is the one whose
		// ageing out frees a slot.
		err := tx.QueryRow(ctx, `SELECT count(*), min(created_at) FROM (
				SELECT created_at FROM ideas WHERE author_sub = $1 AND created_at > $2
				ORDER BY created_at DESC LIMIT $3
			) recent`, sub, windowStart, limit).Scan(&n, &oldest)
		if err != nil {
			return err
		}
		if n >= limit && oldest != nil {
			return &RateLimitedError{RetryAfter: oldest.Add(24 * time.Hour).Sub(now)}
		}
		var id int64
		if err := tx.QueryRow(ctx, `INSERT INTO ideas (title, body, author_sub, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $4) RETURNING id`, title, body, sub, now).Scan(&id); err != nil {
			return err
		}
		idea, err = getIdea(ctx, tx, id)
		return err
	})
	return idea, err
}

// CreateApproved creates an admin idea: approved immediately, no author.
func (s *Store) CreateApproved(ctx context.Context, title, body, status string) (Idea, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `INSERT INTO ideas (title, body, status, moderation_state)
		VALUES ($1, $2, $3, 'approved') RETURNING id`, title, body, status).Scan(&id)
	if err != nil {
		return Idea{}, err
	}
	return s.GetIdea(ctx, id)
}

// lockVotable locks an idea row (shared) and applies the voting rules common
// to casting and removing a vote: the idea must be approved, not authored by
// sub, and not shipped/declined (their scores are frozen).
func lockVotable(ctx context.Context, tx pgx.Tx, id int64, sub string) error {
	var mod, status string
	var author *string
	err := tx.QueryRow(ctx, `SELECT moderation_state, status, author_sub FROM ideas WHERE id = $1 FOR SHARE`, id).
		Scan(&mod, &status, &author)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && mod != ModApproved) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if author != nil && *author == sub {
		return ErrOwnIdea
	}
	if status == StatusShipped || status == StatusDeclined {
		return ErrVotingClosed
	}
	return nil
}

// CastVote sets sub's vote on an approved idea to value (+1 or -1). A repeat
// vote updates the single existing row; it never stacks.
func (s *Store) CastVote(ctx context.Context, id int64, sub string, value int) (Idea, error) {
	var idea Idea
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockVotable(ctx, tx, id, sub); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO votes (idea_id, voter_sub, value) VALUES ($1, $2, $3)
			ON CONFLICT (idea_id, voter_sub) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
			id, sub, value); err != nil {
			return err
		}
		var err error
		idea, err = getIdea(ctx, tx, id)
		return err
	})
	return idea, err
}

// RemoveVote deletes sub's vote on an approved idea (idempotent). Like
// CastVote it is refused on shipped/declined ideas.
func (s *Store) RemoveVote(ctx context.Context, id int64, sub string) (Idea, error) {
	var idea Idea
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockVotable(ctx, tx, id, sub); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM votes WHERE idea_id = $1 AND voter_sub = $2`, id, sub); err != nil {
			return err
		}
		var err error
		idea, err = getIdea(ctx, tx, id)
		return err
	})
	return idea, err
}

// Approve moves a pending or rejected idea to approved (idempotent on approved).
func (s *Store) Approve(ctx context.Context, id int64) (Idea, error) {
	return s.transition(ctx, id, ModApproved, ModPending, ModRejected)
}

// Reject moves a pending or approved idea to rejected (idempotent on rejected).
func (s *Store) Reject(ctx context.Context, id int64) (Idea, error) {
	return s.transition(ctx, id, ModRejected, ModPending, ModApproved)
}

func (s *Store) transition(ctx context.Context, id int64, to string, from ...string) (Idea, error) {
	var idea Idea
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var cur string
		err := tx.QueryRow(ctx, `SELECT moderation_state FROM ideas WHERE id = $1 FOR UPDATE`, id).Scan(&cur)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if cur != to {
			ok := false
			for _, f := range from {
				ok = ok || cur == f
			}
			if !ok {
				return conflict(ConflictTransition, "cannot move idea %d from %s to %s", id, cur, to)
			}
			if _, err := tx.Exec(ctx, `UPDATE ideas SET moderation_state = $2, updated_at = now() WHERE id = $1`, id, to); err != nil {
				return err
			}
		}
		idea, err = getIdea(ctx, tx, id)
		return err
	})
	return idea, err
}

// MergeResult reports what a merge did.
type MergeResult struct {
	Target  Idea
	Moved   int64
	Dropped int64
}

// Merge folds source into target in one transaction:
//   - source votes move to target, except when the voter already voted on
//     target (their target vote wins) or the voter is target's author;
//   - remaining source votes are deleted;
//   - ideas previously merged into source are re-pointed to target;
//   - source becomes moderation_state=merged, merged_into_id=target.
func (s *Store) Merge(ctx context.Context, sourceID, targetID int64) (MergeResult, error) {
	if sourceID == targetID {
		return MergeResult{}, conflict(ConflictSelfMerge, "cannot merge an idea into itself")
	}
	var res MergeResult
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Lock both rows in id order to avoid deadlocks between concurrent merges.
		rows, err := tx.Query(ctx, `SELECT id, moderation_state, author_sub FROM ideas
			WHERE id = ANY($1) ORDER BY id FOR UPDATE`, []int64{sourceID, targetID})
		if err != nil {
			return err
		}
		type row struct {
			mod    string
			author *string
		}
		found := map[int64]row{}
		for rows.Next() {
			var id int64
			var r row
			if err := rows.Scan(&id, &r.mod, &r.author); err != nil {
				return err
			}
			found[id] = r
		}
		if err := rows.Err(); err != nil {
			return err
		}
		src, ok1 := found[sourceID]
		tgt, ok2 := found[targetID]
		if !ok1 || !ok2 {
			return ErrNotFound
		}
		if src.mod == ModMerged {
			return conflict(ConflictAlreadyMerged, "idea %d is already merged", sourceID)
		}
		if tgt.mod != ModApproved && tgt.mod != ModPending {
			return conflict(ConflictBadTarget, "merge target %d is %s; it must be approved or pending", targetID, tgt.mod)
		}

		tag, err := tx.Exec(ctx, `INSERT INTO votes (idea_id, voter_sub, value, created_at, updated_at)
			SELECT $2, s.voter_sub, s.value, s.created_at, now() FROM votes s
			WHERE s.idea_id = $1
			  AND s.voter_sub IS DISTINCT FROM $3
			  AND NOT EXISTS (SELECT 1 FROM votes t WHERE t.idea_id = $2 AND t.voter_sub = s.voter_sub)
			ON CONFLICT (idea_id, voter_sub) DO NOTHING`, sourceID, targetID, tgt.author)
		if err != nil {
			return err
		}
		res.Moved = tag.RowsAffected()
		tag, err = tx.Exec(ctx, `DELETE FROM votes WHERE idea_id = $1`, sourceID)
		if err != nil {
			return err
		}
		res.Dropped = tag.RowsAffected() - res.Moved
		if _, err := tx.Exec(ctx, `UPDATE ideas SET merged_into_id = $2, updated_at = now() WHERE merged_into_id = $1`,
			sourceID, targetID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE ideas SET moderation_state = 'merged', merged_into_id = $2, updated_at = now()
			WHERE id = $1`, sourceID, targetID); err != nil {
			return err
		}
		res.Target, err = getIdea(ctx, tx, targetID)
		return err
	})
	return res, err
}

// SetStatus changes an idea's status label.
func (s *Store) SetStatus(ctx context.Context, id int64, status string) (Idea, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE ideas SET status = $2, updated_at = now() WHERE id = $1`, id, status)
	if err != nil {
		return Idea{}, err
	}
	if tag.RowsAffected() == 0 {
		return Idea{}, ErrNotFound
	}
	return s.GetIdea(ctx, id)
}

// Edit updates title and/or body (nil leaves a field unchanged).
func (s *Store) Edit(ctx context.Context, id int64, title, body *string) (Idea, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE ideas SET title = COALESCE($2, title), body = COALESCE($3, body),
		updated_at = now() WHERE id = $1`, id, title, body)
	if err != nil {
		return Idea{}, err
	}
	if tag.RowsAffected() == 0 {
		return Idea{}, ErrNotFound
	}
	return s.GetIdea(ctx, id)
}

// HasMergedError is returned by Delete when other ideas are merged into the
// idea and the caller did not ask for them to be deleted too.
type HasMergedError struct {
	ID     int64
	Merged int
}

func (e *HasMergedError) Error() string {
	return fmt.Sprintf("idea %d has %d merged idea(s)", e.ID, e.Merged)
}

// Delete removes an idea and its votes. If other ideas are merged into it,
// Delete refuses with *HasMergedError unless withMerged is true, in which case
// the merged ideas (and their votes) are deleted in the same transaction.
// The schema backs this up: merged_into_id has no ON DELETE CASCADE (0002).
func (s *Store) Delete(ctx context.Context, id int64, withMerged bool) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Locking the target blocks a concurrent Merge into it (Merge locks
		// both rows FOR UPDATE), so the count below cannot go stale.
		var one int
		err := tx.QueryRow(ctx, `SELECT 1 FROM ideas WHERE id = $1 FOR UPDATE`, id).Scan(&one)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM ideas WHERE merged_into_id = $1`, id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			if !withMerged {
				return &HasMergedError{ID: id, Merged: n}
			}
			if _, err := tx.Exec(ctx, `DELETE FROM ideas WHERE merged_into_id = $1`, id); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `DELETE FROM ideas WHERE id = $1`, id)
		return err
	})
}

// MergedCounts returns, per merge target, how many ideas are merged into it.
func (s *Store) MergedCounts(ctx context.Context) (map[int64]int, error) {
	rows, err := s.pool.Query(ctx, `SELECT merged_into_id, count(*) FROM ideas
		WHERE merged_into_id IS NOT NULL GROUP BY merged_into_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// DeleteUserData implements GDPR erasure for sub: all their votes are deleted
// and their ideas are anonymised (author_sub set to NULL).
func (s *Store) DeleteUserData(ctx context.Context, sub string) (votesDeleted, ideasAnonymised int64, err error) {
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM votes WHERE voter_sub = $1`, sub)
		if err != nil {
			return err
		}
		votesDeleted = tag.RowsAffected()
		tag, err = tx.Exec(ctx, `UPDATE ideas SET author_sub = NULL, updated_at = now() WHERE author_sub = $1`, sub)
		if err != nil {
			return err
		}
		ideasAnonymised = tag.RowsAffected()
		return nil
	})
	return votesDeleted, ideasAnonymised, err
}
