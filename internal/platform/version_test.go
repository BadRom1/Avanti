package platform

import (
	"runtime"
	"strings"
	"testing"
)

func TestBuildUsesFallbacksWhenLdflagsAreMissing(t *testing.T) {
	t.Parallel()

	got := Build()

	if got.Version == "" {
		t.Error("Version ne doit jamais être vide")
	}
	if got.Commit == "" {
		t.Error("Commit ne doit jamais être vide")
	}
	if got.Date == "" {
		t.Error("Date ne doit jamais être vide")
	}
	if got.GoVersion != runtime.Version() {
		t.Errorf("GoVersion = %q, attendu %q", got.GoVersion, runtime.Version())
	}
}

func TestBuildInfoStringContainsEveryField(t *testing.T) {
	t.Parallel()

	info := BuildInfo{
		Version:   "1.2.3",
		Commit:    "abcdef0",
		Date:      "2026-01-01T00:00:00Z",
		GoVersion: "go1.26.4",
	}

	line := info.String()

	if !strings.HasPrefix(line, "avanti ") {
		t.Errorf("String() = %q, doit commencer par le nom du binaire", line)
	}
	for _, want := range []string{info.Version, info.Commit, info.Date, info.GoVersion} {
		if !strings.Contains(line, want) {
			t.Errorf("String() = %q, doit contenir %q", line, want)
		}
	}
}

func TestFallback(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		value string
		def   string
		want  string
	}{
		"valeur renseignée":     {value: "v1.0.0", def: "dev", want: "v1.0.0"},
		"valeur vide":           {value: "", def: "dev", want: "dev"},
		"valeur blanche":        {value: "   ", def: "dev", want: "dev"},
		"valeur avec espaces":   {value: " v1.0.0 ", def: "dev", want: " v1.0.0 "},
		"ldflags non substitué": {value: "\t\n", def: "none", want: "none"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := fallback(tc.value, tc.def); got != tc.want {
				t.Errorf("fallback(%q, %q) = %q, attendu %q", tc.value, tc.def, got, tc.want)
			}
		})
	}
}
