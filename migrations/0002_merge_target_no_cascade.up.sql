-- Deleting a merge target must not silently delete the ideas merged into it.
-- 0001 declared ideas.merged_into_id ON DELETE CASCADE; replace it with the
-- default NO ACTION so the database refuses the delete while merged ideas
-- still point at the target. The application deletes them explicitly (and
-- only on request) before deleting the target: see store.Delete.
ALTER TABLE ideas DROP CONSTRAINT ideas_merged_into_id_fkey;
ALTER TABLE ideas ADD CONSTRAINT ideas_merged_into_id_fkey
  FOREIGN KEY (merged_into_id) REFERENCES ideas(id);

-- Lookups of "ideas merged into X" (delete guard, admin counts).
CREATE INDEX ideas_merged_into ON ideas (merged_into_id) WHERE merged_into_id IS NOT NULL;
