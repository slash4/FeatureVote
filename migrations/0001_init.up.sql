-- FeatureVote initial schema.
--
-- Scores are NOT stored: they are computed at read time from votes (SUM/COUNT FILTER per idea).
-- Boards are small (hundreds of ideas, thousands of votes), a computed score cannot drift, and
-- merge / GDPR delete / vote switch recounts are correct by construction with no triggers.
-- Revisit only if list latency matters (then add a counter cache maintained in the same tx).

CREATE TABLE ideas (
  id               BIGSERIAL PRIMARY KEY,
  title            TEXT NOT NULL CHECK (char_length(title) BETWEEN 1 AND 120),
  body             TEXT NOT NULL DEFAULT '' CHECK (char_length(body) <= 2000),
  author_sub       TEXT NULL,       -- NULL = admin-created or GDPR-anonymised
  status           TEXT NOT NULL DEFAULT 'under_review'
                   CHECK (status IN ('under_review','planned','in_progress','shipped','declined')),
  moderation_state TEXT NOT NULL DEFAULT 'pending'
                   CHECK (moderation_state IN ('pending','approved','rejected','merged')),
  merged_into_id   BIGINT NULL REFERENCES ideas(id) ON DELETE CASCADE,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK ((moderation_state = 'merged') = (merged_into_id IS NOT NULL)),
  CHECK (merged_into_id IS NULL OR merged_into_id <> id)
);

CREATE INDEX ideas_moderation_created ON ideas (moderation_state, created_at DESC);
CREATE INDEX ideas_author_created ON ideas (author_sub, created_at DESC) WHERE author_sub IS NOT NULL;

CREATE TABLE votes (
  idea_id    BIGINT NOT NULL REFERENCES ideas(id) ON DELETE CASCADE,
  voter_sub  TEXT NOT NULL,
  value      SMALLINT NOT NULL CHECK (value IN (-1, 1)),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (idea_id, voter_sub)  -- one vote per (idea, user)
);

CREATE INDEX votes_voter ON votes (voter_sub);
