// Harnais des tests du domaine finance.
//
// Le dépôt en mémoire ci-dessous n'est pas une commodité : il tient les mêmes
// promesses que celles que [finance.Repository] exige d'une implémentation
// réelle — erreurs de lecture typées, réécriture qui échoue sur une pièce
// inconnue, invariant du cumul revérifié dans CreateAcompte. Un fake plus
// permissif laisserait passer des tests que PostgreSQL ferait échouer.
package finance_test

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Romain-Badino/Avanti/internal/finance"
)

// Repères temporels des tests. Des dates fixes plutôt que time.Now : une suite
// qui dépend de l'heure d'exécution finit par échouer une nuit de changement
// d'heure, et jamais sur le poste de qui l'a écrite.
var (
	instantPiece  = time.Date(2026, time.April, 3, 0, 0, 0, 0, time.UTC)
	instantSaisie = time.Date(2026, time.April, 10, 9, 30, 0, 0, time.UTC)
)

// acteur est l'identifiant d'acteur employé par défaut : une valeur, jamais un
// compte — le domaine ne sait pas la résoudre et n'a pas à le savoir.
const acteur finance.ActeurID = "9f1c2f6e-2b4a-4d3c-9f6a-1c2d3e4f5a6b"

// devisRef est la référence faible de devis employée par défaut.
const devisRef = "5b9d2c40-8f6e-4c11-9d7a-3e2f1a0b9c8d"

// memRepo est un [finance.Repository] en mémoire.
//
// Le verrou sérialise CreateAcompte comme le contrat du port l'exige : la
// relecture du cumul et l'insertion forment un bloc indivisible, exactement ce
// que le verrou consultatif de l'adapter PostgreSQL garantit.
type memRepo struct {
	mu           sync.Mutex
	factures     map[finance.ID]finance.Facture
	factureOrder []finance.ID
	acomptes     map[finance.ID]finance.Acompte
	acompteOrder []finance.ID

	// failures fait échouer une méthode nommée, pour vérifier que le service
	// propage une panne du dépôt au lieu de la déguiser en refus métier.
	failures map[string]error
}

func newMemRepo() *memRepo {
	return &memRepo{
		factures: make(map[finance.ID]finance.Facture),
		acomptes: make(map[finance.ID]finance.Acompte),
		failures: make(map[string]error),
	}
}

// failOn arme une panne sur la méthode nommée.
func (r *memRepo) failOn(method string, err error) {
	r.failures[method] = err
}

func (r *memRepo) fail(method string) error {
	return r.failures[method]
}

func (r *memRepo) CreateFacture(_ context.Context, facture finance.Facture) error {
	if err := r.fail("CreateFacture"); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.factures[facture.ID] = facture
	r.factureOrder = append(r.factureOrder, facture.ID)

	return nil
}

func (r *memRepo) FactureByID(_ context.Context, id finance.ID) (finance.Facture, error) {
	if err := r.fail("FactureByID"); err != nil {
		return finance.Facture{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	facture, ok := r.factures[id]
	if !ok {
		return finance.Facture{}, finance.ErrUnknownFacture
	}

	return facture, nil
}

func (r *memRepo) ListFactures(_ context.Context) ([]finance.Facture, error) {
	if err := r.fail("ListFactures"); err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	factures := make([]finance.Facture, 0, len(r.factureOrder))
	for _, id := range r.factureOrder {
		factures = append(factures, r.factures[id])
	}

	return factures, nil
}

// UpdateFacture honore la garde optimiste du contrat : la réécriture ne passe
// que si la pièce porte encore l'horodatage lu par l'appelant.
func (r *memRepo) UpdateFacture(_ context.Context, facture finance.Facture, expected time.Time) error {
	if err := r.fail("UpdateFacture"); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	current, ok := r.factures[facture.ID]
	if !ok {
		return finance.ErrUnknownFacture
	}
	if !current.UpdatedAt.Equal(expected) {
		return finance.ErrConcurrentUpdate
	}
	r.factures[facture.ID] = facture

	return nil
}

// CreateAcompte reproduit le contrat du dépôt réel : sous verrou, le cumul des
// acomptes du devis est relu et l'insertion refusée si elle dépassait le
// montant engagé. Un acompte sans devisID entre sans vérification.
func (r *memRepo) CreateAcompte(_ context.Context, acompte finance.Acompte, montantEngage finance.Montant) error {
	if err := r.fail("CreateAcompte"); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if acompte.DevisID != "" {
		if existing := r.sumLocked(acompte.DevisID); existing+acompte.Montant > montantEngage {
			return fmt.Errorf("%w : %s", finance.ErrAcomptesExceedEngagement, acompte.DevisID)
		}
	}

	r.acomptes[acompte.ID] = acompte
	r.acompteOrder = append(r.acompteOrder, acompte.ID)

	return nil
}

func (r *memRepo) AcompteByID(_ context.Context, id finance.ID) (finance.Acompte, error) {
	if err := r.fail("AcompteByID"); err != nil {
		return finance.Acompte{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	acompte, ok := r.acomptes[id]
	if !ok {
		return finance.Acompte{}, finance.ErrUnknownAcompte
	}

	return acompte, nil
}

func (r *memRepo) ListAcomptes(_ context.Context) ([]finance.Acompte, error) {
	if err := r.fail("ListAcomptes"); err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	acomptes := make([]finance.Acompte, 0, len(r.acompteOrder))
	for _, id := range r.acompteOrder {
		acomptes = append(acomptes, r.acomptes[id])
	}

	return acomptes, nil
}

// UpdateAcompte honore la même garde optimiste que UpdateFacture.
func (r *memRepo) UpdateAcompte(_ context.Context, acompte finance.Acompte, expected time.Time) error {
	if err := r.fail("UpdateAcompte"); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	current, ok := r.acomptes[acompte.ID]
	if !ok {
		return finance.ErrUnknownAcompte
	}
	if !current.UpdatedAt.Equal(expected) {
		return finance.ErrConcurrentUpdate
	}
	r.acomptes[acompte.ID] = acompte

	return nil
}

func (r *memRepo) SumAcomptesByDevis(_ context.Context, devisID string) (finance.Montant, error) {
	if err := r.fail("SumAcomptesByDevis"); err != nil {
		return 0, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	return r.sumLocked(devisID), nil
}

func (r *memRepo) sumLocked(devisID string) finance.Montant {
	var total finance.Montant
	for _, id := range r.acompteOrder {
		if r.acomptes[id].DevisID == devisID {
			total += r.acomptes[id].Montant
		}
	}

	return total
}

// fixture monte un service sur un dépôt neuf, avec une horloge arrêtée et des
// identifiants prévisibles.
type fixture struct {
	service *finance.Service
	repo    *memRepo
	now     time.Time
	ids     int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	f := &fixture{repo: newMemRepo(), now: instantSaisie}

	service, err := finance.NewService(finance.ServiceOptions{
		Repo:  f.repo,
		Clock: func() time.Time { return f.now },
		NewID: func() (finance.ID, error) {
			f.ids++
			return finance.ID("id-" + strconv.Itoa(f.ids)), nil
		},
	})
	if err != nil {
		t.Fatalf("finance.NewService() échoué : %v", err)
	}
	f.service = service

	return f
}

// factureInput rend une entrée de facture valide, à écraser champ par champ.
func factureInput() finance.FactureInput {
	return finance.FactureInput{
		DevisID:    devisRef,
		Entreprise: "Charpentes du Val",
		Montant:    1_180_050,
		Date:       instantPiece,
		Numero:     "F-2026-042",
		Notes:      "Situation n° 1.",
		By:         acteur,
	}
}

// acompteInput rend une entrée d'acompte valide, à écraser champ par champ.
func acompteInput() finance.AcompteInput {
	return finance.AcompteInput{
		DevisID:       devisRef,
		Entreprise:    "Charpentes du Val",
		Montant:       500_000,
		Date:          instantPiece,
		Moyen:         finance.MoyenVirement,
		MontantEngage: 1_180_050,
		By:            acteur,
	}
}

// facture enregistre une facture valide et la rend.
func (f *fixture) facture(t *testing.T, in finance.FactureInput) finance.Facture {
	t.Helper()

	facture, err := f.service.RecordFacture(t.Context(), in)
	if err != nil {
		t.Fatalf("RecordFacture() échoué : %v", err)
	}

	return facture
}

// acompte enregistre un acompte valide et le rend.
func (f *fixture) acompte(t *testing.T, in finance.AcompteInput) finance.Acompte {
	t.Helper()

	acompte, err := f.service.RecordAcompte(t.Context(), in)
	if err != nil {
		t.Fatalf("RecordAcompte() échoué : %v", err)
	}

	return acompte
}
