package library

import (
	"context"
	"errors"
	"testing"
)

func mustAlbum(t *testing.T, f *fixture, title string, parent *int64) *Album {
	t.Helper()
	a, err := CreateAlbum(context.Background(), f.vault, title, "", parent)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func albumByID(albums []*Album, id int64) *Album {
	for _, a := range albums {
		if a.ID == id {
			return a
		}
	}
	return nil
}

func TestSubAlbums(t *testing.T) {
	f := newMetadataFixture(t)
	ctx := context.Background()

	croatia := mustAlbum(t, f, "Croatia", nil)
	y2024 := mustAlbum(t, f, "2024", &croatia.ID)
	y2025 := mustAlbum(t, f, "2025", &croatia.ID)

	albums, err := ListAlbums(ctx, f.vault)
	if err != nil {
		t.Fatal(err)
	}
	if got := albumByID(albums, y2024.ID); got == nil || got.ParentID == nil || *got.ParentID != croatia.ID {
		t.Fatalf("2024 is not under Croatia: %+v", got)
	}
	if got := albumByID(albums, croatia.ID); got.ParentID != nil {
		t.Fatal("Croatia should be at the top level")
	}

	// The breadcrumb reads root first, because that is the order it is shown in.
	path, err := AlbumPath(ctx, f.vault, y2025.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(path) != 2 || path[0].ID != croatia.ID || path[1].ID != y2025.ID {
		t.Fatalf("path = %v", titles(path))
	}
}

// Without this check, dragging a parent into its own child detaches the whole
// branch: it still exists, still holds photos, and is reachable from nowhere.
func TestAnAlbumCannotContainItself(t *testing.T) {
	f := newMetadataFixture(t)
	ctx := context.Background()

	croatia := mustAlbum(t, f, "Croatia", nil)
	y2024 := mustAlbum(t, f, "2024", &croatia.ID)
	july := mustAlbum(t, f, "July", &y2024.ID)

	if err := SetAlbumParent(ctx, f.vault, croatia.ID, &croatia.ID); !errors.Is(err, ErrAlbumCycle) {
		t.Errorf("an album became its own parent: %v", err)
	}
	// One level down.
	if err := SetAlbumParent(ctx, f.vault, croatia.ID, &y2024.ID); !errors.Is(err, ErrAlbumCycle) {
		t.Errorf("a parent moved into its child: %v", err)
	}
	// And two, which a naive check comparing only the direct parent misses.
	if err := SetAlbumParent(ctx, f.vault, croatia.ID, &july.ID); !errors.Is(err, ErrAlbumCycle) {
		t.Errorf("a parent moved into its grandchild: %v", err)
	}

	// The tree must be untouched by the refusals.
	path, err := AlbumPath(ctx, f.vault, july.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(path) != 3 {
		t.Fatalf("path = %v, want three levels", titles(path))
	}
}

func TestMovingAnAlbumToTheTopLevel(t *testing.T) {
	f := newMetadataFixture(t)
	ctx := context.Background()

	croatia := mustAlbum(t, f, "Croatia", nil)
	y2024 := mustAlbum(t, f, "2024", &croatia.ID)

	if err := SetAlbumParent(ctx, f.vault, y2024.ID, nil); err != nil {
		t.Fatal(err)
	}
	a, err := GetAlbum(ctx, f.vault, y2024.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a.ParentID != nil {
		t.Fatal("the album is still nested")
	}
}

// Deleting a parent must take its children, or they are stranded: still full of
// photos, reachable from nowhere.
func TestDeletingAParentTakesTheSubtree(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	croatia := mustAlbum(t, f, "Croatia", nil)
	y2024 := mustAlbum(t, f, "2024", &croatia.ID)
	july := mustAlbum(t, f, "July", &y2024.ID)
	other := mustAlbum(t, f, "Garden", nil)

	m, err := f.upload(t, "beach.jpg", testJPEG(t, 400, 300))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AddToAlbum(ctx, f.vault, july.ID, []int64{m.ID}); err != nil {
		t.Fatal(err)
	}

	n, err := DeleteAlbum(ctx, f.vault, croatia.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("deleted %d albums, want 3", n)
	}
	for _, id := range []int64{croatia.ID, y2024.ID, july.ID} {
		if _, err := GetAlbum(ctx, f.vault, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("album %d survived", id)
		}
	}
	// Unrelated albums are untouched...
	if _, err := GetAlbum(ctx, f.vault, other.ID); err != nil {
		t.Error("an unrelated album was deleted too")
	}
	// ...and no photograph was harmed.
	if _, err := Get(ctx, f.vault, m.ID); err != nil {
		t.Fatal("deleting an album deleted a photo")
	}
}

// A parent that holds only sub-albums would otherwise show an empty tile.
//
// The cover comes from the FIRST sub-album, which is the one shown directly
// beneath the parent's tile -- not the newest photo somewhere under it, which
// looks arbitrary until you go hunting for where it came from.
func TestCoverFallsBackToTheFirstSubAlbum(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	croatia := mustAlbum(t, f, "Croatia", nil)
	y2024 := mustAlbum(t, f, "2024", &croatia.ID)
	y2025 := mustAlbum(t, f, "2025", &croatia.ID)

	older, err := f.upload(t, "2024.jpg", testJPEG(t, 400, 300))
	if err != nil {
		t.Fatal(err)
	}
	newer, err := f.upload(t, "2025.jpg", testJPEG(t, 410, 300))
	if err != nil {
		t.Fatal(err)
	}
	// Make the ordering explicit rather than relying on upload order.
	if _, err := f.vault.DB.ExecContext(ctx,
		`UPDATE media SET taken_at = ? WHERE id = ?`, 1700000000, older.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.vault.DB.ExecContext(ctx,
		`UPDATE media SET taken_at = ? WHERE id = ?`, 1800000000, newer.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := AddToAlbum(ctx, f.vault, y2024.ID, []int64{older.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := AddToAlbum(ctx, f.vault, y2025.ID, []int64{newer.ID}); err != nil {
		t.Fatal(err)
	}

	albums, err := ListAlbums(ctx, f.vault)
	if err != nil {
		t.Fatal(err)
	}
	parent := albumByID(albums, croatia.ID)
	if parent.Cover == nil {
		t.Fatal("the parent album has no cover at all")
	}
	// 2024 sits before 2025, so its photo is the one on the parent -- even
	// though the photo in 2025 is newer.
	if parent.Cover.ID != older.ID {
		t.Fatalf("cover = %s, want the photo from the first sub-album", parent.Cover.Filename)
	}
	// And the count is the whole subtree, not the parent's own zero.
	if parent.ItemCount != 0 {
		t.Fatalf("ItemCount = %d, the parent holds no photos itself", parent.ItemCount)
	}
	if parent.TotalCount != 2 {
		t.Fatalf("TotalCount = %d, want 2", parent.TotalCount)
	}
}

func TestAnExplicitCoverWins(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	album := mustAlbum(t, f, "Garden", nil)
	first, err := f.upload(t, "a.jpg", testJPEG(t, 400, 300))
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.upload(t, "b.jpg", testJPEG(t, 410, 300))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AddToAlbum(ctx, f.vault, album.ID, []int64{first.ID, second.ID}); err != nil {
		t.Fatal(err)
	}
	if err := SetAlbumCover(ctx, f.vault, album.ID, &first.ID); err != nil {
		t.Fatal(err)
	}

	albums, _ := ListAlbums(ctx, f.vault)
	if got := albumByID(albums, album.ID); got.Cover == nil || got.Cover.ID != first.ID {
		t.Fatalf("the chosen cover was ignored: %+v", got.Cover)
	}

	// A cover that lands in the trash must not leave a hole; the automatic
	// choice takes over.
	if err := Trash(ctx, f.vault, first.ID); err != nil {
		t.Fatal(err)
	}
	albums, _ = ListAlbums(ctx, f.vault)
	got := albumByID(albums, album.ID)
	if got.Cover == nil {
		t.Fatal("trashing the cover left the album with none")
	}
	if got.Cover.ID != second.ID {
		t.Fatalf("cover = %d, want the remaining photo", got.Cover.ID)
	}
}

func TestAlbumTreeIsScopedToItsUser(t *testing.T) {
	f := newMetadataFixture(t)
	ctx := context.Background()
	croatia := mustAlbum(t, f, "Croatia", nil)

	if _, err := f.vault.DB.ExecContext(ctx,
		`INSERT INTO users(id, username, created_at) VALUES(2,'eve',unixepoch())`); err != nil {
		t.Fatal(err)
	}
	eve := Vault{DB: f.vault.DB, DEK: f.vault.DEK, UserID: 2}

	if err := SetAlbumParent(ctx, eve, croatia.ID, nil); !errors.Is(err, ErrNotFound) {
		t.Error("another user could move the album")
	}
	if _, err := AlbumPath(ctx, eve, croatia.ID); !errors.Is(err, ErrNotFound) {
		t.Error("another user could read the path")
	}
	if _, err := DeleteAlbum(ctx, eve, croatia.ID); !errors.Is(err, ErrNotFound) {
		t.Error("another user could delete the album")
	}
	if albums, _ := ListAlbums(ctx, eve); len(albums) != 0 {
		t.Errorf("another user sees %d albums", len(albums))
	}
}

func titles(albums []*Album) []string {
	out := make([]string, 0, len(albums))
	for _, a := range albums {
		out = append(out, a.Title)
	}
	return out
}

// An album described one way as a tile and another way when opened is the same
// class of bug as a stale count: the number on screen depends on which request
// happened to fetch it. GetAlbum used to read a single row, so a parent whose
// photos all live in sub-albums came back with no cover and a total of zero.
func TestOneAlbumReadsTheSameThroughBothEndpoints(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	croatia := mustAlbum(t, f, "Croatia", nil)
	y2024 := mustAlbum(t, f, "2024", &croatia.ID)

	photo, err := f.upload(t, "beach.jpg", testJPEG(t, 400, 300))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AddToAlbum(ctx, f.vault, y2024.ID, []int64{photo.ID}); err != nil {
		t.Fatal(err)
	}

	listed, err := ListAlbums(ctx, f.vault)
	if err != nil {
		t.Fatal(err)
	}
	fromList := albumByID(listed, croatia.ID)

	fetched, err := GetAlbum(ctx, f.vault, croatia.ID)
	if err != nil {
		t.Fatal(err)
	}

	if fetched.TotalCount != fromList.TotalCount {
		t.Fatalf("TotalCount = %d via GetAlbum, %d via ListAlbums",
			fetched.TotalCount, fromList.TotalCount)
	}
	if fetched.Cover == nil {
		t.Fatal("GetAlbum returned no cover, ListAlbums resolved one")
	}
	if fetched.Cover.ID != fromList.Cover.ID {
		t.Fatalf("cover = %d via GetAlbum, %d via ListAlbums",
			fetched.Cover.ID, fromList.Cover.ID)
	}
	// And the inherited answer is the real one, not the parent's own empty row.
	if fetched.TotalCount != 1 || fetched.ItemCount != 0 {
		t.Fatalf("ItemCount = %d, TotalCount = %d; want 0 and 1",
			fetched.ItemCount, fetched.TotalCount)
	}
}

// titlesAt reads one level back in the order it would be displayed in.
func titlesAt(t *testing.T, f *fixture, parent *int64) []string {
	t.Helper()
	albums, err := ListAlbums(context.Background(), f.vault)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, a := range albums {
		switch {
		case parent == nil && a.ParentID == nil:
			out = append(out, a.Title)
		case parent != nil && a.ParentID != nil && *a.ParentID == *parent:
			out = append(out, a.Title)
		}
	}
	return out
}

func TestAlbumsAreOrderedByHand(t *testing.T) {
	f := newMetadataFixture(t)
	ctx := context.Background()

	// Created in this order, so this is the order they start in.
	alps := mustAlbum(t, f, "Alps", nil)
	mustAlbum(t, f, "Bornholm", nil)
	croatia := mustAlbum(t, f, "Croatia", nil)

	if got := titlesAt(t, f, nil); !equalTitles(got, []string{"Alps", "Bornholm", "Croatia"}) {
		t.Fatalf("initial order = %v", got)
	}

	// Croatia to the very front: no neighbour after it, Alps before it.
	if err := MoveAlbumTo(ctx, f.vault, croatia.ID, nil, &alps.ID); err != nil {
		t.Fatal(err)
	}
	if got := titlesAt(t, f, nil); !equalTitles(got, []string{"Croatia", "Alps", "Bornholm"}) {
		t.Fatalf("after moving Croatia to the front = %v", got)
	}

	// And back to the end. Neither neighbour given means "past everything",
	// which is what a drop in the empty space after the last tile looks like.
	if err := MoveAlbumTo(ctx, f.vault, croatia.ID, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := titlesAt(t, f, nil); !equalTitles(got, []string{"Alps", "Bornholm", "Croatia"}) {
		t.Fatalf("after moving Croatia back to the end = %v", got)
	}
}

func TestAlbumOrderIsPerLevel(t *testing.T) {
	f := newMetadataFixture(t)
	ctx := context.Background()

	croatia := mustAlbum(t, f, "Croatia", nil)
	y2024 := mustAlbum(t, f, "2024", &croatia.ID)
	y2025 := mustAlbum(t, f, "2025", &croatia.ID)

	if err := MoveAlbumTo(ctx, f.vault, y2025.ID, nil, &y2024.ID); err != nil {
		t.Fatal(err)
	}
	if got := titlesAt(t, f, &croatia.ID); !equalTitles(got, []string{"2025", "2024"}) {
		t.Fatalf("sub-album order = %v", got)
	}

	// A neighbour from another level describes a position that does not exist
	// here; dropping the album somewhere else instead would be worse than
	// refusing.
	if err := MoveAlbumTo(ctx, f.vault, y2024.ID, &croatia.ID, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("moving next to a non-sibling: %v, want ErrNotFound", err)
	}
}

// Re-parenting has to hand out a fresh position: the old one was ordered
// against albums this one no longer sits beside.
func TestReparentedAlbumLandsAtTheEnd(t *testing.T) {
	f := newMetadataFixture(t)
	ctx := context.Background()

	croatia := mustAlbum(t, f, "Croatia", nil)
	mustAlbum(t, f, "2024", &croatia.ID)
	mustAlbum(t, f, "2025", &croatia.ID)
	stray := mustAlbum(t, f, "Aarhus", nil)

	if err := SetAlbumParent(ctx, f.vault, stray.ID, &croatia.ID); err != nil {
		t.Fatal(err)
	}
	if got := titlesAt(t, f, &croatia.ID); !equalTitles(got, []string{"2024", "2025", "Aarhus"}) {
		t.Fatalf("after re-parenting = %v", got)
	}
}

func equalTitles(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
