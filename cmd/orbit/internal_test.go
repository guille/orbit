package main

import (
	"math/big"
	"strings"
	"testing"
)

func TestNaturalLess(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		// Basic alphabetic
		{"abc", "def", true},
		{"def", "abc", false},
		{"abc", "abc", false},

		// Numeric ordering
		{"task2", "task10", true},
		{"task10", "task2", false},
		{"task10", "task10", false},

		// Leading zeros
		{"task01", "task1", false}, // "01" and "1" are numerically equal, then equal overall
		{"task09", "task10", true},

		// Empty strings
		{"", "a", true},
		{"a", "", false},
		{"", "", false},

		// Pure numeric
		{"2", "10", true},
		{"10", "2", false},
		{"0", "0", false},

		// Mixed chunks
		{"a1b2", "a1b10", true},
		{"a2b", "a10b", true},
		{"z1", "a2", false}, // 'z' > 'a'

		// Different prefix types (digit vs non-digit)
		{"1abc", "abc", true}, // '1' < 'a' in ASCII
		{"abc", "1abc", false},
	}

	for _, tc := range tests {
		got := naturalLess(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("naturalLess(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestSortNatural(t *testing.T) {
	input := []string{"task10", "task2", "task1", "task20", "backup", "task3"}
	sortNatural(input)
	expected := []string{"backup", "task1", "task2", "task3", "task10", "task20"}
	for i, v := range input {
		if v != expected[i] {
			t.Errorf("index %d: got %q, want %q", i, v, expected[i])
		}
	}
}

// FuzzNaturalLess checks ordering axioms: irreflexivity, antisymmetry, and no panics.
func FuzzNaturalLess(f *testing.F) {
	f.Add("task2", "task10")
	f.Add("", "")
	f.Add("abc", "abc")
	f.Add("007", "7")
	f.Add("a1b2", "a1b10")

	f.Fuzz(func(t *testing.T, a, b string) {
		ab := naturalLess(a, b)
		ba := naturalLess(b, a)

		// Irreflexivity
		if naturalLess(a, a) {
			t.Errorf("irreflexivity violated: naturalLess(%q, %q) = true", a, a)
		}

		// Antisymmetry: can't have both a<b and b<a
		if ab && ba {
			t.Errorf("antisymmetry violated: naturalLess(%q, %q) and naturalLess(%q, %q) both true", a, b, b, a)
		}
	})
}

// FuzzSplitChunk checks the concatenation invariant: chunk + rest == input.
func FuzzSplitChunk(f *testing.F) {
	f.Add("task10")
	f.Add("")
	f.Add("123abc456")
	f.Add("000")

	f.Fuzz(func(t *testing.T, s string) {
		chunk, rest := splitChunk(s)

		// Concatenation invariant
		if chunk+rest != s {
			t.Errorf("splitChunk(%q): chunk=%q rest=%q, concatenation != input", s, chunk, rest)
		}

		// Empty input → empty outputs
		if s == "" {
			if chunk != "" || rest != "" {
				t.Errorf("splitChunk(\"\"): expected empty, got chunk=%q rest=%q", chunk, rest)
			}
			return
		}

		// Non-empty input → non-empty chunk
		if chunk == "" {
			t.Errorf("splitChunk(%q): chunk is empty for non-empty input", s)
			return
		}

		// Homogeneity: all chars in chunk are same type (digit or non-digit)
		isDigit := chunk[0] >= '0' && chunk[0] <= '9'
		for i := range len(chunk) {
			d := chunk[i] >= '0' && chunk[i] <= '9'
			if d != isDigit {
				t.Errorf("splitChunk(%q): chunk %q is not homogeneous at byte %d", s, chunk, i)
				break
			}
		}
	})
}

// FuzzCompareNumeric checks antisymmetry and consistency with math/big.
func FuzzCompareNumeric(f *testing.F) {
	f.Add("0", "0")
	f.Add("1", "10")
	f.Add("007", "7")
	f.Add("99999999999999999999", "100000000000000000000")

	f.Fuzz(func(t *testing.T, a, b string) {
		// Only feed digit-only strings to compareNumeric
		if !isDigits(a) || !isDigits(b) {
			t.Skip()
		}

		cmp := compareNumeric(a, b)

		// Result must be -1, 0, or 1
		if cmp != -1 && cmp != 0 && cmp != 1 {
			t.Errorf("compareNumeric(%q, %q) = %d, want -1/0/1", a, b, cmp)
		}

		// Antisymmetry
		rev := compareNumeric(b, a)
		if cmp != -rev && !(cmp == 0 && rev == 0) {
			t.Errorf("antisymmetry: compareNumeric(%q,%q)=%d but compareNumeric(%q,%q)=%d", a, b, cmp, b, a, rev)
		}

		// Oracle: compare against math/big
		bigA, _ := new(big.Int).SetString(strings.TrimLeft(a, "0")+"0"[:1], 10)
		bigB, _ := new(big.Int).SetString(strings.TrimLeft(b, "0")+"0"[:1], 10)
		if bigA == nil {
			bigA = big.NewInt(0)
		}
		if bigB == nil {
			bigB = big.NewInt(0)
		}
		bigA.SetString(a, 10)
		bigB.SetString(b, 10)
		expected := bigA.Cmp(bigB)
		if cmp != expected {
			t.Errorf("compareNumeric(%q, %q) = %d, big.Int says %d", a, b, cmp, expected)
		}
	})
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
