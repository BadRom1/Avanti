package document_test

import (
	"strings"
	"testing"

	"github.com/Romain-Badino/Avanti/internal/document"
)

// Ces tests fixent les FRONTIÈRES exactes du nettoyage des noms de fichiers et
// des rattachements : la valeur limite passe, la valeur juste au-delà est
// refusée, et les caractères au bord des plages retirées restent en place. Ils
// sont nés de la campagne de mutation du lot 10 — les mutants de bornes
// survivaient, faute d'un test qui regarde pile la limite.

func TestNormalizeFileNameBoundsAreExact(t *testing.T) {
	t.Parallel()

	t.Run("longueur limite", func(t *testing.T) {
		t.Parallel()

		exact := strings.Repeat("n", 251) + ".pdf" // 255, la limite
		if _, err := document.NormalizeFileName(exact); err != nil {
			t.Errorf("255 caractères (la limite exacte) doivent passer : %v", err)
		}
		if _, err := document.NormalizeFileName("n" + exact); err == nil {
			t.Error("256 caractères doivent être refusés")
		}
	})

	t.Run("séparateur en tête de chemin", func(t *testing.T) {
		t.Parallel()

		// Le dernier segment vaut aussi quand le séparateur est le PREMIER
		// caractère : « /devis.pdf » est un chemin, pas un nom.
		got, err := document.NormalizeFileName("/devis.pdf")
		if err != nil {
			t.Fatalf("NormalizeFileName(/devis.pdf) échoué : %v", err)
		}
		if got != "devis.pdf" {
			t.Errorf("NormalizeFileName(/devis.pdf) = %q, attendu devis.pdf", got)
		}
	})

	t.Run("l'espace interne survit au nettoyage", func(t *testing.T) {
		t.Parallel()

		// La plage retirée s'arrête juste avant l'espace (0x20) : « devis
		// toiture.pdf » doit rester lisible, seuls les caractères de contrôle
		// partent.
		got, err := document.NormalizeFileName("devis toiture.pdf")
		if err != nil {
			t.Fatalf("NormalizeFileName(devis toiture.pdf) échoué : %v", err)
		}
		if got != "devis toiture.pdf" {
			t.Errorf("NormalizeFileName(devis toiture.pdf) = %q, l'espace interne ne doit pas partir", got)
		}
	})
}

func TestNormalizeTargetIDBoundIsExact(t *testing.T) {
	t.Parallel()

	exact := document.Target{Type: document.TargetDevis, ID: strings.Repeat("x", 255)}
	if _, err := document.NormalizeTarget(exact); err != nil {
		t.Errorf("un identifiant de cible de 255 caractères (la limite exacte) doit passer : %v", err)
	}

	over := document.Target{Type: document.TargetDevis, ID: strings.Repeat("x", 256)}
	if _, err := document.NormalizeTarget(over); err == nil {
		t.Error("un identifiant de cible de 256 caractères doit être refusé")
	}
}

func TestUploadDescriptionBoundIsExact(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	f.upload(t, func(in *document.UploadInput) {
		in.Description = strings.Repeat("d", 2000) // la limite exacte
	})

	over := validInput()
	over.Description = strings.Repeat("d", 2001)
	if _, err := f.service.Upload(t.Context(), over); err == nil {
		t.Error("une description de 2001 caractères doit être refusée")
	}
}
