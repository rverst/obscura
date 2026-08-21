package order

import (
	"errors"
	"math/rand"
	"sort"
	"testing"
)

func mustBetween(t *testing.T, prev, next string) string {
	t.Helper()
	k, err := Between(prev, next)
	if err != nil {
		t.Fatalf("Between(%q, %q): %v", prev, next, err)
	}
	if prev != "" && k <= prev {
		t.Fatalf("Between(%q, %q) = %q, which does not sort after %q", prev, next, k, prev)
	}
	if next != "" && k >= next {
		t.Fatalf("Between(%q, %q) = %q, which does not sort before %q", prev, next, k, next)
	}
	if err := Validate(k); err != nil {
		t.Fatalf("Between(%q, %q) produced an invalid key %q: %v", prev, next, k, err)
	}
	return k
}

func TestEmptyAlbum(t *testing.T) {
	k := mustBetween(t, "", "")
	if k != First {
		t.Fatalf("first key = %q, want %q", k, First)
	}
}

func TestAppendAndPrepend(t *testing.T) {
	first := mustBetween(t, "", "")
	after := mustBetween(t, first, "")
	before := mustBetween(t, "", first)

	if !(before < first && first < after) {
		t.Fatalf("ordering broken: %q < %q < %q", before, first, after)
	}
}

func TestInsertBetween(t *testing.T) {
	a := mustBetween(t, "", "")
	c := mustBetween(t, a, "")
	b := mustBetween(t, a, c)

	got := []string{c, a, b}
	sort.Strings(got)
	if got[0] != a || got[1] != b || got[2] != c {
		t.Fatalf("sorted to %v, want [%q %q %q]", got, a, b, c)
	}
}

// Dragging repeatedly into the same slot is the pathological case: each
// insertion halves the space remaining between two fixed neighbours, so keys
// grow by about one character per log2(62) insertions no matter what the scheme
// does. That is inherent to fractional indexing, not a defect -- and it is
// precisely why Rebalance exists.
//
// This test pins the growth rate rather than an arbitrary ceiling, so a change
// that made it worse would be caught while the expected behaviour is not
// reported as a failure.
func TestRepeatedInsertionAtTheSamePointGrowsPredictably(t *testing.T) {
	lo := mustBetween(t, "", "")
	hi := mustBetween(t, lo, "")

	const n = 500
	prev := hi
	for i := 0; i < n; i++ {
		prev = mustBetween(t, lo, prev)
	}

	// Roughly n / log2(62) characters, plus the integer part. Anything much
	// beyond that means the scheme stopped bisecting efficiently.
	limit := n/5 + 16
	if len(prev) > limit {
		t.Fatalf("after %d insertions at one point the key is %d characters (limit %d): %q",
			n, len(prev), limit, prev)
	}
	t.Logf("%d insertions at one point -> key length %d", n, len(prev))
}

// Rebalance is the remedy for the growth above: it is what an album maintenance
// pass would run after heavy reordering.
func TestRebalanceRecoversFromLongKeys(t *testing.T) {
	lo := mustBetween(t, "", "")
	prev := mustBetween(t, lo, "")
	for i := 0; i < 300; i++ {
		prev = mustBetween(t, lo, prev)
	}
	before := len(prev)

	fresh, err := Rebalance(302)
	if err != nil {
		t.Fatal(err)
	}
	longest := 0
	for _, k := range fresh {
		if len(k) > longest {
			longest = len(k)
		}
	}
	if longest >= before {
		t.Fatalf("rebalanced keys are %d characters, no better than the %d before", longest, before)
	}
	t.Logf("longest key %d -> %d after rebalancing", before, longest)
}

func TestRepeatedAppendStaysShort(t *testing.T) {
	prev := ""
	for i := 0; i < 10_000; i++ {
		prev = mustBetween(t, prev, "")
	}
	// Appending is the common case -- adding photos to the end of an album --
	// and must not degrade. This is what the magnitude prefix exists for.
	if len(prev) > 8 {
		t.Fatalf("after 10k appends the key is %d characters: %q", len(prev), prev)
	}
	t.Logf("10k appends -> key length %d (%q)", len(prev), prev)
}

// The invariant that actually matters: however photos are dragged around, the
// keys must always sort into the order the user sees.
func TestRandomReordersPreserveOrder(t *testing.T) {
	type item struct {
		id  int
		key string
	}

	rnd := rand.New(rand.NewSource(42))
	var list []item

	// Start with 40 items.
	prev := ""
	for i := 0; i < 40; i++ {
		prev = mustBetween(t, prev, "")
		list = append(list, item{id: i, key: prev})
	}

	for move := 0; move < 2000; move++ {
		from := rnd.Intn(len(list))
		moved := list[from]
		list = append(list[:from], list[from+1:]...)

		to := rnd.Intn(len(list) + 1)
		var before, after string
		if to > 0 {
			before = list[to-1].key
		}
		if to < len(list) {
			after = list[to].key
		}
		moved.key = mustBetween(t, before, after)

		list = append(list, item{})
		copy(list[to+1:], list[to:])
		list[to] = moved

		// Sorting by key must reproduce the visible order exactly.
		byKey := make([]item, len(list))
		copy(byKey, list)
		sort.Slice(byKey, func(i, j int) bool { return byKey[i].key < byKey[j].key })
		for i := range list {
			if byKey[i].id != list[i].id {
				t.Fatalf("after move %d the key order diverged from the visible order at %d", move, i)
			}
		}
	}

	longest := 0
	for _, it := range list {
		if len(it.key) > longest {
			longest = len(it.key)
		}
	}
	t.Logf("2000 random moves -> longest key %d characters", longest)
	if longest > 40 {
		t.Fatalf("keys grew to %d characters under random reordering", longest)
	}
}

func TestRejectsReversedNeighbours(t *testing.T) {
	a := mustBetween(t, "", "")
	b := mustBetween(t, a, "")
	if _, err := Between(b, a); !errors.Is(err, ErrNotOrdered) {
		t.Fatalf("want ErrNotOrdered, got: %v", err)
	}
	if _, err := Between(a, a); !errors.Is(err, ErrNotOrdered) {
		t.Fatalf("identical neighbours: want ErrNotOrdered, got: %v", err)
	}
}

func TestRejectsInvalidKeys(t *testing.T) {
	for _, bad := range []string{"!", "0", " ", "a0V0", "zz"} {
		if _, err := Between(bad, ""); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("Between(%q, \"\"): want ErrInvalidKey, got %v", bad, err)
		}
	}
}

// A key ending in the lowest digit has no room beneath it, so the generator
// must never emit one -- otherwise a later insert would be impossible.
func TestNeverEmitsKeyEndingInLowestDigit(t *testing.T) {
	rnd := rand.New(rand.NewSource(7))
	keys := []string{mustBetween(t, "", "")}

	for i := 0; i < 3000; i++ {
		sort.Strings(keys)
		at := rnd.Intn(len(keys) + 1)
		var before, after string
		if at > 0 {
			before = keys[at-1]
		}
		if at < len(keys) {
			after = keys[at]
		}
		k := mustBetween(t, before, after)
		ip, err := integerPart(k)
		if err != nil {
			t.Fatal(err)
		}
		frac := k[len(ip):]
		if len(frac) > 0 && frac[len(frac)-1] == digits[0] {
			t.Fatalf("emitted %q, whose fractional part ends in the lowest digit", k)
		}
		keys = append(keys, k)
	}
}

func TestRebalance(t *testing.T) {
	keys, err := Rebalance(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 100 {
		t.Fatalf("got %d keys, want 100", len(keys))
	}
	for i := 1; i < len(keys); i++ {
		if keys[i-1] >= keys[i] {
			t.Fatalf("keys %d and %d are not ascending: %q >= %q", i-1, i, keys[i-1], keys[i])
		}
	}
	if empty, err := Rebalance(0); err != nil || len(empty) != 0 {
		t.Fatalf("Rebalance(0) = %v, %v", empty, err)
	}
	if _, err := Rebalance(-1); err == nil {
		t.Fatal("negative count was accepted")
	}
}

// Keys are compared by the database as plain strings, so Go's comparison and
// SQLite's have to agree. Both are byte-wise over ASCII here, which is exactly
// why the alphabet is ASCII-ordered.
func TestOrderingIsPlainByteComparison(t *testing.T) {
	keys, err := Rebalance(200)
	if err != nil {
		t.Fatal(err)
	}
	shuffled := make([]string, len(keys))
	copy(shuffled, keys)
	rand.New(rand.NewSource(1)).Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})
	sort.Strings(shuffled)
	for i := range keys {
		if shuffled[i] != keys[i] {
			t.Fatalf("byte order differs from generation order at %d", i)
		}
	}
}
