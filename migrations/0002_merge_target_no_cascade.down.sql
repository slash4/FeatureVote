DROP INDEX IF EXISTS ideas_merged_into;
ALTER TABLE ideas DROP CONSTRAINT ideas_merged_into_id_fkey;
ALTER TABLE ideas ADD CONSTRAINT ideas_merged_into_id_fkey
  FOREIGN KEY (merged_into_id) REFERENCES ideas(id) ON DELETE CASCADE;
