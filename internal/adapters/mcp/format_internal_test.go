package mcp

import (
	"testing"
	"time"
)

// TestFormatInstantRecadreDansLeFuseauDuServeur fixe la frontière entre les
// deux mises en forme rendues à l'agent : une date saisie se relit en UTC, un
// horodatage dans le fuseau du serveur — celui qu'affiche l'interface web, pour
// que les deux ne racontent pas deux jours différents d'un même règlement.
func TestFormatInstantRecadreDansLeFuseauDuServeur(t *testing.T) {
	t.Parallel()

	paris := time.FixedZone("CEST", 2*60*60)
	regle := time.Date(2026, time.August, 15, 22, 30, 0, 0, time.UTC)

	if got := formatInstantIn(regle, paris); got != "2026-08-16" {
		t.Errorf("formatInstantIn(22h30 UTC, +02:00) = %q, attendu 2026-08-16", got)
	}
	// Le fuseau que porte la valeur à l'arrivée — UTC depuis le domaine, local
	// depuis pgx — ne change pas la réponse rendue.
	if got := formatInstantIn(regle.In(paris), paris); got != "2026-08-16" {
		t.Errorf("formatInstantIn(même instant porté en +02:00) = %q, attendu 2026-08-16", got)
	}
	// Une date civile se relit en UTC : c'est celle que l'agent a saisie.
	if got := formatDate(time.Date(2026, time.August, 16, 0, 0, 0, 0, time.UTC)); got != "2026-08-16" {
		t.Errorf("formatDate(minuit UTC) = %q, attendu 2026-08-16", got)
	}
	// « Pas encore » reste la chaîne vide, dans le vocabulaire des domaines.
	if got := formatInstantIn(time.Time{}, paris); got != "" {
		t.Errorf("formatInstantIn(zéro) = %q, attendu vide", got)
	}
}
