package logging_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/Romain-Badino/Avanti/internal/platform/config"
	"github.com/Romain-Badino/Avanti/internal/platform/logging"
)

func TestNewJSONFormat(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	logger := logging.New(&out, &config.Config{LogFormat: config.LogJSON, LogLevel: slog.LevelInfo})
	logger.Info("chantier démarré", slog.String("lot", "gros œuvre"))

	var record map[string]any
	if err := json.Unmarshal(out.Bytes(), &record); err != nil {
		t.Fatalf("la sortie n'est pas du JSON : %v (sortie : %q)", err, out.String())
	}
	if record["msg"] != "chantier démarré" {
		t.Errorf("msg = %v", record["msg"])
	}
	if record["lot"] != "gros œuvre" {
		t.Errorf("lot = %v, les attributs doivent être conservés", record["lot"])
	}
}

func TestNewTextFormat(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	logger := logging.New(&out, &config.Config{LogFormat: config.LogText, LogLevel: slog.LevelInfo})
	logger.Info("chantier démarré")

	line := out.String()
	if json.Valid(out.Bytes()) {
		t.Errorf("sortie = %q, le format texte ne doit pas produire du JSON", line)
	}
	if !strings.Contains(line, "chantier démarré") {
		t.Errorf("sortie = %q, doit contenir le message", line)
	}
}

func TestNewHonoursLevel(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		threshold slog.Level
		emitted   slog.Level
		want      bool
	}{
		{name: "debug sous le seuil info", threshold: slog.LevelInfo, emitted: slog.LevelDebug, want: false},
		{name: "info au seuil info", threshold: slog.LevelInfo, emitted: slog.LevelInfo, want: true},
		{name: "debug au seuil debug", threshold: slog.LevelDebug, emitted: slog.LevelDebug, want: true},
		{name: "info sous le seuil error", threshold: slog.LevelError, emitted: slog.LevelInfo, want: false},
		{name: "error au seuil error", threshold: slog.LevelError, emitted: slog.LevelError, want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer
			logger := logging.New(&out, &config.Config{LogFormat: config.LogJSON, LogLevel: tc.threshold})
			logger.Log(t.Context(), tc.emitted, "message")

			if got := out.Len() > 0; got != tc.want {
				t.Errorf("message écrit = %v, attendu %v (sortie : %q)", got, tc.want, out.String())
			}
		})
	}
}

func TestDiscardWritesNothing(t *testing.T) {
	t.Parallel()

	logger := logging.Discard()
	if logger.Enabled(t.Context(), slog.LevelError) {
		t.Error("Discard() doit désactiver tous les niveaux")
	}
}
