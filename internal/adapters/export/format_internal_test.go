package export

import (
	"testing"
	"time"
)

// TestFormatInstantRecadreDansLeFuseauDuServeur fixe la frontière entre les
// deux mises en forme du dossier d'assurance : la date que porte la pièce se
// lit en UTC, l'horodatage d'un règlement dans le fuseau du serveur.
//
// L'enjeu n'est pas cosmétique : c'est la date que lit l'assureur. Une facture
// réglée à 00h30 à Paris a été réglée le 16, pas le 15.
func TestFormatInstantRecadreDansLeFuseauDuServeur(t *testing.T) {
	t.Parallel()

	paris := time.FixedZone("CEST", 2*60*60)
	regle := time.Date(2026, time.August, 15, 22, 30, 0, 0, time.UTC)

	if got := formatInstantIn(regle, paris); got != "16/08/2026" {
		t.Errorf("formatInstantIn(22h30 UTC, +02:00) = %q, attendu 16/08/2026", got)
	}
	// Le fuseau que porte la valeur à l'arrivée — UTC depuis le domaine, local
	// depuis pgx — ne change pas ce qui est écrit dans le dossier.
	if got := formatInstantIn(regle.In(paris), paris); got != "16/08/2026" {
		t.Errorf("formatInstantIn(même instant porté en +02:00) = %q, attendu 16/08/2026", got)
	}
	// La date de la pièce, elle, se lit en UTC : c'est celle qui a été saisie.
	if got := formatDate(time.Date(2026, time.August, 16, 0, 0, 0, 0, time.UTC)); got != "16/08/2026" {
		t.Errorf("formatDate(minuit UTC) = %q, attendu 16/08/2026", got)
	}
	// Une pièce ni réglée ni remboursée laisse la colonne vide.
	if got := formatInstantIn(time.Time{}, paris); got != "" {
		t.Errorf("formatInstantIn(zéro) = %q, attendu vide", got)
	}
}
