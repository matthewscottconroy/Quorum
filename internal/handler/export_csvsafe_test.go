package handler

import "testing"

func TestCSVSafe(t *testing.T) {
	cases := map[string]string{
		"":                   "",
		"Ada Lovelace":       "Ada Lovelace",
		"=1+1":               "'=1+1",
		"+cmd":               "'+cmd",
		"-2":                 "'-2",
		"@SUM(A1)":           "'@SUM(A1)",
		"\tTabbed":           "'\tTabbed",
		"\rReturn":           "'\rReturn",
		"normal@example.com": "normal@example.com",
		"O'Brien":            "O'Brien",
	}
	for in, want := range cases {
		if got := csvSafe(in); got != want {
			t.Errorf("csvSafe(%q) = %q, want %q", in, got, want)
		}
	}
}
