package postgres_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Romain-Badino/Avanti/internal/adapters/postgres"
	"github.com/Romain-Badino/Avanti/internal/finance"
)

// Repères temporels des tests de finance.
var (
	pieceTest      = time.Date(2026, time.April, 3, 0, 0, 0, 0, time.UTC)
	saisieFinance  = time.Date(2026, time.April, 10, 9, 30, 0, 0, time.UTC)
	transitionTest = time.Date(2026, time.May, 5, 14, 0, 0, 0, time.UTC)
)

// newFinanceRepo monte une base neuve et rend le dépôt finance avec le pool qui
// le porte : quelques vérifications visent les contraintes de table plutôt que
// le dépôt, et n'ont pas d'autre chemin que le SQL direct.
func newFinanceRepo(t *testing.T) (*postgres.FinanceRepo, *pgxpool.Pool) {
	t.Helper()

	pool := openPool(t, freshDatabase(t))
	applyMigrations(t, pool)

	repo, err := postgres.NewFinanceRepo(pool)
	if err != nil {
		t.Fatalf("postgres.NewFinanceRepo() échoué : %v", err)
	}

	return repo, pool
}

func TestNewFinanceRepoRejectsMissingPool(t *testing.T) {
	t.Parallel()

	if _, err := postgres.NewFinanceRepo(nil); err == nil {
		t.Error("NewFinanceRepo(nil) doit échouer")
	}
}

// financeID tire un identifiant du domaine finance.
func financeID(t *testing.T) finance.ID {
	t.Helper()

	id, err := finance.NewID()
	if err != nil {
		t.Fatalf("finance.NewID() échoué : %v", err)
	}

	return id
}

// financeActeur fabrique un identifiant d'acteur. La colonne ne porte pas de
// clé étrangère vers users — référence faible (R2) — donc un UUID quelconque
// suffit.
func financeActeur(t *testing.T) finance.ActeurID {
	t.Helper()

	return finance.ActeurID(financeID(t).String())
}

// devisRefTest fabrique une référence faible de devis : un UUID quelconque, la
// table n'exige pas que le devis existe (R2).
func devisRefTest(t *testing.T) string {
	t.Helper()

	return financeID(t).String()
}

func testFacture(t *testing.T, devisID string, acteur finance.ActeurID, entreprise string, montant finance.Montant) finance.Facture {
	t.Helper()

	return finance.Facture{
		ID:         financeID(t),
		DevisID:    devisID,
		Entreprise: entreprise,
		Montant:    montant,
		Date:       pieceTest,
		Numero:     "F-2026-042",
		Notes:      "Situation n° 1.",
		Paiement:   finance.PaiementImpayee,
		Assurance:  finance.SuiviAssurance{Statut: finance.AssuranceNonEnvoyee},
		RecordedBy: acteur,
		CreatedAt:  saisieFinance,
		UpdatedAt:  saisieFinance,
	}
}

func testAcompte(t *testing.T, devisID string, acteur finance.ActeurID, montant finance.Montant) finance.Acompte {
	t.Helper()

	return finance.Acompte{
		ID:         financeID(t),
		DevisID:    devisID,
		Entreprise: "Charpentes du Val",
		Montant:    montant,
		Date:       pieceTest,
		Moyen:      finance.MoyenVirement,
		Notes:      "Acompte à la commande.",
		Assurance:  finance.SuiviAssurance{Statut: finance.AssuranceNonEnvoyee},
		RecordedBy: acteur,
		CreatedAt:  saisieFinance,
		UpdatedAt:  saisieFinance,
	}
}

// TestFactureRoundTrip : ce qui est écrit se relit à l'identique, transitions
// comprises — c'est le seul moyen de vérifier qu'aucune valeur ne se perd dans
// la traduction vers le SQL, notamment les horodatages optionnels du suivi.
func TestFactureRoundTrip(t *testing.T) {
	t.Parallel()

	repo, _ := newFinanceRepo(t)
	acteur := financeActeur(t)
	facture := testFacture(t, devisRefTest(t), acteur, "Charpentes du Val", 1_180_050)

	if err := repo.CreateFacture(t.Context(), facture); err != nil {
		t.Fatalf("CreateFacture() échoué : %v", err)
	}

	stored, err := repo.FactureByID(t.Context(), facture.ID)
	if err != nil {
		t.Fatalf("FactureByID() échoué : %v", err)
	}

	switch {
	case stored.ID != facture.ID:
		t.Errorf("ID = %q, attendu %q", stored.ID, facture.ID)
	case stored.DevisID != facture.DevisID:
		t.Errorf("DevisID = %q, attendu %q", stored.DevisID, facture.DevisID)
	case stored.Entreprise != facture.Entreprise:
		t.Errorf("Entreprise = %q", stored.Entreprise)
	case stored.Montant != 1_180_050:
		t.Errorf("Montant = %d, attendu 1180050", int64(stored.Montant))
	case !stored.Date.Equal(pieceTest):
		t.Errorf("Date = %s, attendu %s", stored.Date, pieceTest)
	case stored.Numero != facture.Numero || stored.Notes != facture.Notes:
		t.Errorf("Numero, Notes = %q, %q", stored.Numero, stored.Notes)
	case stored.Paiement != finance.PaiementImpayee || !stored.PaidAt.IsZero():
		t.Errorf("paiement = (%q, %s)", stored.Paiement, stored.PaidAt)
	case stored.Assurance.Statut != finance.AssuranceNonEnvoyee:
		t.Errorf("Assurance = %+v", stored.Assurance)
	case !stored.Assurance.SentAt.IsZero() || !stored.Assurance.RefundedAt.IsZero():
		t.Errorf("horodatages assurance non nuls : %+v", stored.Assurance)
	case stored.RecordedBy != acteur:
		t.Errorf("RecordedBy = %q, attendu %q", stored.RecordedBy, acteur)
	}

	// La ligne entière se réécrit : paiement puis cycle assurance complet.
	paid, err := stored.MarkPayee(transitionTest)
	if err != nil {
		t.Fatalf("MarkPayee() échoué : %v", err)
	}
	sent, err := paid.MarkEnvoyeeAssurance(transitionTest)
	if err != nil {
		t.Fatalf("MarkEnvoyeeAssurance() échoué : %v", err)
	}
	refunded, err := sent.MarkRemboursee(1_000_000, transitionTest.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("MarkRemboursee() échoué : %v", err)
	}
	if updateErr := repo.UpdateFacture(t.Context(), refunded, stored.UpdatedAt); updateErr != nil {
		t.Fatalf("UpdateFacture() échoué : %v", updateErr)
	}

	again, err := repo.FactureByID(t.Context(), facture.ID)
	if err != nil {
		t.Fatalf("FactureByID() échoué : %v", err)
	}
	switch {
	case again.Paiement != finance.PaiementPayee || !again.PaidAt.Equal(transitionTest):
		t.Errorf("paiement relu = (%q, %s)", again.Paiement, again.PaidAt)
	case again.Assurance.Statut != finance.AssuranceRemboursee:
		t.Errorf("Assurance.Statut = %q", again.Assurance.Statut)
	case again.Assurance.MontantRembourse != 1_000_000:
		t.Errorf("MontantRembourse = %d", int64(again.Assurance.MontantRembourse))
	case !again.Assurance.SentAt.Equal(transitionTest):
		t.Errorf("SentAt = %s", again.Assurance.SentAt)
	case !again.Assurance.RefundedAt.Equal(transitionTest.Add(24 * time.Hour)):
		t.Errorf("RefundedAt = %s", again.Assurance.RefundedAt)
	case !again.UpdatedAt.Equal(refunded.UpdatedAt):
		t.Errorf("UpdatedAt = %s", again.UpdatedAt)
	}
}

// TestAcompteRoundTrip vérifie l'aller-retour d'un acompte, moyen de paiement
// et suivi assurance compris.
func TestAcompteRoundTrip(t *testing.T) {
	t.Parallel()

	repo, _ := newFinanceRepo(t)
	acteur := financeActeur(t)
	devisID := devisRefTest(t)
	acompte := testAcompte(t, devisID, acteur, 500_000)

	if err := repo.CreateAcompte(t.Context(), acompte, 1_180_050); err != nil {
		t.Fatalf("CreateAcompte() échoué : %v", err)
	}

	stored, err := repo.AcompteByID(t.Context(), acompte.ID)
	if err != nil {
		t.Fatalf("AcompteByID() échoué : %v", err)
	}

	switch {
	case stored.ID != acompte.ID || stored.DevisID != devisID:
		t.Errorf("identifiants = (%q, %q)", stored.ID, stored.DevisID)
	case stored.Montant != 500_000 || stored.Moyen != finance.MoyenVirement:
		t.Errorf("pièce = (%d, %q)", int64(stored.Montant), stored.Moyen)
	case stored.Notes != acompte.Notes || stored.RecordedBy != acteur:
		t.Errorf("Notes, RecordedBy = %q, %q", stored.Notes, stored.RecordedBy)
	case stored.Assurance.Statut != finance.AssuranceNonEnvoyee:
		t.Errorf("Assurance = %+v", stored.Assurance)
	}

	sent, err := stored.MarkEnvoyeAssurance(transitionTest)
	if err != nil {
		t.Fatalf("MarkEnvoyeAssurance() échoué : %v", err)
	}
	if updateErr := repo.UpdateAcompte(t.Context(), sent, stored.UpdatedAt); updateErr != nil {
		t.Fatalf("UpdateAcompte() échoué : %v", updateErr)
	}

	again, err := repo.AcompteByID(t.Context(), acompte.ID)
	if err != nil {
		t.Fatalf("AcompteByID() échoué : %v", err)
	}
	if again.Assurance.Statut != finance.AssuranceEnvoyee || !again.Assurance.SentAt.Equal(transitionTest) {
		t.Errorf("assurance relue = %+v", again.Assurance)
	}
}

func TestFinanceUnknownPieces(t *testing.T) {
	t.Parallel()

	repo, _ := newFinanceRepo(t)

	if _, err := repo.FactureByID(t.Context(), financeID(t)); !errors.Is(err, finance.ErrUnknownFacture) {
		t.Errorf("FactureByID(absente) = %v, attendu %v", err, finance.ErrUnknownFacture)
	}
	if _, err := repo.AcompteByID(t.Context(), "pas-un-uuid"); !errors.Is(err, finance.ErrUnknownAcompte) {
		t.Errorf("AcompteByID(illisible) = %v, attendu %v", err, finance.ErrUnknownAcompte)
	}

	// Une réécriture d'une pièce disparue rend l'erreur « inconnue », pas le
	// conflit : c'est ce qui distingue un 404 d'une course entre deux
	// personnes.
	facture := testFacture(t, "", financeActeur(t), "Fantôme", 100)
	if err := repo.UpdateFacture(t.Context(), facture, facture.UpdatedAt); !errors.Is(err, finance.ErrUnknownFacture) {
		t.Errorf("UpdateFacture(absente) = %v, attendu %v", err, finance.ErrUnknownFacture)
	}
	acompte := testAcompte(t, "", financeActeur(t), 100)
	if err := repo.UpdateAcompte(t.Context(), acompte, acompte.UpdatedAt); !errors.Is(err, finance.ErrUnknownAcompte) {
		t.Errorf("UpdateAcompte(absent) = %v, attendu %v", err, finance.ErrUnknownAcompte)
	}
}

// TestFinanceListsOrder : les listes se lisent de la pièce la plus récente à la
// plus ancienne — l'ordre que la page affiche, tenu par la requête et son
// index.
func TestFinanceListsOrder(t *testing.T) {
	t.Parallel()

	repo, _ := newFinanceRepo(t)
	acteur := financeActeur(t)

	ancienne := testFacture(t, "", acteur, "Ancienne", 100_000)
	ancienne.Date = pieceTest.Add(-30 * 24 * time.Hour)
	recente := testFacture(t, "", acteur, "Récente", 200_000)

	for _, facture := range []finance.Facture{ancienne, recente} {
		if err := repo.CreateFacture(t.Context(), facture); err != nil {
			t.Fatalf("CreateFacture(%s) échoué : %v", facture.Entreprise, err)
		}
	}

	factures, err := repo.ListFactures(t.Context())
	if err != nil {
		t.Fatalf("ListFactures() échoué : %v", err)
	}
	if len(factures) != 2 || factures[0].Entreprise != "Récente" {
		t.Errorf("ordre des factures = %+v, attendu la plus récente en tête", factures)
	}

	ancien := testAcompte(t, "", acteur, 50_000)
	ancien.Date = pieceTest.Add(-15 * 24 * time.Hour)
	ancien.Entreprise = "Ancien"
	recent := testAcompte(t, "", acteur, 60_000)
	recent.Entreprise = "Récent"

	for _, acompte := range []finance.Acompte{ancien, recent} {
		if createErr := repo.CreateAcompte(t.Context(), acompte, 0); createErr != nil {
			t.Fatalf("CreateAcompte(%s) échoué : %v", acompte.Entreprise, createErr)
		}
	}

	acomptes, err := repo.ListAcomptes(t.Context())
	if err != nil {
		t.Fatalf("ListAcomptes() échoué : %v", err)
	}
	if len(acomptes) != 2 || acomptes[0].Entreprise != "Récent" {
		t.Errorf("ordre des acomptes = %+v, attendu le plus récent en tête", acomptes)
	}
}

// TestCreateAcompteEnforcesEngagement : l'invariant du cumul, dans le dépôt
// lui-même — dépassement refusé, égalité acceptée, hors devis libre, et le
// cumul relu qui fait foi.
func TestCreateAcompteEnforcesEngagement(t *testing.T) {
	t.Parallel()

	repo, _ := newFinanceRepo(t)
	acteur := financeActeur(t)
	devisID := devisRefTest(t)
	const engage = finance.Montant(1_000_000)

	if err := repo.CreateAcompte(t.Context(), testAcompte(t, devisID, acteur, 600_000), engage); err != nil {
		t.Fatalf("premier acompte refusé : %v", err)
	}

	// 400 001 dépasseraient d'un centime.
	depassement := testAcompte(t, devisID, acteur, 400_001)
	if err := repo.CreateAcompte(t.Context(), depassement, engage); !errors.Is(err, finance.ErrAcomptesExceedEngagement) {
		t.Fatalf("CreateAcompte(dépassement) = %v, attendu %v", err, finance.ErrAcomptesExceedEngagement)
	}

	// Le refus n'a rien laissé derrière lui.
	if cumul, err := repo.SumAcomptesByDevis(t.Context(), devisID); err != nil || cumul != 600_000 {
		t.Fatalf("SumAcomptesByDevis() = (%d, %v), attendu 600000", int64(cumul), err)
	}

	// 400 000 soldent exactement l'engagement.
	if err := repo.CreateAcompte(t.Context(), testAcompte(t, devisID, acteur, 400_000), engage); err != nil {
		t.Fatalf("CreateAcompte(solde exact) = %v, attendu aucune erreur", err)
	}

	// Hors devis : aucun verrou, aucun engagement, le montant est libre.
	horsDevis := testAcompte(t, "", acteur, 50_000_000)
	if err := repo.CreateAcompte(t.Context(), horsDevis, 0); err != nil {
		t.Fatalf("CreateAcompte(hors devis) = %v, attendu aucune erreur", err)
	}

	// L'invariant est par devis : un autre devisID repart de zéro.
	if err := repo.CreateAcompte(t.Context(), testAcompte(t, devisRefTest(t), acteur, 100_000), 100_000); err != nil {
		t.Fatalf("CreateAcompte(autre devis) = %v, attendu aucune erreur", err)
	}
}

// concurrentAcompteRounds est le nombre de courses jouées par
// [TestConcurrentAcomptesAtTheLimit]. Une seule ne prouverait rien : ce qu'on
// cherche est une fenêtre, et une fenêtre ne s'ouvre pas à tous les coups.
const concurrentAcompteRounds = 20

// TestConcurrentAcomptesAtTheLimit est l'épreuve de concurrence de l'invariant :
// deux insertions simultanées au ras de la limite, une seule doit passer.
//
// Sans le verrou consultatif, les deux transactions liraient le même cumul,
// jugeraient chacune que leur montant tient, et écriraient toutes les deux —
// le devis serait payé au-delà de l'engagé sans qu'aucune requête n'ait rien vu
// d'anormal. La post-condition ne présume pas de laquelle gagne, seulement
// qu'il y en a exactement une.
func TestConcurrentAcomptesAtTheLimit(t *testing.T) {
	t.Parallel()

	repo, _ := newFinanceRepo(t)
	acteur := financeActeur(t)

	for round := range concurrentAcompteRounds {
		devisID := devisRefTest(t)
		const engage = finance.Montant(1_000_000)

		// 700 000 déjà versés : il reste 300 000, et chacune des deux
		// concurrentes en demande 300 000 — l'une sole, l'autre déborde.
		if err := repo.CreateAcompte(t.Context(), testAcompte(t, devisID, acteur, 700_000), engage); err != nil {
			t.Fatalf("tour %d : mise en place échouée : %v", round, err)
		}

		first := testAcompte(t, devisID, acteur, 300_000)
		second := testAcompte(t, devisID, acteur, 300_000)
		firstErr, secondErr := raceCreateAcomptes(t, repo, first, second, engage)

		accepted := 0
		for _, err := range []error{firstErr, secondErr} {
			switch {
			case err == nil:
				accepted++
			case !errors.Is(err, finance.ErrAcomptesExceedEngagement):
				t.Fatalf("tour %d : erreur inattendue : %v", round, err)
			}
		}
		if accepted != 1 {
			t.Fatalf("tour %d : %d insertions acceptées, attendu exactement 1", round, accepted)
		}

		cumul, err := repo.SumAcomptesByDevis(t.Context(), devisID)
		if err != nil {
			t.Fatalf("tour %d : SumAcomptesByDevis() échoué : %v", round, err)
		}
		if cumul != engage {
			t.Fatalf("tour %d : cumul = %d, attendu %d — l'invariant a cédé", round, int64(cumul), int64(engage))
		}
	}
}

// raceCreateAcomptes lance les deux insertions ensemble et rend leurs erreurs.
//
// Le départ est donné par la fermeture d'un canal plutôt que par deux `go`
// successifs : les goroutines sont déjà lancées et bloquées dessus, ce qui
// resserre la fenêtre au lieu de laisser la première prendre de l'avance.
func raceCreateAcomptes(
	t *testing.T, repo *postgres.FinanceRepo, first, second finance.Acompte, engage finance.Montant,
) (firstErr, secondErr error) {
	t.Helper()

	depart := make(chan struct{})

	var course sync.WaitGroup
	course.Add(2)

	go func() {
		defer course.Done()
		<-depart
		firstErr = repo.CreateAcompte(t.Context(), first, engage)
	}()
	go func() {
		defer course.Done()
		<-depart
		secondErr = repo.CreateAcompte(t.Context(), second, engage)
	}()

	close(depart)
	course.Wait()

	return firstErr, secondErr
}

// TestUpdateGuardsAgainstStaleWrite : la garde optimiste, jouée sans course —
// une réécriture qui porte un horodatage périmé ne touche rien et ressort en
// conflit, sur les deux tables.
func TestUpdateGuardsAgainstStaleWrite(t *testing.T) {
	t.Parallel()

	repo, _ := newFinanceRepo(t)
	acteur := financeActeur(t)

	facture := testFacture(t, "", acteur, "Toiture Ain", 1_180_050)
	if err := repo.CreateFacture(t.Context(), facture); err != nil {
		t.Fatalf("CreateFacture() échoué : %v", err)
	}

	paid, err := facture.MarkPayee(transitionTest)
	if err != nil {
		t.Fatalf("MarkPayee() échoué : %v", err)
	}
	if firstErr := repo.UpdateFacture(t.Context(), paid, facture.UpdatedAt); firstErr != nil {
		t.Fatalf("UpdateFacture() échoué : %v", firstErr)
	}

	// L'horodatage de facture est périmé : la ligne porte celui de paid.
	sent, err := facture.MarkEnvoyeeAssurance(transitionTest.Add(time.Hour))
	if err != nil {
		t.Fatalf("MarkEnvoyeeAssurance() échoué : %v", err)
	}
	if staleErr := repo.UpdateFacture(t.Context(), sent, facture.UpdatedAt); !errors.Is(staleErr, finance.ErrConcurrentUpdate) {
		t.Fatalf("UpdateFacture(périmée) = %v, attendu %v", staleErr, finance.ErrConcurrentUpdate)
	}

	// Le refus n'a rien écrit : la facture est restée payée, jamais envoyée.
	stored, err := repo.FactureByID(t.Context(), facture.ID)
	if err != nil {
		t.Fatalf("FactureByID() échoué : %v", err)
	}
	if stored.Paiement != finance.PaiementPayee || stored.Assurance.Statut != finance.AssuranceNonEnvoyee {
		t.Errorf("état après refus = (%q, %q)", stored.Paiement, stored.Assurance.Statut)
	}

	acompte := testAcompte(t, "", acteur, 100_000)
	if createErr := repo.CreateAcompte(t.Context(), acompte, 0); createErr != nil {
		t.Fatalf("CreateAcompte() échoué : %v", createErr)
	}
	acompteSent, err := acompte.MarkEnvoyeAssurance(transitionTest)
	if err != nil {
		t.Fatalf("MarkEnvoyeAssurance() échoué : %v", err)
	}
	if err := repo.UpdateAcompte(t.Context(), acompteSent, acompte.UpdatedAt); err != nil {
		t.Fatalf("UpdateAcompte() échoué : %v", err)
	}
	if err := repo.UpdateAcompte(t.Context(), acompteSent, acompte.UpdatedAt); !errors.Is(err, finance.ErrConcurrentUpdate) {
		t.Errorf("UpdateAcompte(périmé) = %v, attendu %v", err, finance.ErrConcurrentUpdate)
	}
}

// TestConcurrentFactureTransitions met en vraie concurrence deux transitions
// sur la même facture — l'une paie, l'autre envoie à l'assurance — toutes deux
// bâties sur la même lecture.
//
// C'est le scénario que la garde optimiste doit tenir : sans elle, les deux
// réécritures passeraient et la seconde ferait régresser l'état posé par la
// première (une facture payée qui redeviendrait impayée). La post-condition ne
// présume pas de laquelle gagne, mais exige qu'il y en ait exactement une, et
// que l'état final soit exactement le sien.
func TestConcurrentFactureTransitions(t *testing.T) {
	t.Parallel()

	repo, _ := newFinanceRepo(t)
	acteur := financeActeur(t)

	for round := range concurrentAcompteRounds {
		facture := testFacture(t, "", acteur, "Toiture Ain", 1_180_050)
		if err := repo.CreateFacture(t.Context(), facture); err != nil {
			t.Fatalf("tour %d : CreateFacture() échoué : %v", round, err)
		}

		paid, err := facture.MarkPayee(transitionTest)
		if err != nil {
			t.Fatalf("tour %d : MarkPayee() échoué : %v", round, err)
		}
		sent, err := facture.MarkEnvoyeeAssurance(transitionTest.Add(time.Minute))
		if err != nil {
			t.Fatalf("tour %d : MarkEnvoyeeAssurance() échoué : %v", round, err)
		}

		paidErr, sentErr := raceUpdateFacture(t, repo, paid, sent, facture.UpdatedAt)

		winners := 0
		for _, raceErr := range []error{paidErr, sentErr} {
			switch {
			case raceErr == nil:
				winners++
			case !errors.Is(raceErr, finance.ErrConcurrentUpdate):
				t.Fatalf("tour %d : erreur inattendue : %v", round, raceErr)
			}
		}
		if winners != 1 {
			t.Fatalf("tour %d : %d réécritures passées, attendu exactement 1", round, winners)
		}

		stored, err := repo.FactureByID(t.Context(), facture.ID)
		if err != nil {
			t.Fatalf("tour %d : FactureByID() échoué : %v", round, err)
		}

		// L'état final est celui du gagnant, en entier — jamais un mélange ni
		// une régression.
		switch {
		case paidErr == nil &&
			(stored.Paiement != finance.PaiementPayee || stored.Assurance.Statut != finance.AssuranceNonEnvoyee):
			t.Fatalf("tour %d : le paiement a gagné mais l'état est (%q, %q)", round, stored.Paiement, stored.Assurance.Statut)
		case sentErr == nil &&
			(stored.Paiement != finance.PaiementImpayee || stored.Assurance.Statut != finance.AssuranceEnvoyee):
			t.Fatalf("tour %d : l'envoi a gagné mais l'état est (%q, %q)", round, stored.Paiement, stored.Assurance.Statut)
		}
	}
}

// raceUpdateFacture lance les deux réécritures ensemble et rend leurs erreurs.
// Même mécanique de départ que la course des acomptes.
func raceUpdateFacture(
	t *testing.T, repo *postgres.FinanceRepo, paid, sent finance.Facture, expected time.Time,
) (paidErr, sentErr error) {
	t.Helper()

	depart := make(chan struct{})

	var course sync.WaitGroup
	course.Add(2)

	go func() {
		defer course.Done()
		<-depart
		paidErr = repo.UpdateFacture(t.Context(), paid, expected)
	}()
	go func() {
		defer course.Done()
		<-depart
		sentErr = repo.UpdateFacture(t.Context(), sent, expected)
	}()

	close(depart)
	course.Wait()

	return paidErr, sentErr
}

// TestFinanceTableConstraints vérifie que les garde-fous des tables mordent, et
// non seulement ceux du domaine. Ce sont eux qui protègent la base d'une
// écriture directe en psql ou d'un futur chemin de code qui court-circuiterait
// le domaine.
func TestFinanceTableConstraints(t *testing.T) {
	t.Parallel()

	_, pool := newFinanceRepo(t)
	acteur := financeActeur(t)

	const factureQuery = `
		INSERT INTO factures (id, devis_id, entreprise, montant, date_piece, statut_paiement, payee_le,
		                      statut_assurance, envoyee_le, montant_rembourse, rembourse_le,
		                      saisi_par, cree_le, modifie_le)
		VALUES (gen_random_uuid(), '', $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11)`

	type factureRow struct {
		entreprise  string
		montant     int64
		paiement    string
		payeeLe     any
		assurance   string
		envoyeeLe   any
		rembourse   int64
		rembourseLe any
	}

	valid := factureRow{
		entreprise: "Toiture Ain", montant: 1_180_050,
		paiement: "impayee", assurance: "non_envoyee",
	}

	cases := map[string]func(*factureRow){
		"montant nul":                   func(r *factureRow) { r.montant = 0 },
		"montant négatif":               func(r *factureRow) { r.montant = -1 },
		"montant démesuré":              func(r *factureRow) { r.montant = 10_000_000_001 },
		"entreprise vide":               func(r *factureRow) { r.entreprise = "   " },
		"statut paiement inventé":       func(r *factureRow) { r.paiement = "en_attente" },
		"impayée datée d'un règlement":  func(r *factureRow) { r.payeeLe = transitionTest },
		"payée sans date":               func(r *factureRow) { r.paiement = "payee" },
		"statut assurance inventé":      func(r *factureRow) { r.assurance = "perdu" },
		"non envoyée avec date d'envoi": func(r *factureRow) { r.envoyeeLe = transitionTest },
		"envoyée sans date":             func(r *factureRow) { r.assurance = "envoyee" },
		"envoyée déjà remboursée":       func(r *factureRow) { r.assurance = "envoyee"; r.envoyeeLe = transitionTest; r.rembourse = 100 },
		"remboursée sans montant": func(r *factureRow) {
			r.assurance = "remboursee"
			r.envoyeeLe = transitionTest
			r.rembourseLe = transitionTest
		},
		"remboursée au-delà du montant": func(r *factureRow) {
			r.assurance = "remboursee"
			r.envoyeeLe = transitionTest
			r.rembourse = r.montant + 1
			r.rembourseLe = transitionTest
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			row := valid
			mutate(&row)

			_, err := pool.Exec(t.Context(), factureQuery,
				row.entreprise, row.montant, pieceTest, row.paiement, row.payeeLe,
				row.assurance, row.envoyeeLe, row.rembourse, row.rembourseLe,
				acteur.String(), saisieFinance)
			if err == nil {
				t.Errorf("l'insertion %q a été acceptée, la contrainte ne mord pas", name)
			}
		})
	}

	// Le pendant acomptes se limite au moyen de paiement, seule contrainte qui
	// lui soit propre — les autres sont les mêmes expressions que factures.
	const acompteQuery = `
		INSERT INTO acomptes (id, devis_id, entreprise, montant, date_piece, moyen,
		                      statut_assurance, saisi_par, cree_le, modifie_le)
		VALUES (gen_random_uuid(), '', 'Toiture Ain', 100, $1, $2, 'non_envoyee', $3, $4, $4)`

	if _, err := pool.Exec(t.Context(), acompteQuery, pieceTest, "troc", acteur.String(), saisieFinance); err == nil {
		t.Error("un moyen de paiement inventé a été accepté")
	}
	if _, err := pool.Exec(t.Context(), acompteQuery, pieceTest, "virement", acteur.String(), saisieFinance); err != nil {
		t.Errorf("un acompte valide a été refusé : %v", err)
	}
}

// TestSumAcomptesByDevis : un devis sans acompte rend zéro, pas une erreur, et
// le cumul ne mélange pas les devis.
func TestSumAcomptesByDevis(t *testing.T) {
	t.Parallel()

	repo, _ := newFinanceRepo(t)
	acteur := financeActeur(t)
	devisID := devisRefTest(t)

	if cumul, err := repo.SumAcomptesByDevis(t.Context(), devisID); err != nil || cumul != 0 {
		t.Fatalf("SumAcomptesByDevis(vide) = (%d, %v), attendu 0", int64(cumul), err)
	}

	if err := repo.CreateAcompte(t.Context(), testAcompte(t, devisID, acteur, 250_000), 1_000_000); err != nil {
		t.Fatalf("CreateAcompte() échoué : %v", err)
	}
	if err := repo.CreateAcompte(t.Context(), testAcompte(t, devisRefTest(t), acteur, 999_999), 999_999); err != nil {
		t.Fatalf("CreateAcompte(autre devis) échoué : %v", err)
	}

	if cumul, err := repo.SumAcomptesByDevis(t.Context(), devisID); err != nil || cumul != 250_000 {
		t.Errorf("SumAcomptesByDevis() = (%d, %v), attendu 250000", int64(cumul), err)
	}
}
