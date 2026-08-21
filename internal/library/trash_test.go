package library

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rverst/obscura/internal/blob"
)

func TestTrashAndRestore(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m, err := f.upload(t, "holiday.jpg", testJPEG(t, 400, 300))
	if err != nil {
		t.Fatal(err)
	}

	if err := Trash(ctx, f.vault, m.ID); err != nil {
		t.Fatal(err)
	}
	// Gone from the library, but not gone.
	if _, err := Get(ctx, f.vault, m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("trashed photo is still in the library: %v", err)
	}
	if n, _ := Count(ctx, f.vault); n != 0 {
		t.Fatalf("Count = %d, want 0", n)
	}
	if n, _ := TrashCount(ctx, f.vault); n != 1 {
		t.Fatalf("TrashCount = %d, want 1", n)
	}

	items, err := ListTrash(ctx, f.vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != m.ID {
		t.Fatalf("trash = %v", items)
	}
	// The deadline has to be visible, or "30 days" is just a promise.
	if items[0].PurgeAt.Before(time.Now().Add(29 * 24 * time.Hour)) {
		t.Fatalf("PurgeAt = %s, want roughly 30 days out", items[0].PurgeAt)
	}

	if err := Restore(ctx, f.vault, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := Get(ctx, f.vault, m.ID); err != nil {
		t.Fatalf("restored photo is not back: %v", err)
	}
	if n, _ := TrashCount(ctx, f.vault); n != 0 {
		t.Fatalf("TrashCount = %d after restore", n)
	}
}

// Trashing must not destroy album curation. The photo leaves every album while
// it is in the trash and returns to its old position on restore.
func TestTrashKeepsAlbumMembership(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	album, m := albumWith(t, f, 3)

	if err := Trash(ctx, f.vault, m[1]); err != nil {
		t.Fatal(err)
	}
	items, _ := AlbumItems(ctx, f.vault, album.ID)
	if !equalIDs(ids(items), m[0], m[2]) {
		t.Fatalf("album while trashed = %v", ids(items))
	}

	if err := Restore(ctx, f.vault, m[1]); err != nil {
		t.Fatal(err)
	}
	items, _ = AlbumItems(ctx, f.vault, album.ID)
	if !equalIDs(ids(items), m[0], m[1], m[2]) {
		t.Fatalf("album after restore = %v, want the photo back in its old place", ids(items))
	}
}

// Purge is the only place blobs are removed, so it has to actually remove them
// -- otherwise deleting a library frees no space at all.
func TestPurgeRemovesBlobs(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m, err := f.upload(t, "gone.jpg", testJPEG(t, 400, 300))
	if err != nil {
		t.Fatal(err)
	}
	blobIDs := []string{m.BlobOrig, m.BlobPreview, m.BlobThumb}

	if err := Trash(ctx, f.vault, m.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.lib.Purge(ctx, f.vault, m.ID); err != nil {
		t.Fatal(err)
	}

	for _, id := range blobIDs {
		if id == "" {
			continue
		}
		if _, err := f.blobs.Open(f.vault.DEK, id); !errors.Is(err, blob.ErrNotFound) {
			t.Errorf("blob %s survived the purge", id)
		}
	}
	if n, _ := TrashCount(ctx, f.vault); n != 0 {
		t.Fatalf("TrashCount = %d after purge", n)
	}
}

// Purging something that is merely in the library, not in the trash, would make
// deletion a single irreversible step.
func TestPurgeRefusesUntrashedMedia(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m, err := f.upload(t, "safe.jpg", testJPEG(t, 200, 200))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.lib.Purge(ctx, f.vault, m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got: %v", err)
	}
	if _, err := Get(ctx, f.vault, m.ID); err != nil {
		t.Fatal("the photo was purged anyway")
	}
}

func TestPurgeExpiredOnlyTakesTheOverdue(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	recent, err := f.upload(t, "recent.jpg", testJPEG(t, 300, 200))
	if err != nil {
		t.Fatal(err)
	}
	old, err := f.upload(t, "old.jpg", testJPEG(t, 320, 240))
	if err != nil {
		t.Fatal(err)
	}
	if err := Trash(ctx, f.vault, recent.ID); err != nil {
		t.Fatal(err)
	}
	if err := Trash(ctx, f.vault, old.ID); err != nil {
		t.Fatal(err)
	}
	// Backdate one past the retention window.
	past := time.Now().Add(-TrashRetention - time.Hour).Unix()
	if _, err := f.vault.DB.ExecContext(ctx,
		`UPDATE media SET deleted_at = ? WHERE id = ?`, past, old.ID); err != nil {
		t.Fatal(err)
	}

	n, err := f.lib.PurgeExpired(ctx, f.vault)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("purged %d, want 1", n)
	}
	items, _ := ListTrash(ctx, f.vault)
	if len(items) != 1 || items[0].ID != recent.ID {
		t.Fatalf("trash = %v, want only the recent one left", ids2(items))
	}
}

func TestEmptyTrash(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		m, err := f.upload(t, "p.jpg", testJPEG(t, 100+i*10, 100))
		if err != nil {
			t.Fatal(err)
		}
		if err := Trash(ctx, f.vault, m.ID); err != nil {
			t.Fatal(err)
		}
	}
	n, err := f.lib.EmptyTrash(ctx, f.vault)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("emptied %d, want 3", n)
	}
	if c, _ := TrashCount(ctx, f.vault); c != 0 {
		t.Fatalf("TrashCount = %d", c)
	}
}

// The unique index on (user_id, sha256) covers trashed rows, so this would
// otherwise fail on a constraint violation -- which looks like a broken upload
// rather than the photo already being there.
func TestReuploadingATrashedPhotoRestoresIt(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	content := testJPEG(t, 500, 400)

	first, err := f.upload(t, "again.jpg", content)
	if err != nil {
		t.Fatal(err)
	}
	if err := Trash(ctx, f.vault, first.ID); err != nil {
		t.Fatal(err)
	}

	again, err := f.upload(t, "again.jpg", content)
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("want ErrDuplicate, got: %v", err)
	}
	if again == nil || again.ID != first.ID {
		t.Fatal("the restored photo is not the original record")
	}
	if _, err := Get(ctx, f.vault, first.ID); err != nil {
		t.Fatalf("the photo was not restored: %v", err)
	}
	if n, _ := TrashCount(ctx, f.vault); n != 0 {
		t.Fatalf("TrashCount = %d, want 0", n)
	}
	// And there must be exactly one row, not two.
	if n, _ := Count(ctx, f.vault); n != 1 {
		t.Fatalf("Count = %d, want 1", n)
	}
}

func TestTrashIsScopedToItsUser(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m, err := f.upload(t, "mine.jpg", testJPEG(t, 200, 200))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.vault.DB.ExecContext(ctx,
		`INSERT INTO users(id, username, created_at) VALUES(2,'eve',unixepoch())`); err != nil {
		t.Fatal(err)
	}
	eve := Vault{DB: f.vault.DB, DEK: f.vault.DEK, UserID: 2}

	if err := Trash(ctx, eve, m.ID); !errors.Is(err, ErrNotFound) {
		t.Error("another user could trash the photo")
	}
	if err := Trash(ctx, f.vault, m.ID); err != nil {
		t.Fatal(err)
	}
	if err := Restore(ctx, eve, m.ID); !errors.Is(err, ErrNotFound) {
		t.Error("another user could restore it")
	}
	if err := f.lib.Purge(ctx, eve, m.ID); !errors.Is(err, ErrNotFound) {
		t.Error("another user could purge it")
	}
	if items, _ := ListTrash(ctx, eve); len(items) != 0 {
		t.Errorf("another user sees %d trashed items", len(items))
	}
}

func ids2(items []*TrashedItem) []int64 {
	out := make([]int64, 0, len(items))
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}
