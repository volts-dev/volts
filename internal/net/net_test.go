package net

import "testing"

func TestNormalizeBindAddr(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"localhost:8080", ":8080"},
		{"127.0.0.1:8080", ":8080"},
		{"::1:8080", ":8080"},
		{":8080", ":8080"},
		{"0.0.0.0:8080", "0.0.0.0:8080"},
		{"192.168.1.10:8080", "192.168.1.10:8080"},
		{"example.com:8080", "example.com:8080"},
		{"invalid-no-port", "invalid-no-port"},
	}
	for _, c := range cases {
		got := normalizeBindAddr(c.input)
		if got != c.want {
			t.Errorf("normalizeBindAddr(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}
