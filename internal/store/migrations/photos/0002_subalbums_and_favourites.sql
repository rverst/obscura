-- Sub-albums and favourites.

-- An album may live inside another: Croatia -> 2024, 2025.
--
-- A nullable parent rather than a separate tree table: the depth here is a
-- handful of levels chosen by one person, and a closure table would be more
-- machinery than the problem has. Cycles are prevented in code, because SQLite
-- has no constraint that can express "not an ancestor of itself".
ALTER TABLE albums ADD COLUMN parent_id INTEGER REFERENCES albums(id);

CREATE INDEX idx_albums_parent ON albums(user_id, parent_id);

-- Favourites, as a timestamp rather than a flag.
--
-- NULL means "not a favourite", which is unambiguous in a way that 0 is not,
-- and it records when -- enough to offer "recently favourited" later without
-- another migration. The partial index keeps it the size of the favourites
-- rather than the size of the library.
ALTER TABLE media ADD COLUMN favorited_at INTEGER;

CREATE INDEX idx_media_favorite ON media(user_id, favorited_at)
    WHERE favorited_at IS NOT NULL;
