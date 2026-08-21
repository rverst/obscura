// Package order implements fractional indexing for custom sort orders.
//
// A photo's position in an album is a string, not a number. Moving one photo
// generates a key that sorts between its two new neighbours, so a reorder is a
// single-row UPDATE no matter how large the album is. With consecutive integers
// the same move would renumber every row after the insertion point -- on a Pi,
// with an encrypted database, dragging a photo from position 900 to position 3
// would rewrite 897 rows.
//
// Keys are compared as plain strings, so the database sorts them with an
// ordinary index and no custom collation.
//
// # Key structure
//
// A key is an integer part followed by an optional fractional part. The first
// character of the integer part encodes how long that part is: 'a'..'z' mean
// lengths 2..27 going up, 'Z'..'A' mean lengths 2..27 going down. That magnitude
// prefix is what keeps appending cheap -- the common case of adding photos to
// the end of an album increments an integer instead of repeatedly bisecting
// towards infinity, which would grow keys without bound.
//
// This is the scheme described in Implementing Fractional Indexing by David
// Greenspan and used by the fractional-indexing package. It is reproduced here
// rather than depended on because it is eighty lines and the alternative is a
// dependency in the position-critical path of the headline feature.
package order

import (
	"errors"
	"fmt"
	"strings"
)

// digits is ASCII-ordered, which is what makes plain byte comparison agree with
// the intended ordering.
const digits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// First is the key for the first item in an empty album.
const First = "a0"

// smallestInteger is the lower bound of the integer space; nothing may sit at
// or below it, or prepending would have nowhere left to go.
var smallestInteger = "A" + strings.Repeat("0", 26)

var (
	// ErrNotOrdered reports that the neighbours were given the wrong way round.
	ErrNotOrdered = errors.New("order: keys are not in ascending order")
	// ErrInvalidKey reports a malformed key.
	ErrInvalidKey = errors.New("order: invalid key")
	// ErrExhausted reports that the key space ran out. Reaching this needs
	// something on the order of 62^26 insertions in one direction, so in
	// practice it means a bug rather than a very tidy user.
	ErrExhausted = errors.New("order: key space exhausted")
)

// Between returns a key that sorts strictly after prev and strictly before next.
//
// An empty prev means "before everything"; an empty next means "after
// everything". Both empty yields First.
func Between(prev, next string) (string, error) {
	if prev != "" {
		if err := Validate(prev); err != nil {
			return "", err
		}
	}
	if next != "" {
		if err := Validate(next); err != nil {
			return "", err
		}
	}
	if prev != "" && next != "" && prev >= next {
		return "", fmt.Errorf("%w: %q >= %q", ErrNotOrdered, prev, next)
	}

	switch {
	case prev == "" && next == "":
		return First, nil

	case prev == "":
		ib, err := integerPart(next)
		if err != nil {
			return "", err
		}
		fb := next[len(ib):]
		if ib == smallestInteger {
			// Already at the bottom of the integer space, so the only room left
			// is below the fractional part.
			return ib + midpoint("", fb), nil
		}
		if ib < next {
			return ib, nil
		}
		dec := decrementInteger(ib)
		if dec == "" {
			return "", ErrExhausted
		}
		return dec, nil

	case next == "":
		ia, err := integerPart(prev)
		if err != nil {
			return "", err
		}
		fa := prev[len(ia):]
		if inc := incrementInteger(ia); inc != "" {
			return inc, nil
		}
		return ia + midpoint(fa, ""), nil

	default:
		ia, err := integerPart(prev)
		if err != nil {
			return "", err
		}
		ib, err := integerPart(next)
		if err != nil {
			return "", err
		}
		fa, fb := prev[len(ia):], next[len(ib):]
		if ia == ib {
			return ia + midpoint(fa, fb), nil
		}
		inc := incrementInteger(ia)
		if inc == "" {
			return "", ErrExhausted
		}
		if inc < next {
			return inc, nil
		}
		return ia + midpoint(fa, ""), nil
	}
}

// Validate reports whether a key is well formed.
func Validate(key string) error {
	if key == "" {
		return fmt.Errorf("%w: empty", ErrInvalidKey)
	}
	if key == smallestInteger {
		return fmt.Errorf("%w: %q is the reserved lower bound", ErrInvalidKey, key)
	}
	ip, err := integerPart(key)
	if err != nil {
		return err
	}
	frac := key[len(ip):]
	for i := 0; i < len(frac); i++ {
		if strings.IndexByte(digits, frac[i]) < 0 {
			return fmt.Errorf("%w: %q contains %q", ErrInvalidKey, key, frac[i])
		}
	}
	// A fractional part ending in the lowest digit has no room beneath it, so
	// a later insert before it would be impossible.
	if len(frac) > 0 && frac[len(frac)-1] == digits[0] {
		return fmt.Errorf("%w: %q ends with %q", ErrInvalidKey, key, digits[0])
	}
	return nil
}

// Rebalance assigns evenly spaced keys to n items.
//
// Repeated insertion at the same position lengthens keys one character at a
// time; this is the escape hatch when they get unreasonable. It rewrites every
// row, so it is a maintenance operation, not something a drag triggers.
func Rebalance(n int) ([]string, error) {
	if n < 0 {
		return nil, fmt.Errorf("order: negative count %d", n)
	}
	keys := make([]string, 0, n)
	prev := ""
	for i := 0; i < n; i++ {
		k, err := Between(prev, "")
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
		prev = k
	}
	return keys, nil
}

// --- integer part -----------------------------------------------------------

// integerLength decodes the magnitude prefix.
func integerLength(head byte) int {
	switch {
	case head >= 'a' && head <= 'z':
		return int(head-'a') + 2
	case head >= 'A' && head <= 'Z':
		return int('Z'-head) + 2
	default:
		return -1
	}
}

func integerPart(key string) (string, error) {
	if key == "" {
		return "", fmt.Errorf("%w: empty", ErrInvalidKey)
	}
	n := integerLength(key[0])
	if n < 0 {
		return "", fmt.Errorf("%w: %q has an invalid magnitude prefix %q", ErrInvalidKey, key, key[0])
	}
	if n > len(key) {
		return "", fmt.Errorf("%w: %q is shorter than its prefix claims", ErrInvalidKey, key)
	}
	return key[:n], nil
}

// incrementInteger returns the next integer, or "" when the space is exhausted.
func incrementInteger(x string) string {
	head := x[0]
	digs := []byte(x[1:])

	carry := true
	for i := len(digs) - 1; carry && i >= 0; i-- {
		d := strings.IndexByte(digits, digs[i]) + 1
		if d == len(digits) {
			digs[i] = digits[0]
		} else {
			digs[i] = digits[d]
			carry = false
		}
	}
	if !carry {
		return string(head) + string(digs)
	}

	// The digits overflowed, so the magnitude has to grow.
	switch head {
	case 'Z':
		return First
	case 'z':
		return ""
	}
	h := head + 1
	if h > 'a' {
		digs = append(digs, digits[0])
	} else {
		digs = digs[:len(digs)-1]
	}
	return string(h) + string(digs)
}

// decrementInteger returns the previous integer, or "" when exhausted.
func decrementInteger(x string) string {
	head := x[0]
	digs := []byte(x[1:])

	borrow := true
	for i := len(digs) - 1; borrow && i >= 0; i-- {
		d := strings.IndexByte(digits, digs[i]) - 1
		if d == -1 {
			digs[i] = digits[len(digits)-1]
		} else {
			digs[i] = digits[d]
			borrow = false
		}
	}
	if !borrow {
		return string(head) + string(digs)
	}

	switch head {
	case 'a':
		return "Z" + string(digits[len(digits)-1])
	case 'A':
		return ""
	}
	h := head - 1
	if h < 'Z' {
		digs = append(digs, digits[len(digits)-1])
	} else {
		digs = digs[:len(digs)-1]
	}
	return string(h) + string(digs)
}

// --- fractional part --------------------------------------------------------

// midpoint returns a string strictly between a and b, where an empty b means
// "one" rather than "empty". Both are fractions in base 62.
func midpoint(a, b string) string {
	if b != "" {
		// A shared prefix contributes nothing to the comparison, so peel it off
		// and solve the rest. Missing characters in a count as the lowest
		// digit, which is what makes midpoint("", "0V") descend into the "0"
		// instead of returning it and producing a key with a trailing zero.
		n := 0
		for n < len(b) {
			ca := byte(digits[0])
			if n < len(a) {
				ca = a[n]
			}
			if ca != b[n] {
				break
			}
			n++
		}
		if n > 0 {
			var tailA string
			if n < len(a) {
				tailA = a[n:]
			}
			return b[:n] + midpoint(tailA, b[n:])
		}
	}

	digitA := 0
	if a != "" {
		digitA = strings.IndexByte(digits, a[0])
	}
	digitB := len(digits)
	if b != "" {
		digitB = strings.IndexByte(digits, b[0])
	}

	if digitB-digitA > 1 {
		// There is room for a digit strictly between them.
		mid := (digitA + digitB + 1) / 2
		return string(digits[mid])
	}
	if len(b) > 1 {
		// b has more below its first digit than a does.
		return b[:1]
	}
	// b is a single digit or absent, so descend into a's tail with no upper
	// bound.
	var tail string
	if a != "" {
		tail = a[1:]
	}
	return string(digits[digitA]) + midpoint(tail, "")
}
