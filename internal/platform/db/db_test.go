package db_test

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Romain-Badino/Avanti/internal/platform/db"
)

// Ces tests couvrent les chemins d'erreur d'Open qui ne demandent aucun
// PostgreSQL réel — le chemin nominal est exercé par les tests d'intégration
// de internal/platform/migrate.

func TestOpenRejectsInvalidDSN(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		dsn  string
	}{
		{name: "port non numérique", dsn: "postgres://avanti:mdp@localhost:pas-un-port/avanti"},
		{name: "paire clé-valeur malformée", dsn: "ceci n'est pas une chaîne de connexion"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			pool, err := db.Open(t.Context(), discardLogger(), tc.dsn, time.Second)
			if err == nil {
				pool.Close()
				t.Fatalf("Open(%q) : erreur attendue, obtenu nil", tc.dsn)
			}
			if !strings.Contains(err.Error(), "chaîne de connexion PostgreSQL invalide") {
				t.Errorf("Open(%q) : erreur %q, attendu une mention de chaîne invalide", tc.dsn, err)
			}
		})
	}
}

func TestOpenGivesUpWhenDatabaseUnreachable(t *testing.T) {
	t.Parallel()

	// Le port 1 est réservé et fermé : la connexion échoue immédiatement, et
	// Open doit réessayer jusqu'à épuiser le délai plutôt que rendre la main
	// au premier refus.
	const dsn = "postgres://avanti:mdp@127.0.0.1:1/avanti?sslmode=disable"

	start := time.Now()
	pool, err := db.Open(t.Context(), discardLogger(), dsn, 600*time.Millisecond)
	if err == nil {
		pool.Close()
		t.Fatal("Open vers un port fermé : erreur attendue, obtenu nil")
	}
	if !strings.Contains(err.Error(), "injoignable") {
		t.Errorf("Open vers un port fermé : erreur %q, attendu une mention d'injoignabilité", err)
	}
	if elapsed := time.Since(start); elapsed < 500*time.Millisecond {
		t.Errorf("Open a abandonné en %s : attendu au moins ~600ms de tentatives", elapsed)
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}
