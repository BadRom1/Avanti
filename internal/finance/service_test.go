package finance_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Romain-Badino/Avanti/internal/finance"
)

func TestNewServiceRejectsMissingRepo(t *testing.T) {
	t.Parallel()

	if _, err := finance.NewService(finance.ServiceOptions{}); err == nil {
		t.Error("NewService() sans dépôt doit échouer")
	}
}

func TestRecordFactureStoresNormalizedPiece(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	in := factureInput()
	in.Entreprise = "  Charpentes   du Val "
	in.Numero = "  F-2026-042  "
	in.Notes = "  Situation n° 1.  "
	in.DevisID = " " + devisRef + " "

	facture := f.facture(t, in)

	switch {
	case facture.ID != "id-1":
		t.Errorf("ID = %q", facture.ID)
	case facture.Entreprise != "Charpentes du Val":
		t.Errorf("Entreprise = %q", facture.Entreprise)
	case facture.Numero != "F-2026-042":
		t.Errorf("Numero = %q", facture.Numero)
	case facture.Notes != "Situation n° 1.":
		t.Errorf("Notes = %q", facture.Notes)
	case facture.DevisID != devisRef:
		t.Errorf("DevisID = %q", facture.DevisID)
	case facture.RecordedBy != acteur:
		t.Errorf("RecordedBy = %q", facture.RecordedBy)
	case !facture.CreatedAt.Equal(instantSaisie) || !facture.UpdatedAt.Equal(instantSaisie):
		t.Errorf("horodatages = (%s, %s)", facture.CreatedAt, facture.UpdatedAt)
	}

	stored, err := f.service.Facture(t.Context(), facture.ID)
	if err != nil || stored.Entreprise != facture.Entreprise {
		t.Errorf("Facture() = (%+v, %v)", stored, err)
	}
}

func TestRecordFactureValidation(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", 2001)

	cases := []struct {
		name   string
		mutate func(*finance.FactureInput)
		want   error
	}{
		{name: "entreprise vide", mutate: func(in *finance.FactureInput) { in.Entreprise = "   " }, want: finance.ErrEmptyEntreprise},
		{name: "entreprise trop longue", mutate: func(in *finance.FactureInput) { in.Entreprise = strings.Repeat("é", 201) }, want: finance.ErrTextTooLong},
		{name: "montant nul", mutate: func(in *finance.FactureInput) { in.Montant = 0 }, want: finance.ErrInvalidMontant},
		{name: "montant négatif", mutate: func(in *finance.FactureInput) { in.Montant = -50 }, want: finance.ErrInvalidMontant},
		{name: "montant démesuré", mutate: func(in *finance.FactureInput) { in.Montant = finance.MaxMontant + 1 }, want: finance.ErrInvalidMontant},
		{name: "date absente", mutate: func(in *finance.FactureInput) { in.Date = time.Time{} }, want: finance.ErrMissingDate},
		{name: "numéro trop long", mutate: func(in *finance.FactureInput) { in.Numero = strings.Repeat("9", 81) }, want: finance.ErrTextTooLong},
		{name: "notes trop longues", mutate: func(in *finance.FactureInput) { in.Notes = long }, want: finance.ErrTextTooLong},
		{name: "référence de devis trop longue", mutate: func(in *finance.FactureInput) { in.DevisID = strings.Repeat("a", 256) }, want: finance.ErrInvalidDevisID},
		{name: "acteur absent", mutate: func(in *finance.FactureInput) { in.By = "" }, want: finance.ErrMissingActor},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t)
			in := factureInput()
			tc.mutate(&in)

			if _, err := f.service.RecordFacture(t.Context(), in); !errors.Is(err, tc.want) {
				t.Errorf("RecordFacture() = %v, attendu %v", err, tc.want)
			}
		})
	}
}

func TestRecordAcompteValidation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(*finance.AcompteInput)
		want   error
	}{
		{name: "entreprise vide", mutate: func(in *finance.AcompteInput) { in.Entreprise = "" }, want: finance.ErrEmptyEntreprise},
		{name: "montant nul", mutate: func(in *finance.AcompteInput) { in.Montant = 0 }, want: finance.ErrInvalidMontant},
		{name: "date absente", mutate: func(in *finance.AcompteInput) { in.Date = time.Time{} }, want: finance.ErrMissingDate},
		{name: "moyen inconnu", mutate: func(in *finance.AcompteInput) { in.Moyen = "troc" }, want: finance.ErrUnknownMoyenPaiement},
		{name: "moyen vide", mutate: func(in *finance.AcompteInput) { in.Moyen = "" }, want: finance.ErrUnknownMoyenPaiement},
		{name: "acteur absent", mutate: func(in *finance.AcompteInput) { in.By = "" }, want: finance.ErrMissingActor},
		{
			name:   "devis sans montant engagé",
			mutate: func(in *finance.AcompteInput) { in.MontantEngage = 0 },
			want:   finance.ErrMissingEngagement,
		},
		{
			name:   "notes trop longues",
			mutate: func(in *finance.AcompteInput) { in.Notes = strings.Repeat("x", 2001) },
			want:   finance.ErrTextTooLong,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t)
			in := acompteInput()
			tc.mutate(&in)

			if _, err := f.service.RecordAcompte(t.Context(), in); !errors.Is(err, tc.want) {
				t.Errorf("RecordAcompte() = %v, attendu %v", err, tc.want)
			}
		})
	}
}

// TestAcomptesInvariant est le test de l'invariant central : le cumul des
// acomptes d'un devis ne dépasse pas le montant engagé. Le dépassement est
// refusé, l'égalité acceptée — solder l'engagement est le cas nominal — et un
// acompte hors devis échappe à la règle.
func TestAcomptesInvariant(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	// 500 000 + 600 000 = 1 100 000 sur 1 180 050 engagés : les deux passent.
	f.acompte(t, acompteInput())
	second := acompteInput()
	second.Montant = 600_000
	f.acompte(t, second)

	// 80 051 de plus dépasseraient d'un centime : refusé.
	depassement := acompteInput()
	depassement.Montant = 80_051
	if _, err := f.service.RecordAcompte(t.Context(), depassement); !errors.Is(err, finance.ErrAcomptesExceedEngagement) {
		t.Fatalf("RecordAcompte(dépassement) = %v, attendu %v", err, finance.ErrAcomptesExceedEngagement)
	}

	// 80 050 soldent exactement l'engagement : accepté.
	solde := acompteInput()
	solde.Montant = 80_050
	f.acompte(t, solde)

	// Le devis est soldé : plus un centime n'entre.
	unCentime := acompteInput()
	unCentime.Montant = 1
	if _, err := f.service.RecordAcompte(t.Context(), unCentime); !errors.Is(err, finance.ErrAcomptesExceedEngagement) {
		t.Fatalf("RecordAcompte(après solde) = %v, attendu %v", err, finance.ErrAcomptesExceedEngagement)
	}

	// Hors devis : aucun engagement à comparer, le montant est libre.
	libre := acompteInput()
	libre.DevisID = ""
	libre.MontantEngage = 0
	libre.Montant = 5_000_000
	f.acompte(t, libre)

	// L'invariant est par devis : un autre devis repart de zéro.
	autre := acompteInput()
	autre.DevisID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	autre.MontantEngage = 100_000
	autre.Montant = 100_000
	f.acompte(t, autre)
}

// TestRecordAcompteReliesOnRepositoryCheck : la vérification du service peut
// être doublée par une écriture concurrente — c'est alors le dépôt qui refuse,
// et son erreur remonte telle quelle.
func TestRecordAcompteReliesOnRepositoryCheck(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.repo.failOn("SumAcomptesByDevis", nil) // explicite : la lecture du service passe.
	f.repo.failOn("CreateAcompte", finance.ErrAcomptesExceedEngagement)

	if _, err := f.service.RecordAcompte(t.Context(), acompteInput()); !errors.Is(err, finance.ErrAcomptesExceedEngagement) {
		t.Errorf("RecordAcompte() = %v, attendu l'erreur du dépôt", err)
	}
}

// TestServicePropagatesRepositoryFailures : une panne du dépôt reste une panne,
// jamais un refus métier déguisé.
func TestServicePropagatesRepositoryFailures(t *testing.T) {
	t.Parallel()

	panne := errors.New("panne simulée")

	cases := []struct {
		name   string
		method string
		run    func(*testing.T, *fixture) error
	}{
		{name: "création de facture", method: "CreateFacture", run: func(t *testing.T, f *fixture) error {
			_, err := f.service.RecordFacture(t.Context(), factureInput())
			return err
		}},
		{name: "cumul des acomptes", method: "SumAcomptesByDevis", run: func(t *testing.T, f *fixture) error {
			_, err := f.service.RecordAcompte(t.Context(), acompteInput())
			return err
		}},
		{name: "liste des factures", method: "ListFactures", run: func(t *testing.T, f *fixture) error {
			_, err := f.service.Totaux(t.Context())
			return err
		}},
		{name: "liste des acomptes", method: "ListAcomptes", run: func(t *testing.T, f *fixture) error {
			_, err := f.service.Totaux(t.Context())
			return err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t)
			f.repo.failOn(tc.method, panne)

			if err := tc.run(t, f); !errors.Is(err, panne) {
				t.Errorf("erreur = %v, attendu la panne du dépôt", err)
			}
		})
	}
}

func TestMarkFactureTransitionsPersist(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	facture := f.facture(t, factureInput())

	paid, err := f.service.MarkFacturePayee(t.Context(), facture.ID, acteur)
	if err != nil {
		t.Fatalf("MarkFacturePayee() échoué : %v", err)
	}
	if paid.Paiement != finance.PaiementPayee || !paid.PaidAt.Equal(instantSaisie) {
		t.Errorf("facture payée = %+v", paid)
	}

	sent, err := f.service.MarkFactureEnvoyeeAssurance(t.Context(), facture.ID, acteur)
	if err != nil {
		t.Fatalf("MarkFactureEnvoyeeAssurance() échoué : %v", err)
	}
	if sent.Assurance.Statut != finance.AssuranceEnvoyee {
		t.Errorf("après envoi : %+v", sent.Assurance)
	}

	refunded, err := f.service.MarkFactureRemboursee(t.Context(), facture.ID, 1_000_000, acteur)
	if err != nil {
		t.Fatalf("MarkFactureRemboursee() échoué : %v", err)
	}
	if refunded.Assurance.MontantRembourse != 1_000_000 {
		t.Errorf("MontantRembourse = %s", refunded.Assurance.MontantRembourse)
	}

	// L'état persiste : la relecture rend la dernière transition.
	stored, err := f.service.Facture(t.Context(), facture.ID)
	if err != nil || stored.Assurance.Statut != finance.AssuranceRemboursee || stored.Paiement != finance.PaiementPayee {
		t.Errorf("Facture() = (%+v, %v)", stored, err)
	}
}

func TestMarkAcompteTransitionsPersist(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	acompte := f.acompte(t, acompteInput())

	if _, err := f.service.MarkAcompteRembourse(t.Context(), acompte.ID, 100, acteur); !errors.Is(err, finance.ErrForbiddenAssuranceTransition) {
		t.Errorf("MarkAcompteRembourse() avant envoi = %v, attendu %v", err, finance.ErrForbiddenAssuranceTransition)
	}

	if _, err := f.service.MarkAcompteEnvoyeAssurance(t.Context(), acompte.ID, acteur); err != nil {
		t.Fatalf("MarkAcompteEnvoyeAssurance() échoué : %v", err)
	}
	refunded, err := f.service.MarkAcompteRembourse(t.Context(), acompte.ID, 250_000, acteur)
	if err != nil {
		t.Fatalf("MarkAcompteRembourse() échoué : %v", err)
	}
	if refunded.Assurance.MontantRembourse != 250_000 {
		t.Errorf("MontantRembourse = %s", refunded.Assurance.MontantRembourse)
	}

	stored, err := f.service.Acompte(t.Context(), acompte.ID)
	if err != nil || stored.Assurance.Statut != finance.AssuranceRemboursee {
		t.Errorf("Acompte() = (%+v, %v)", stored, err)
	}
}

func TestMarkTransitionsRequireActorAndExistingPiece(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	facture := f.facture(t, factureInput())
	acompte := f.acompte(t, acompteInput())

	if _, err := f.service.MarkFacturePayee(t.Context(), facture.ID, ""); !errors.Is(err, finance.ErrMissingActor) {
		t.Errorf("MarkFacturePayee() sans acteur = %v, attendu %v", err, finance.ErrMissingActor)
	}
	if _, err := f.service.MarkAcompteEnvoyeAssurance(t.Context(), acompte.ID, ""); !errors.Is(err, finance.ErrMissingActor) {
		t.Errorf("MarkAcompteEnvoyeAssurance() sans acteur = %v, attendu %v", err, finance.ErrMissingActor)
	}

	if _, err := f.service.MarkFacturePayee(t.Context(), "id-inconnu", acteur); !errors.Is(err, finance.ErrUnknownFacture) {
		t.Errorf("MarkFacturePayee(inconnue) = %v, attendu %v", err, finance.ErrUnknownFacture)
	}
	if _, err := f.service.MarkAcompteEnvoyeAssurance(t.Context(), "id-inconnu", acteur); !errors.Is(err, finance.ErrUnknownAcompte) {
		t.Errorf("MarkAcompteEnvoyeAssurance(inconnu) = %v, attendu %v", err, finance.ErrUnknownAcompte)
	}

	// Une transition refusée par l'entité n'écrit rien.
	if _, err := f.service.MarkFactureRemboursee(t.Context(), facture.ID, 100, acteur); !errors.Is(err, finance.ErrForbiddenAssuranceTransition) {
		t.Errorf("MarkFactureRemboursee() avant envoi = %v, attendu %v", err, finance.ErrForbiddenAssuranceTransition)
	}
	stored, err := f.service.Facture(t.Context(), facture.ID)
	if err != nil || stored.Assurance.Statut != finance.AssuranceNonEnvoyee {
		t.Errorf("la facture a changé malgré le refus : (%+v, %v)", stored, err)
	}
}

// TestMarkTransitionsSurfaceConcurrentUpdate : le conflit d'écriture du dépôt
// remonte tel quel — c'est l'appelant qui décide de relire et recommencer, le
// service ne rejoue rien de lui-même.
func TestMarkTransitionsSurfaceConcurrentUpdate(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	facture := f.facture(t, factureInput())
	acompte := f.acompte(t, acompteInput())

	f.repo.failOn("UpdateFacture", finance.ErrConcurrentUpdate)
	if _, err := f.service.MarkFacturePayee(t.Context(), facture.ID, acteur); !errors.Is(err, finance.ErrConcurrentUpdate) {
		t.Errorf("MarkFacturePayee() = %v, attendu %v", err, finance.ErrConcurrentUpdate)
	}

	f.repo.failOn("UpdateAcompte", finance.ErrConcurrentUpdate)
	if _, err := f.service.MarkAcompteEnvoyeAssurance(t.Context(), acompte.ID, acteur); !errors.Is(err, finance.ErrConcurrentUpdate) {
		t.Errorf("MarkAcompteEnvoyeAssurance() = %v, attendu %v", err, finance.ErrConcurrentUpdate)
	}
}

// TestTotaux vérifie l'agrégation : facturé, payé, remboursé, par devis, hors
// devis et total chantier — en deux lectures, jamais une par devis.
func TestTotaux(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	const autreDevis = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"

	// Devis 1 : une facture payée et remboursée de 300 000, une impayée, un
	// acompte de 500 000 remboursé de 100 000.
	payee := f.facture(t, factureInput()) // 1 180 050
	if _, err := f.service.MarkFacturePayee(t.Context(), payee.ID, acteur); err != nil {
		t.Fatalf("MarkFacturePayee() échoué : %v", err)
	}
	if _, err := f.service.MarkFactureEnvoyeeAssurance(t.Context(), payee.ID, acteur); err != nil {
		t.Fatalf("MarkFactureEnvoyeeAssurance() échoué : %v", err)
	}
	if _, err := f.service.MarkFactureRemboursee(t.Context(), payee.ID, 300_000, acteur); err != nil {
		t.Fatalf("MarkFactureRemboursee() échoué : %v", err)
	}

	impayee := factureInput()
	impayee.Montant = 200_000
	f.facture(t, impayee)

	verse := f.acompte(t, acompteInput()) // 500 000
	if _, err := f.service.MarkAcompteEnvoyeAssurance(t.Context(), verse.ID, acteur); err != nil {
		t.Fatalf("MarkAcompteEnvoyeAssurance() échoué : %v", err)
	}
	if _, err := f.service.MarkAcompteRembourse(t.Context(), verse.ID, 100_000, acteur); err != nil {
		t.Fatalf("MarkAcompteRembourse() échoué : %v", err)
	}

	// Devis 2 : un acompte seul.
	deux := acompteInput()
	deux.DevisID = autreDevis
	deux.MontantEngage = 900_000
	deux.Montant = 250_000
	f.acompte(t, deux)

	// Hors devis : une facture impayée et un acompte.
	horsFacture := factureInput()
	horsFacture.DevisID = ""
	horsFacture.Montant = 40_000
	f.facture(t, horsFacture)

	horsAcompte := acompteInput()
	horsAcompte.DevisID = ""
	horsAcompte.MontantEngage = 0
	horsAcompte.Montant = 15_000
	f.acompte(t, horsAcompte)

	totaux, err := f.service.Totaux(t.Context())
	if err != nil {
		t.Fatalf("Totaux() échoué : %v", err)
	}

	un := totaux.ParDevis[devisRef]
	if un.Facture != 1_380_050 || un.Paye != 1_680_050 || un.Rembourse != 400_000 {
		t.Errorf("devis 1 = %+v, attendu facturé 1380050, payé 1680050, remboursé 400000", un)
	}

	second := totaux.ParDevis[autreDevis]
	if second.Facture != 0 || second.Paye != 250_000 || second.Rembourse != 0 {
		t.Errorf("devis 2 = %+v", second)
	}

	if totaux.HorsDevis.Facture != 40_000 || totaux.HorsDevis.Paye != 15_000 || totaux.HorsDevis.Rembourse != 0 {
		t.Errorf("hors devis = %+v", totaux.HorsDevis)
	}

	chantier := totaux.Chantier
	if chantier.Facture != 1_420_050 || chantier.Paye != 1_945_050 || chantier.Rembourse != 400_000 {
		t.Errorf("chantier = %+v", chantier)
	}

	if len(totaux.ParDevis) != 2 {
		t.Errorf("ParDevis compte %d entrées, attendu 2", len(totaux.ParDevis))
	}
}

// TestTotauxEmpty : un chantier sans pièce rend des totaux à zéro, pas une
// erreur.
func TestTotauxEmpty(t *testing.T) {
	t.Parallel()

	totaux, err := newFixture(t).service.Totaux(t.Context())
	if err != nil {
		t.Fatalf("Totaux() échoué : %v", err)
	}
	if len(totaux.ParDevis) != 0 || totaux.Chantier != (finance.TotalFinance{}) {
		t.Errorf("Totaux() = %+v, attendu des cumuls vides", totaux)
	}
}

func TestListsComeFromRepository(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.facture(t, factureInput())
	f.acompte(t, acompteInput())

	factures, err := f.service.Factures(t.Context())
	if err != nil || len(factures) != 1 {
		t.Errorf("Factures() = (%d, %v)", len(factures), err)
	}
	acomptes, err := f.service.Acomptes(t.Context())
	if err != nil || len(acomptes) != 1 {
		t.Errorf("Acomptes() = (%d, %v)", len(acomptes), err)
	}

	if _, err := f.service.Facture(t.Context(), "absente"); !errors.Is(err, finance.ErrUnknownFacture) {
		t.Errorf("Facture(absente) = %v", err)
	}
	if _, err := f.service.Acompte(t.Context(), "absent"); !errors.Is(err, finance.ErrUnknownAcompte) {
		t.Errorf("Acompte(absent) = %v", err)
	}
}

// TestNewIDFailurePropagates : un crypto/rand en panne refuse d'enregistrer.
func TestNewIDFailurePropagates(t *testing.T) {
	t.Parallel()

	panne := errors.New("générateur en panne")

	service, err := finance.NewService(finance.ServiceOptions{
		Repo:  newMemRepo(),
		NewID: func() (finance.ID, error) { return "", panne },
	})
	if err != nil {
		t.Fatalf("finance.NewService() échoué : %v", err)
	}

	if _, err := service.RecordFacture(t.Context(), factureInput()); !errors.Is(err, panne) {
		t.Errorf("RecordFacture() = %v, attendu la panne du générateur", err)
	}
	if _, err := service.RecordAcompte(t.Context(), acompteInput()); !errors.Is(err, panne) {
		t.Errorf("RecordAcompte() = %v, attendu la panne du générateur", err)
	}
}
