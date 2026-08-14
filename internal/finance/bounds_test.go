package finance_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/Romain-Badino/Avanti/internal/finance"
)

// Ces tests fixent les FRONTIÈRES exactes du domaine : la valeur limite passe,
// la valeur juste au-delà est refusée. Ils sont nés de la campagne de mutation
// du lot 10 — les mutants de bornes (> devenu >=) survivaient, faute d'un test
// qui regarde pile la limite. Les longueurs recopiées ici sont celles des
// constantes du domaine (facture.go) ; c'est le prix pour que le test échoue
// si une borne bouge sans que ce soit un choix.

func TestMontantValidBoundsAreExact(t *testing.T) {
	t.Parallel()

	cases := []struct {
		montant finance.Montant
		want    bool
	}{
		{montant: 0, want: false},
		{montant: 1, want: true},
		{montant: finance.MaxMontant, want: true},
		{montant: finance.MaxMontant + 1, want: false},
	}

	for _, tc := range cases {
		if got := tc.montant.Valid(); got != tc.want {
			t.Errorf("Montant(%d).Valid() = %t, attendu %t", tc.montant, got, tc.want)
		}
	}
}

// TestRecordAcompteOverdraftNamesTheThreeAmounts : le refus précoce du service
// dit les trois montants — versé, demandé, engagé — pour que la personne voie
// d'un coup de combien elle déborde. C'est sa promesse propre : le rejeu du
// dépôt sous verrou refuse aussi, mais sans ce détail.
func TestRecordAcompteOverdraftNamesTheThreeAmounts(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	if _, err := f.service.RecordAcompte(t.Context(), acompteInput()); err != nil {
		t.Fatalf("premier acompte échoué : %v", err)
	}

	over := acompteInput()
	over.Montant = 700_000 // 500 000 déjà versés + 700 000 > 1 180 050 engagés
	_, err := f.service.RecordAcompte(t.Context(), over)
	if !errors.Is(err, finance.ErrAcomptesExceedEngagement) {
		t.Fatalf("erreur = %v, attendu ErrAcomptesExceedEngagement", err)
	}
	for _, want := range []string{"500000 centimes déjà versés", "700000 centimes demandés", "1180050 centimes engagés"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("le refus %q ne dit pas %q — il doit nommer les trois montants", err, want)
		}
	}
}

func TestFactureTextBoundsAreExact(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(*finance.FactureInput, string)
		limit  int
	}{
		{
			name:   "entreprise",
			mutate: func(in *finance.FactureInput, v string) { in.Entreprise = v },
			limit:  200,
		},
		{
			name:   "numéro",
			mutate: func(in *finance.FactureInput, v string) { in.Numero = v },
			limit:  80,
		},
		{
			name:   "notes",
			mutate: func(in *finance.FactureInput, v string) { in.Notes = v },
			limit:  2000,
		},
		{
			name:   "référence de devis",
			mutate: func(in *finance.FactureInput, v string) { in.DevisID = v },
			limit:  255,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t)

			exact := factureInput()
			tc.mutate(&exact, strings.Repeat("x", tc.limit))
			if _, err := f.service.RecordFacture(t.Context(), exact); err != nil {
				t.Errorf("%d caractères (la limite exacte) doivent passer : %v", tc.limit, err)
			}

			over := factureInput()
			tc.mutate(&over, strings.Repeat("x", tc.limit+1))
			if _, err := f.service.RecordFacture(t.Context(), over); err == nil {
				t.Errorf("%d caractères (un de trop) doivent être refusés", tc.limit+1)
			}
		})
	}
}
