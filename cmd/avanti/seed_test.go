package main

import (
	"bytes"
	"strings"
	"testing"
)

// Les tests de `avanti seed` portent sur ce qui se décide *avant* d'ouvrir la
// base : l'aiguillage, la validation des arguments et le refus de la
// production. Le jeu de données lui-même est vérifié par le test d'intégration
// (seed_integration_test.go), contre un PostgreSQL réel.

func TestSeedRejectsMissingSubcommand(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	err := run(t.Context(), []string{"seed"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("« avanti seed » sans sous-commande doit échouer")
	}
	if !strings.Contains(stderr.String(), "demo") {
		t.Errorf("stderr = %q, doit rappeler la sous-commande disponible", stderr.String())
	}
}

func TestSeedRejectsUnknownSubcommand(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	err := run(t.Context(), []string{"seed", "production"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("une sous-commande inconnue doit échouer")
	}
	if !strings.Contains(err.Error(), "production") {
		t.Errorf("erreur = %q, doit citer la sous-commande fautive", err.Error())
	}
}

func TestSeedDemoRequiresAnEmail(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	err := run(t.Context(), []string{"seed", "demo"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("seed demo doit exiger --email")
	}
	if !strings.Contains(err.Error(), "--email") {
		t.Errorf("erreur = %q, doit nommer le drapeau manquant", err.Error())
	}
}

// TestSeedDemoRefusesProduction : le refus tombe sur la seule lecture de la
// configuration, avant toute connexion — la chaîne PostgreSQL ci-dessous ne
// désigne volontairement rien de joignable, et le test doit échouer AVANT
// d'essayer de la joindre.
func TestSeedDemoRefusesProduction(t *testing.T) {
	t.Setenv("AVANTI_ENV", "production")
	t.Setenv("AVANTI_DATABASE_URL", "postgres://avanti:inutilise@127.0.0.1:1/avanti")
	t.Setenv("AVANTI_OAUTH_SECRET", strings.Repeat("k", 44))

	var stdout, stderr bytes.Buffer

	err := run(t.Context(), []string{"seed", "demo", "--email", "romain@exemple.fr"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("seed demo doit refuser AVANTI_ENV=production")
	}
	if !strings.Contains(err.Error(), "production") {
		t.Errorf("erreur = %q, doit expliquer le refus de la production", err.Error())
	}
}

// TestUsageSeedNamesItsGuards : l'aide dit ce que la commande refuse, pour que
// le refus ne soit jamais une surprise.
func TestUsageSeedNamesItsGuards(t *testing.T) {
	t.Parallel()

	var help bytes.Buffer
	usageSeed(&help)

	for _, want := range []string{"demo", "--email", "production", "vide"} {
		if !strings.Contains(help.String(), want) {
			t.Errorf("l'aide de « seed » ne mentionne pas %q", want)
		}
	}
}
