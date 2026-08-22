package library

import (
	"context"
	"errors"
	"testing"
)

func TestFavorites(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	keep, err := f.upload(t, "keep.jpg", testJPEG(t, 400, 300))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := f.upload(t, "plain.jpg", testJPEG(t, 410, 300))
	if err != nil {
		t.Fatal(err)
	}

	at, err := SetFavorite(ctx, f.vault, keep.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if at == nil {
		t.Fatal("no timestamp came back")
	}

	got, err := Get(ctx, f.vault, keep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Favorite() {
		t.Fatal("the photo is not marked")
	}
	if other, _ := Get(ctx, f.vault, plain.ID); other.Favorite() {
		t.Fatal("marking one photo marked another")
	}

	if n, err := FavoriteCount(ctx, f.vault); err != nil || n != 1 {
		t.Fatalf("FavoriteCount = %d (%v), want 1", n, err)
	}

	// The filter is what the "only favourites" view rests on.
	list, err := List(ctx, f.vault, ListOptions{FavoritesOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != keep.ID {
		t.Fatalf("filtered list = %v", ids2list(list))
	}
	// And without it, everything is still there.
	if all, _ := List(ctx, f.vault, ListOptions{}); len(all) != 2 {
		t.Fatalf("unfiltered list has %d items, want 2", len(all))
	}

	if _, err := SetFavorite(ctx, f.vault, keep.ID, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := Get(ctx, f.vault, keep.ID); got.Favorite() {
		t.Fatal("the mark did not come off")
	}
}

// Two taps, or two devices, must not be an error -- and must not move the
// timestamp, or "when did I favourite this" stops being true.
func TestFavoritingTwiceKeepsTheFirstTime(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m, err := f.upload(t, "a.jpg", testJPEG(t, 400, 300))
	if err != nil {
		t.Fatal(err)
	}

	first, err := SetFavorite(ctx, f.vault, m.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := SetFavorite(ctx, f.vault, m.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Equal(*second) {
		t.Fatalf("timestamp moved from %s to %s", first, second)
	}

	// Unmarking twice is likewise harmless.
	if _, err := SetFavorite(ctx, f.vault, m.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := SetFavorite(ctx, f.vault, m.ID, false); err != nil {
		t.Fatal(err)
	}
}

// A trashed photo is out of the library as far as everything else is concerned.
func TestATrashedPhotoCannotBeFavorited(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m, err := f.upload(t, "a.jpg", testJPEG(t, 400, 300))
	if err != nil {
		t.Fatal(err)
	}
	if err := Trash(ctx, f.vault, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := SetFavorite(ctx, f.vault, m.ID, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// A favourite that goes to the trash must not show up in the favourites view,
// and must still be a favourite when it comes back.
func TestFavoriteSurvivesTheTrash(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m, err := f.upload(t, "a.jpg", testJPEG(t, 400, 300))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SetFavorite(ctx, f.vault, m.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := Trash(ctx, f.vault, m.ID); err != nil {
		t.Fatal(err)
	}

	if list, _ := List(ctx, f.vault, ListOptions{FavoritesOnly: true}); len(list) != 0 {
		t.Fatalf("a trashed favourite is still listed: %v", ids2list(list))
	}
	if n, _ := FavoriteCount(ctx, f.vault); n != 0 {
		t.Fatalf("FavoriteCount = %d while the photo is in the trash", n)
	}

	if err := Restore(ctx, f.vault, m.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := Get(ctx, f.vault, m.ID); !got.Favorite() {
		t.Fatal("restoring lost the mark")
	}
}

func TestFavoritesAreScopedToTheirUser(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m, err := f.upload(t, "mine.jpg", testJPEG(t, 400, 300))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.vault.DB.ExecContext(ctx,
		`INSERT INTO users(id, username, created_at) VALUES(2,'eve',unixepoch())`); err != nil {
		t.Fatal(err)
	}
	eve := Vault{DB: f.vault.DB, DEK: f.vault.DEK, UserID: 2}

	if _, err := SetFavorite(ctx, eve, m.ID, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("another user could mark the photo: %v", err)
	}
	if got, _ := Get(ctx, f.vault, m.ID); got.Favorite() {
		t.Error("the photo was marked anyway")
	}
}

func ids2list(list []*Media) []int64 {
	out := make([]int64, 0, len(list))
	for _, m := range list {
		out = append(out, m.ID)
	}
	return out
}
