package library

import (
	"context"
	"testing"
)

func TestSearchByFilename(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// Distinct dimensions per file, or identical content would be deduplicated
	// and the second upload would fail rather than being indexed.
	for i, name := range []string{"DSC01234.jpg", "DSC09999.jpg", "holiday-crete.jpg"} {
		if _, err := f.upload(t, name, testJPEG(t, 200+i*37, 150)); err != nil {
			t.Fatal(err)
		}
	}

	hits, err := Search(ctx, f.vault, "DSC01234", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Filename != "DSC01234.jpg" {
		t.Fatalf("hits = %v", names(hits))
	}

	// Half a filename is what someone actually types.
	hits, _ = Search(ctx, f.vault, "holiday", 50)
	if len(hits) != 1 || hits[0].Filename != "holiday-crete.jpg" {
		t.Fatalf("prefix search: %v", names(hits))
	}
}

// Several words must narrow the result, not widen it.
func TestSearchCombinesWordsWithAnd(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.upload(t, "beach-crete-2019.jpg", testJPEG(t, 300, 200)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.upload(t, "beach-corsica-2021.jpg", testJPEG(t, 320, 200)); err != nil {
		t.Fatal(err)
	}

	if hits, _ := Search(ctx, f.vault, "beach", 50); len(hits) != 2 {
		t.Fatalf("one word: %v", names(hits))
	}
	if hits, _ := Search(ctx, f.vault, "beach crete", 50); len(hits) != 1 {
		t.Fatalf("two words should narrow: %v", names(hits))
	}
}

// FTS5 has its own syntax, so a stray quote or operator in a search box would
// otherwise be a syntax error rather than a search.
func TestSearchSurvivesPunctuation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.upload(t, "DSC01234.jpg", testJPEG(t, 300, 200)); err != nil {
		t.Fatal(err)
	}

	for _, query := range []string{
		`"`, `DSC01234"`, `*`, `DSC AND`, `NEAR(a b)`, `foo OR bar`, `-`, `^`, `(`,
		`DSC01234.jpg`, `  DSC01234  `,
	} {
		if _, err := Search(ctx, f.vault, query, 50); err != nil {
			t.Errorf("query %q returned an error instead of results: %v", query, err)
		}
	}

	// And the meaningful ones still find the photo.
	if hits, _ := Search(ctx, f.vault, `DSC01234.jpg`, 50); len(hits) != 1 {
		t.Fatalf("a filename with a dot found %d", len(hits))
	}
}

func TestSearchIgnoresTrashedMedia(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m, err := f.upload(t, "deleted-photo.jpg", testJPEG(t, 300, 200))
	if err != nil {
		t.Fatal(err)
	}
	if hits, _ := Search(ctx, f.vault, "deleted", 50); len(hits) != 1 {
		t.Fatal("not found before trashing")
	}
	if err := Trash(ctx, f.vault, m.ID); err != nil {
		t.Fatal(err)
	}
	if hits, _ := Search(ctx, f.vault, "deleted", 50); len(hits) != 0 {
		t.Fatalf("a trashed photo is still findable: %v", names(hits))
	}
}

func TestSearchEmptyQuery(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.upload(t, "a.jpg", testJPEG(t, 200, 200)); err != nil {
		t.Fatal(err)
	}
	// An empty box is not a request for the whole library.
	for _, q := range []string{"", "   ", "!!!"} {
		hits, err := Search(ctx, f.vault, q, 50)
		if err != nil {
			t.Fatalf("query %q: %v", q, err)
		}
		if len(hits) != 0 {
			t.Errorf("query %q returned %d results", q, len(hits))
		}
	}
}

func TestSearchIsScopedToItsUser(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.upload(t, "private-photo.jpg", testJPEG(t, 200, 200)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.vault.DB.ExecContext(ctx,
		`INSERT INTO users(id, username, created_at) VALUES(2,'eve',unixepoch())`); err != nil {
		t.Fatal(err)
	}
	eve := Vault{DB: f.vault.DB, DEK: f.vault.DEK, UserID: 2}

	if hits, _ := Search(ctx, eve, "private", 50); len(hits) != 0 {
		t.Fatalf("another user found %d of someone else's photos", len(hits))
	}
}

func names(items []*Media) []string {
	out := make([]string, 0, len(items))
	for _, m := range items {
		out = append(out, m.Filename)
	}
	return out
}
