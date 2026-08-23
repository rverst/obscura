-- Albums carry their own order, so a level can be arranged by hand.

-- The same fractional key the photos in an album already use, for the same
-- reason: a position that can be written without touching its neighbours.
--
-- The default is the empty string rather than a generated key, because a
-- migration cannot mint keys in the scheme the order package defines. Empty
-- sorts before every real key, which is exactly right: albums that predate
-- this column keep their alphabetical order until someone drags one, and
-- anything created afterwards lands at the end. See ListAlbums for the
-- tie-break that makes that work.
ALTER TABLE albums ADD COLUMN sort_key TEXT NOT NULL DEFAULT '';
