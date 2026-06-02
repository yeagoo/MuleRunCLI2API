package main

import "testing"

func TestRedactDSN(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"file:/var/lib/cli2api/jobs.db", "file:/var/lib/cli2api/jobs.db"},
		{"/tmp/jobs.db", "/tmp/jobs.db"},
		{"libsql://host.turso.io?authToken=secret123", "libsql://host.turso.io?authToken=%2A%2A%2A"},
		{"libsql://host?authToken=secret&other=v", "libsql://host?authToken=%2A%2A%2A&other=v"},
		{"https://host/db?jwt=abc", "https://host/db?jwt=%2A%2A%2A"},
		{"libsql://user:pw@host?authToken=secret", "libsql://user:%2A%2A%2A@host?authToken=%2A%2A%2A"},
		{"libsql://host", "libsql://host"},
	}
	for _, c := range cases {
		got := redactDSN(c.in)
		if got != c.want {
			t.Errorf("redactDSN(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
