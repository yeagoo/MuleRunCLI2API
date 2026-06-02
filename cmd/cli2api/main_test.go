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
		// Case-insensitive secret matching: regression for review #3.
		{"libsql://host?AuthToken=secret123", "libsql://host?AuthToken=%2A%2A%2A"},
		{"libsql://host?AUTHTOKEN=x&Password=p&JWT=j", "libsql://host?AUTHTOKEN=%2A%2A%2A&JWT=%2A%2A%2A&Password=%2A%2A%2A"},
		// Userinfo: drop ENTIRE userinfo, never preserve the username — the
		// username slot is a common place to put bearer tokens (Turso, GitHub,
		// Bitbucket); preserving it would still leak the secret.
		{"libsql://supersecrettoken@host", "libsql://%2A%2A%2A@host"},
		{"https://apikey:supersecret@host/db", "https://%2A%2A%2A@host/db"},
		{"libsql://user:pw@host?authToken=secret", "libsql://%2A%2A%2A@host?authToken=%2A%2A%2A"},
		{"libsql://host", "libsql://host"},
	}
	for _, c := range cases {
		got := redactDSN(c.in)
		if got != c.want {
			t.Errorf("redactDSN(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
