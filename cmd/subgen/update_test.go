package main

import (
	"testing"
)

func TestParseVersion(t *testing.T) {
	tests := []struct {
		input                    string
		expectedMaj, expectedMin int
		expectedPat              int
	}{
		{"0.17.0", 0, 17, 0},
		{"v1.2.3", 1, 2, 3},
		{"v2.0", 2, 0, 0},
		{"10", 10, 0, 0},
	}

	for _, test := range tests {
		maj, min, pat := parseVersion(test.input)
		if maj != test.expectedMaj || min != test.expectedMin || pat != test.expectedPat {
			t.Errorf("parseVersion(%q) = (%d, %d, %d), expected (%d, %d, %d)",
				test.input, maj, min, pat, test.expectedMaj, test.expectedMin, test.expectedPat)
		}
	}
}

func TestIsNewer(t *testing.T) {
	tests := []struct {
		latest, current string
		expected        bool
	}{
		{"0.17.1", "0.17.0", true},
		{"1.0.0", "0.17.0", true},
		{"0.18.0", "0.17.5", true},
		{"0.17.0", "0.17.0", false},
		{"0.16.9", "0.17.0", false},
		{"v0.17.1", "v0.17.0", true},
		{"0.17.0", "v0.17.0", false},
	}

	for _, test := range tests {
		result := isNewer(test.latest, test.current)
		if result != test.expected {
			t.Errorf("isNewer(%q, %q) = %t, expected %t",
				test.latest, test.current, result, test.expected)
		}
	}
}

func TestParseChecksum(t *testing.T) {
	checksums := []byte(`
abcd1234  subgen_darwin_amd64.tar.gz
12345678  subgen_windows_amd64.zip
efgh9101	subgen_linux_arm64.tar.gz
`)

	tests := []struct {
		filename string
		expected string
		err      bool
	}{
		{"subgen_darwin_amd64.tar.gz", "abcd1234", false},
		{"subgen_windows_amd64.zip", "12345678", false},
		{"subgen_linux_arm64.tar.gz", "efgh9101", false},
		{"non_existent.tar.gz", "", true},
	}

	for _, test := range tests {
		res, err := parseChecksum(checksums, test.filename)
		if (err != nil) != test.err {
			t.Errorf("parseChecksum for %s: expected err=%t, got err=%v", test.filename, test.err, err)
		}
		if res != test.expected {
			t.Errorf("parseChecksum for %s: expected %q, got %q", test.filename, test.expected, res)
		}
	}
}
