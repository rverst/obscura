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
func TestCoverFallsBackToTheNewestSubAlbum(t *testing.T) {
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
	if parent.Cover.ID != newer.ID {
		t.Fatalf("cover = %s, want the newest photo beneath it", parent.Cover.Filename)
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
