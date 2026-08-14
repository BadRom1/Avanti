package devis_test

import (
	"strings"
	"testing"

	"github.com/Romain-Badino/Avanti/internal/devis"
)

// Ces tests fixent les FRONTIÈRES exactes du domaine : la valeur limite passe,
// la valeur juste au-delà est refusée. Ils sont nés de la campagne de mutation
// du lot 10 — les mutants de bornes (> devenu >=) survivaient, faute d'un test
// qui regarde pile la limite. Les longueurs recopiées ici sont celles des
// constantes du domaine (artisan.go, demande.go) ; c'est le prix pour que le
// test échoue si une borne bouge sans que ce soit un choix.

func TestNormalizeArtisanBoundsAreExact(t *testing.T) {
	t.Parallel()

	// 243 caractères de partie locale + « @exemple.fr » (11) = 254, la limite.
	longestEmail := strings.Repeat("a", 243) + "@exemple.fr"

	cases := []struct {
		name  string
		exact devis.Artisan
		over  devis.Artisan
	}{
		{
			name:  "entreprise à 200 caractères",
			exact: devis.Artisan{Entreprise: strings.Repeat("e", 200)},
			over:  devis.Artisan{Entreprise: strings.Repeat("e", 201)},
		},
		{
			name:  "email à 254 caractères",
			exact: devis.Artisan{Entreprise: "Charpentes du Val", Email: longestEmail},
			over:  devis.Artisan{Entreprise: "Charpentes du Val", Email: "a" + longestEmail},
		},
		{
			name:  "téléphone à 40 caractères",
			exact: devis.Artisan{Entreprise: "Charpentes du Val", Telephone: strings.Repeat("0", 40)},
			over:  devis.Artisan{Entreprise: "Charpentes du Val", Telephone: strings.Repeat("0", 41)},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := devis.NormalizeArtisan(tc.exact); err != nil {
				t.Errorf("la limite exacte doit passer : %v", err)
			}
			if _, err := devis.NormalizeArtisan(tc.over); err == nil {
				t.Error("un caractère de trop doit être refusé")
			}
		})
	}
}

func TestMontantValidBoundsAreExact(t *testing.T) {
	t.Parallel()

	cases := []struct {
		montant devis.Montant
		want    bool
	}{
		{montant: 0, want: false},
		{montant: 1, want: true},
		{montant: devis.MaxMontant, want: true},
		{montant: devis.MaxMontant + 1, want: false},
	}

	for _, tc := range cases {
		if got := tc.montant.Valid(); got != tc.want {
			t.Errorf("Montant(%d).Valid() = %t, attendu %t", tc.montant, got, tc.want)
		}
	}
}

func TestDemandeTextBoundsAreExact(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	exact := devis.DemandeInput{
		Lot:         strings.Repeat("l", 120),
		Description: strings.Repeat("d", 4000),
		SentAt:      instantEnvoi,
		By:          acteur,
	}
	if _, err := f.service.CreateDemande(t.Context(), exact); err != nil {
		t.Errorf("lot de 120 et description de 4000 caractères (les limites exactes) doivent passer : %v", err)
	}

	overLot := exact
	overLot.Lot = strings.Repeat("l", 121)
	if _, err := f.service.CreateDemande(t.Context(), overLot); err == nil {
		t.Error("un lot de 121 caractères doit être refusé")
	}

	overDescription := exact
	overDescription.Description = strings.Repeat("d", 4001)
	if _, err := f.service.CreateDemande(t.Context(), overDescription); err == nil {
		t.Error("une description de 4001 caractères doit être refusée")
	}
}
