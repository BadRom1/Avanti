package devis_test

import (
	"strings"
	"testing"

	"github.com/Romain-Badino/Avanti/internal/devis"
)

func TestMontantValid(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		montant devis.Montant
		want    bool
	}{
		"un centime":            {montant: 1, want: true},
		"douze mille cinq cent": {montant: 1_250_000, want: true},
		"la borne haute":        {montant: devis.MaxMontant, want: true},
		"zéro":                  {montant: 0, want: false},
		"négatif":               {montant: -1, want: false},
		"au-delà de la borne":   {montant: devis.MaxMontant + 1, want: false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := tc.montant.Valid(); got != tc.want {
				t.Errorf("Montant(%d).Valid() = %t, attendu %t", int64(tc.montant), got, tc.want)
			}
		})
	}
}

// TestMontantSplit vérifie la seule conversion d'unité du domaine, celle dont
// l'affichage dépend. Les centimes ne se perdent pas en route et ne se
// réinventent pas : 11 800,50 € vaut 1 180 050 centimes, exactement.
func TestMontantSplit(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		montant  devis.Montant
		euros    int64
		centimes int64
	}{
		"zéro":                  {montant: 0, euros: 0, centimes: 0},
		"un centime":            {montant: 1, euros: 0, centimes: 1},
		"quatre-vingt-dix-neuf": {montant: 99, euros: 0, centimes: 99},
		"un euro pile":          {montant: 100, euros: 1, centimes: 0},
		"onze mille huit cents et cinquante centimes": {montant: 1_180_050, euros: 11_800, centimes: 50},
		"douze mille cinq cents pile":                 {montant: 1_250_000, euros: 12_500, centimes: 0},
		// Un montant négatif ne peut pas être stocké, mais Split ne doit pas
		// rendre pour autant des composantes négatives qui produiraient
		// « -12 500,-50 » à l'affichage.
		"négatif": {montant: -1_180_050, euros: 11_800, centimes: 50},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			euros, centimes := tc.montant.Split()
			if euros != tc.euros || centimes != tc.centimes {
				t.Errorf("Montant(%d).Split() = (%d, %d), attendu (%d, %d)",
					int64(tc.montant), euros, centimes, tc.euros, tc.centimes)
			}
		})
	}
}

// TestMontantSplitRecomposes ferme la boucle : quel que soit le montant, les
// deux composantes le reconstituent. C'est la propriété dont dépend le
// formatage, et elle vaut mieux qu'une liste d'exemples.
func TestMontantSplitRecomposes(t *testing.T) {
	t.Parallel()

	for _, montant := range []devis.Montant{1, 7, 99, 100, 101, 999_99, 1_180_050, devis.MaxMontant} {
		euros, centimes := montant.Split()
		if recomposed := devis.Montant(euros*100 + centimes); recomposed != montant {
			t.Errorf("Montant(%d) recomposé = %d", int64(montant), int64(recomposed))
		}
	}
}

func TestMontantString(t *testing.T) {
	t.Parallel()

	got := devis.Montant(1_180_050).String()
	if !strings.Contains(got, "1180050") {
		t.Errorf("Montant.String() = %q, la valeur en centimes doit y figurer", got)
	}
}
