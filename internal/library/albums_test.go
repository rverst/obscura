package library

import (
	"context"
	"errors"
	"testing"
)

func ids(items []*AlbumItem) []int64 {
	out := make([]int64, 0, len(items))
	for _, it := range items {
		out = append(out, it.Media.ID)
	}
	return out
}

func equalIDs(a []int64, b ...int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// albumWith uploads n photos and puts them all in a fresh album, in order.
func albumWith(t *testing.T, f *fixture, n int) (*Album, []int64) {
	t.Helper()
	ctx := context.Background()
	album, err := CreateAlbum(ctx, f.vault, "Crete 2019", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	var mediaIDs []int64
	for i := 0; i < n; i++ {
		m, err := f.upload(t, "p.jpg", testJPEG(t, 100+i*10, 100))
		if err != nil {
			t.Fatal(err)
		}
		mediaIDs = append(mediaIDs, m.ID)
	}
	added, err := AddToAlbum(ctx, f.vault, album.ID, mediaIDs)
	if err != nil {
		t.Fatal(err)
	}
	if added != n {
		t.Fatalf("added %d of %d", added, n)
	}
	return album, mediaIDs
}

func TestCreateAndListAlbums(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	a, err := CreateAlbum(ctx, f.vault, "Crete 2019", "Beach holiday", nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == 0 || a.Title != "Crete 2019" {
		t.Fatalf("album = %+v", a)
	}

	if _, err := CreateAlbum(ctx, f.vault, "  ", "", nil); err == nil {
		t.Fatal("a blank title was accepted")
	}

	list, err := ListAlbums(ctx, f.vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ItemCount != 0 {
		t.Fatalf("list = %+v", list)
	}
}

func TestAddAndOrderItems(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	album, mediaIDs := albumWith(t, f, 5)

	items, err := AlbumItems(ctx, f.vault, album.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Added in order, so they come back in order.
	if !equalIDs(ids(items), mediaIDs...) {
		t.Fatalf("order = %v, want %v", ids(items), mediaIDs)
	}
	// Sort keys must be strictly ascending, or the ORDER BY is meaningless.
	for i := 1; i < len(items); i++ {
		if items[i-1].SortKey >= items[i].SortKey {
			t.Fatalf("sort keys not ascending: %q >= %q", items[i-1].SortKey, items[i].SortKey)
		}
	}
}

// The headline feature. Moving a photo must land it exactly between the
// neighbours the client named.
func TestMoveWithinAlbum(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	album, m := albumWith(t, f, 5) // m[0..4] in order

	// Move the last photo to the very front.
	if _, err := MoveInAlbum(ctx, f.vault, album.ID, m[4], nil, &m[0]); err != nil {
		t.Fatal(err)
	}
	items, _ := AlbumItems(ctx, f.vault, album.ID)
	if !equalIDs(ids(items), m[4], m[0], m[1], m[2], m[3]) {
		t.Fatalf("after move to front: %v", ids(items))
	}

	// And into the middle, between m[1] and m[2].
	if _, err := MoveInAlbum(ctx, f.vault, album.ID, m[4], &m[1], &m[2]); err != nil {
		t.Fatal(err)
	}
	items, _ = AlbumItems(ctx, f.vault, album.ID)
	if !equalIDs(ids(items), m[0], m[1], m[4], m[2], m[3]) {
		t.Fatalf("after move to middle: %v", ids(items))
	}

	// And to the very end.
	if _, err := MoveInAlbum(ctx, f.vault, album.ID, m[0], &m[3], nil); err != nil {
		t.Fatal(err)
	}
	items, _ = AlbumItems(ctx, f.vault, album.ID)
	if !equalIDs(ids(items), m[1], m[4], m[2], m[3], m[0]) {
		t.Fatalf("after move to end: %v", ids(items))
	}
}

// The claim that a reorder is one row update is the reason for the whole sort
// key design, so it is worth checking rather than trusting.
func TestMoveTouchesExactlyOneRow(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	album, m := albumWith(t, f, 20)

	before := make(map[int64]string)
	items, _ := AlbumItems(ctx, f.vault, album.ID)
	for _, it := range items {
		before[it.Media.ID] = it.SortKey
	}

	if _, err := MoveInAlbum(ctx, f.vault, album.ID, m[19], &m[1], &m[2]); err != nil {
		t.Fatal(err)
	}

	items, _ = AlbumItems(ctx, f.vault, album.ID)
	changed := 0
	for _, it := range items {
		if before[it.Media.ID] != it.SortKey {
			changed++
		}
	}
	if changed != 1 {
		t.Fatalf("%d sort keys changed, want exactly 1", changed)
	}
}

func TestMoveRejectsForeignNeighbour(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	album, m := albumWith(t, f, 3)

	other, err := f.upload(t, "elsewhere.jpg", testJPEG(t, 640, 480))
	if err != nil {
		t.Fatal(err)
	}
	// A neighbour that is not in this album cannot define a position.
	if _, err := MoveInAlbum(ctx, f.vault, album.ID, m[0], &other.ID, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got: %v", err)
	}
}

func TestAddIsIdempotent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	album, m := albumWith(t, f, 3)

	// Adding a selection that overlaps what is already there should add the
	// remainder, not fail outright.
	extra, err := f.upload(t, "extra.jpg", testJPEG(t, 700, 500))
	if err != nil {
		t.Fatal(err)
	}
	added, err := AddToAlbum(ctx, f.vault, album.ID, []int64{m[0], m[1], extra.ID})
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 {
		t.Fatalf("added %d, want 1 (two were already present)", added)
	}
	items, _ := AlbumItems(ctx, f.vault, album.ID)
	if len(items) != 4 {
		t.Fatalf("%d items, want 4", len(items))
	}
}

// Deleting an album must never delete photographs.
func TestDeleteAlbumKeepsPhotos(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	album, m := albumWith(t, f, 3)

	if _, err := DeleteAlbum(ctx, f.vault, album.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := GetAlbum(ctx, f.vault, album.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("album still readable: %v", err)
	}
	for _, id := range m {
		if _, err := Get(ctx, f.vault, id); err != nil {
			t.Fatalf("photo %d was lost with the album: %v", id, err)
		}
	}
	n, _ := Count(ctx, f.vault)
	if n != 3 {
		t.Fatalf("%d photos left in the library, want 3", n)
	}
}

func TestRemoveFromAlbumKeepsPhoto(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	album, m := albumWith(t, f, 3)

	if err := RemoveFromAlbum(ctx, f.vault, album.ID, m[1]); err != nil {
		t.Fatal(err)
	}
	items, _ := AlbumItems(ctx, f.vault, album.ID)
	if !equalIDs(ids(items), m[0], m[2]) {
		t.Fatalf("items = %v", ids(items))
	}
	if _, err := Get(ctx, f.vault, m[1]); err != nil {
		t.Fatalf("removing from an album deleted the photo: %v", err)
	}
	if err := RemoveFromAlbum(ctx, f.vault, album.ID, m[1]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removing twice: want ErrNotFound, got %v", err)
	}
}

// A photo in the trash must vanish from albums without leaving a gap or a
// dangling entry.
func TestTrashedPhotoLeavesAlbumView(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	album, m := albumWith(t, f, 3)

	if _, err := f.vault.DB.ExecContext(ctx,
		`UPDATE media SET deleted_at = unixepoch() WHERE id = ?`, m[1]); err != nil {
		t.Fatal(err)
	}

	items, err := AlbumItems(ctx, f.vault, album.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !equalIDs(ids(items), m[0], m[2]) {
		t.Fatalf("items = %v, want the trashed photo gone", ids(items))
	}
	a, _ := GetAlbum(ctx, f.vault, album.ID)
	if a.ItemCount != 2 {
		t.Fatalf("ItemCount = %d, want 2", a.ItemCount)
	}
}

func TestUpdateAlbum(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	album, _ := albumWith(t, f, 1)

	if err := UpdateAlbum(ctx, f.vault, album.ID, "Kreta 2019", "Strandurlaub"); err != nil {
		t.Fatal(err)
	}
	got, _ := GetAlbum(ctx, f.vault, album.ID)
	if got.Title != "Kreta 2019" || got.Description != "Strandurlaub" {
		t.Fatalf("album = %+v", got)
	}
	if err := UpdateAlbum(ctx, f.vault, 9999, "x", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got: %v", err)
	}
}

func TestSetCover(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	album, m := albumWith(t, f, 3)

	if err := SetAlbumCover(ctx, f.vault, album.ID, &m[1]); err != nil {
		t.Fatal(err)
	}
	got, _ := GetAlbum(ctx, f.vault, album.ID)
	if got.CoverMediaID == nil || *got.CoverMediaID != m[1] {
		t.Fatalf("cover = %v, want %d", got.CoverMediaID, m[1])
	}

	// A photo outside the album cannot be its cover.
	outside, _ := f.upload(t, "outside.jpg", testJPEG(t, 320, 200))
	if err := SetAlbumCover(ctx, f.vault, album.ID, &outside.ID); err == nil {
		t.Fatal("a photo outside the album was accepted as its cover")
	}

	if err := SetAlbumCover(ctx, f.vault, album.ID, nil); err != nil {
		t.Fatal(err)
	}
	got, _ = GetAlbum(ctx, f.vault, album.ID)
	if got.CoverMediaID != nil {
		t.Fatalf("cover = %v, want nil", got.CoverMediaID)
	}
}

// v1 is single-user, but the queries are scoped and a regression would stay
// invisible until multi-user lands.
func TestAlbumsAreScopedToTheirUser(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	album, m := albumWith(t, f, 2)

	if _, err := f.vault.DB.ExecContext(ctx,
		`INSERT INTO users(id, username, created_at) VALUES(2,'eve',unixepoch())`); err != nil {
		t.Fatal(err)
	}
	eve := Vault{DB: f.vault.DB, DEK: f.vault.DEK, UserID: 2}

	if _, err := GetAlbum(ctx, eve, album.ID); !errors.Is(err, ErrNotFound) {
		t.Error("another user could read the album")
	}
	if _, err := AlbumItems(ctx, eve, album.ID); !errors.Is(err, ErrNotFound) {
		t.Error("another user could list the album's items")
	}
	if _, err := DeleteAlbum(ctx, eve, album.ID); !errors.Is(err, ErrNotFound) {
		t.Error("another user could delete the album")
	}
	if _, err := MoveInAlbum(ctx, eve, album.ID, m[0], nil, nil); !errors.Is(err, ErrNotFound) {
		t.Error("another user could reorder the album")
	}
	list, _ := ListAlbums(ctx, eve)
	if len(list) != 0 {
		t.Errorf("another user sees %d albums", len(list))
	}
}

func TestAddRejectsForeignMedia(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	album, _ := albumWith(t, f, 1)

	// An id that does not belong to this user must be skipped, not added.
	added, err := AddToAlbum(ctx, f.vault, album.ID, []int64{99999})
	if err != nil {
		t.Fatal(err)
	}
	if added != 0 {
		t.Fatalf("added %d unknown media", added)
	}
}
