package finance_test

import (
	"errors"
	"testing"
	"time"

	"github.com/Romain-Badino/Avanti/internal/finance"
)

// instantTransition est la date des transitions jouées dans ces tests.
var instantTransition = time.Date(2026, time.May, 5, 14, 0, 0, 0, time.UTC)

// facturePiece rend une facture née du domaine : impayée, non envoyée.
func facturePiece(t *testing.T) finance.Facture {
	t.Helper()

	return newFixture(t).facture(t, factureInput())
}

func TestFactureNaitImpayeeEtNonEnvoyee(t *testing.T) {
	t.Parallel()

	facture := facturePiece(t)

	switch {
	case facture.Paiement != finance.PaiementImpayee:
		t.Errorf("Paiement = %q, attendu %q", facture.Paiement, finance.PaiementImpayee)
	case !facture.PaidAt.IsZero():
		t.Errorf("PaidAt = %s, attendu zéro", facture.PaidAt)
	case facture.Assurance.Statut != finance.AssuranceNonEnvoyee:
		t.Errorf("Assurance.Statut = %q, attendu %q", facture.Assurance.Statut, finance.AssuranceNonEnvoyee)
	case facture.Assurance.MontantRembourse != 0:
		t.Errorf("MontantRembourse = %s, attendu 0", facture.Assurance.MontantRembourse)
	}
}

func TestMarkPayee(t *testing.T) {
	t.Parallel()

	facture := facturePiece(t)

	paid, err := facture.MarkPayee(instantTransition)
	if err != nil {
		t.Fatalf("MarkPayee() échoué : %v", err)
	}

	switch {
	case paid.Paiement != finance.PaiementPayee:
		t.Errorf("Paiement = %q, attendu %q", paid.Paiement, finance.PaiementPayee)
	case !paid.PaidAt.Equal(instantTransition):
		t.Errorf("PaidAt = %s, attendu %s", paid.PaidAt, instantTransition)
	case !paid.UpdatedAt.Equal(instantTransition):
		t.Errorf("UpdatedAt = %s, attendu %s", paid.UpdatedAt, instantTransition)
	case facture.Paiement != finance.PaiementImpayee:
		t.Error("le récepteur a été muté : les transitions doivent rendre une nouvelle valeur")
	}

	if _, err := paid.MarkPayee(instantTransition); !errors.Is(err, finance.ErrFactureAlreadyPaid) {
		t.Errorf("MarkPayee() sur une facture payée = %v, attendu %v", err, finance.ErrFactureAlreadyPaid)
	}
	if _, err := facture.MarkPayee(time.Time{}); !errors.Is(err, finance.ErrMissingDate) {
		t.Errorf("MarkPayee() sans date = %v, attendu %v", err, finance.ErrMissingDate)
	}
}

func TestAssuranceLifecycle(t *testing.T) {
	t.Parallel()

	facture := facturePiece(t)

	sent, err := facture.MarkEnvoyeeAssurance(instantTransition)
	if err != nil {
		t.Fatalf("MarkEnvoyeeAssurance() échoué : %v", err)
	}
	if sent.Assurance.Statut != finance.AssuranceEnvoyee || !sent.Assurance.SentAt.Equal(instantTransition) {
		t.Fatalf("après envoi : %+v", sent.Assurance)
	}

	refundedAt := instantTransition.Add(30 * 24 * time.Hour)
	refunded, refundErr := sent.MarkRemboursee(1_000_000, refundedAt)
	if refundErr != nil {
		t.Fatalf("MarkRemboursee() échoué : %v", refundErr)
	}

	switch {
	case refunded.Assurance.Statut != finance.AssuranceRemboursee:
		t.Errorf("Statut = %q, attendu %q", refunded.Assurance.Statut, finance.AssuranceRemboursee)
	case refunded.Assurance.MontantRembourse != 1_000_000:
		t.Errorf("MontantRembourse = %s", refunded.Assurance.MontantRembourse)
	case !refunded.Assurance.RefundedAt.Equal(refundedAt):
		t.Errorf("RefundedAt = %s", refunded.Assurance.RefundedAt)
	case !refunded.Assurance.SentAt.Equal(instantTransition):
		t.Error("la date d'envoi a été perdue par le remboursement")
	}
}

// TestAssuranceForbiddenTransitions énumère tout ce que le cycle interdit : il
// ne va que dans un sens, et le remboursement exige l'envoi préalable.
func TestAssuranceForbiddenTransitions(t *testing.T) {
	t.Parallel()

	facture := facturePiece(t)

	sent, err := facture.MarkEnvoyeeAssurance(instantTransition)
	if err != nil {
		t.Fatalf("MarkEnvoyeeAssurance() échoué : %v", err)
	}
	refunded, err := sent.MarkRemboursee(500_000, instantTransition)
	if err != nil {
		t.Fatalf("MarkRemboursee() échoué : %v", err)
	}

	cases := []struct {
		name string
		run  func() error
		want error
	}{
		{
			name: "rembourser une pièce jamais envoyée",
			run: func() error {
				_, err := facture.MarkRemboursee(100, instantTransition)
				return err
			},
			want: finance.ErrForbiddenAssuranceTransition,
		},
		{
			name: "renvoyer une pièce déjà envoyée",
			run: func() error {
				_, err := sent.MarkEnvoyeeAssurance(instantTransition)
				return err
			},
			want: finance.ErrForbiddenAssuranceTransition,
		},
		{
			name: "renvoyer une pièce remboursée",
			run: func() error {
				_, err := refunded.MarkEnvoyeeAssurance(instantTransition)
				return err
			},
			want: finance.ErrForbiddenAssuranceTransition,
		},
		{
			name: "rembourser deux fois",
			run: func() error {
				_, err := refunded.MarkRemboursee(100, instantTransition)
				return err
			},
			want: finance.ErrForbiddenAssuranceTransition,
		},
		{
			name: "envoyer sans date",
			run: func() error {
				_, err := facture.MarkEnvoyeeAssurance(time.Time{})
				return err
			},
			want: finance.ErrMissingDate,
		},
		{
			name: "rembourser sans date",
			run: func() error {
				_, err := sent.MarkRemboursee(100, time.Time{})
				return err
			},
			want: finance.ErrMissingDate,
		},
		{
			name: "remboursement nul",
			run: func() error {
				_, err := sent.MarkRemboursee(0, instantTransition)
				return err
			},
			want: finance.ErrInvalidRemboursement,
		},
		{
			name: "remboursement négatif",
			run: func() error {
				_, err := sent.MarkRemboursee(-1, instantTransition)
				return err
			},
			want: finance.ErrInvalidRemboursement,
		},
		{
			name: "remboursement supérieur à la pièce",
			run: func() error {
				_, err := sent.MarkRemboursee(sent.Montant+1, instantTransition)
				return err
			},
			want: finance.ErrInvalidRemboursement,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if err := tc.run(); !errors.Is(err, tc.want) {
				t.Errorf("erreur = %v, attendu %v", err, tc.want)
			}
		})
	}
}

// TestAssuranceRemboursementEgalMontant : rembourser exactement le montant de
// la pièce est le cas nominal, pas un dépassement.
func TestAssuranceRemboursementEgalMontant(t *testing.T) {
	t.Parallel()

	facture := facturePiece(t)

	sent, err := facture.MarkEnvoyeeAssurance(instantTransition)
	if err != nil {
		t.Fatalf("MarkEnvoyeeAssurance() échoué : %v", err)
	}

	refunded, err := sent.MarkRemboursee(sent.Montant, instantTransition)
	if err != nil {
		t.Fatalf("MarkRemboursee(montant exact) = %v, attendu aucune erreur", err)
	}
	if refunded.Assurance.MontantRembourse != sent.Montant {
		t.Errorf("MontantRembourse = %s, attendu %s", refunded.Assurance.MontantRembourse, sent.Montant)
	}
}

// TestAcompteAssuranceLifecycle : le suivi est le même type partagé, mais les
// méthodes de l'acompte sont les siennes — elles se vérifient aussi.
func TestAcompteAssuranceLifecycle(t *testing.T) {
	t.Parallel()

	acompte := newFixture(t).acompte(t, acompteInput())

	sent, err := acompte.MarkEnvoyeAssurance(instantTransition)
	if err != nil {
		t.Fatalf("MarkEnvoyeAssurance() échoué : %v", err)
	}
	if acompte.Assurance.Statut != finance.AssuranceNonEnvoyee {
		t.Error("le récepteur a été muté : les transitions doivent rendre une nouvelle valeur")
	}

	if _, tooBigErr := sent.MarkRembourse(sent.Montant+1, instantTransition); !errors.Is(tooBigErr, finance.ErrInvalidRemboursement) {
		t.Errorf("MarkRembourse(au-delà de la pièce) = %v, attendu %v", tooBigErr, finance.ErrInvalidRemboursement)
	}

	refunded, err := sent.MarkRembourse(200_000, instantTransition)
	if err != nil {
		t.Fatalf("MarkRembourse() échoué : %v", err)
	}
	if refunded.Assurance.Statut != finance.AssuranceRemboursee || refunded.Assurance.MontantRembourse != 200_000 {
		t.Errorf("après remboursement : %+v", refunded.Assurance)
	}
	if _, err := refunded.MarkEnvoyeAssurance(instantTransition); !errors.Is(err, finance.ErrForbiddenAssuranceTransition) {
		t.Errorf("MarkEnvoyeAssurance() sur un acompte remboursé = %v, attendu %v", err, finance.ErrForbiddenAssuranceTransition)
	}
}

func TestNormalizeMoyenPaiement(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
		want finance.MoyenPaiement
		err  error
	}{
		{name: "virement", raw: "virement", want: finance.MoyenVirement},
		{name: "casse et blancs tolérés", raw: "  Cheque ", want: finance.MoyenCheque},
		{name: "moyen inventé", raw: "cryptomonnaie", err: finance.ErrUnknownMoyenPaiement},
		{name: "vide", raw: "", err: finance.ErrUnknownMoyenPaiement},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := finance.NormalizeMoyenPaiement(tc.raw)
			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Errorf("erreur = %v, attendu %v", err, tc.err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("NormalizeMoyenPaiement(%q) = (%q, %v), attendu %q", tc.raw, got, err, tc.want)
			}
		})
	}
}

// TestEnumerations : les listes exportées sont des copies et couvrent les
// valeurs attendues — c'est sur elles que les gabarits et les CHECK SQL
// s'alignent.
func TestEnumerations(t *testing.T) {
	t.Parallel()

	statuts := finance.AllStatutsAssurance()
	if len(statuts) != 3 || statuts[0] != finance.AssuranceNonEnvoyee || statuts[2] != finance.AssuranceRemboursee {
		t.Errorf("AllStatutsAssurance() = %v", statuts)
	}
	statuts[0] = "corrompu"
	if !finance.AssuranceNonEnvoyee.Known() {
		t.Error("la liste rendue n'est pas une copie")
	}

	paiements := finance.AllStatutsPaiement()
	if len(paiements) != 2 || paiements[0] != finance.PaiementImpayee {
		t.Errorf("AllStatutsPaiement() = %v", paiements)
	}

	moyens := finance.AllMoyensPaiement()
	if len(moyens) != 4 || moyens[0] != finance.MoyenVirement {
		t.Errorf("AllMoyensPaiement() = %v", moyens)
	}

	if finance.StatutAssurance("perdu").Known() || finance.StatutPaiement("brouillon").Known() || finance.MoyenPaiement("troc").Known() {
		t.Error("une valeur inventée est reconnue")
	}

	for _, s := range []string{
		finance.AssuranceEnvoyee.String(), finance.PaiementPayee.String(), finance.MoyenEspeces.String(),
	} {
		if s == "" {
			t.Error("String() rend une chaîne vide")
		}
	}
}
