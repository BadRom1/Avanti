// Harnais des tests du domaine devis.
//
// Le dépôt en mémoire ci-dessous n'est pas une commodité : il tient les mêmes
// promesses que celles que [devis.Repository] exige d'une implémentation réelle
// — erreurs de lecture typées, décision refusée sur un devis déjà tranché,
// refus des concurrents en même temps que le retenu. Un fake plus permissif
// laisserait passer des tests que PostgreSQL ferait échouer.
package devis_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/Romain-Badino/Avanti/internal/devis"
)

// Repères temporels des tests. Des dates fixes plutôt que time.Now : une suite
// qui dépend de l'heure d'exécution finit par échouer une nuit de changement
// d'heure, et jamais sur le poste de qui l'a écrite.
var (
	instantEnvoi   = time.Date(2026, time.March, 2, 9, 0, 0, 0, time.UTC)
	instantReponse = time.Date(2026, time.March, 12, 14, 30, 0, 0, time.UTC)
	instantSaisie  = time.Date(2026, time.March, 15, 10, 0, 0, 0, time.UTC)
)

// acteur est l'identifiant d'acteur employé par défaut : une valeur, jamais un
// compte — le domaine ne sait pas la résoudre et n'a pas à le savoir.
const acteur devis.ActeurID = "9f1c2f6e-2b4a-4d3c-9f6a-1c2d3e4f5a6b"

// memRepo est un [devis.Repository] en mémoire.
type memRepo struct {
	demandes     map[devis.ID]devis.DemandeDevis
	demandeOrder []devis.ID
	propositions map[devis.ID]devis.Devis
	devisOrder   []devis.ID

	// failures fait échouer une méthode nommée, pour vérifier que le service
	// propage une panne du dépôt au lieu de la déguiser en refus métier.
	failures map[string]error
}

func newMemRepo() *memRepo {
	return &memRepo{
		demandes:     make(map[devis.ID]devis.DemandeDevis),
		propositions: make(map[devis.ID]devis.Devis),
		failures:     make(map[string]error),
	}
}

// failOn arme une panne sur la méthode nommée.
func (r *memRepo) failOn(method string, err error) {
	r.failures[method] = err
}

func (r *memRepo) fail(method string) error {
	return r.failures[method]
}

func (r *memRepo) CreateDemande(_ context.Context, demande devis.DemandeDevis) error {
	if err := r.fail("CreateDemande"); err != nil {
		return err
	}

	r.demandes[demande.ID] = demande
	r.demandeOrder = append(r.demandeOrder, demande.ID)

	return nil
}

func (r *memRepo) DemandeByID(_ context.Context, id devis.ID) (devis.DemandeDevis, error) {
	if err := r.fail("DemandeByID"); err != nil {
		return devis.DemandeDevis{}, err
	}

	demande, ok := r.demandes[id]
	if !ok {
		return devis.DemandeDevis{}, devis.ErrUnknownDemande
	}

	return demande, nil
}

func (r *memRepo) ListDemandes(_ context.Context) ([]devis.DemandeDevis, error) {
	if err := r.fail("ListDemandes"); err != nil {
		return nil, err
	}

	demandes := make([]devis.DemandeDevis, 0, len(r.demandeOrder))
	for _, id := range r.demandeOrder {
		demandes = append(demandes, r.demandes[id])
	}

	return demandes, nil
}

func (r *memRepo) CreateDevis(_ context.Context, proposition devis.Devis) error {
	if err := r.fail("CreateDevis"); err != nil {
		return err
	}

	r.propositions[proposition.ID] = proposition
	r.devisOrder = append(r.devisOrder, proposition.ID)

	return nil
}

func (r *memRepo) DevisByID(_ context.Context, id devis.ID) (devis.Devis, error) {
	if err := r.fail("DevisByID"); err != nil {
		return devis.Devis{}, err
	}

	proposition, ok := r.propositions[id]
	if !ok {
		return devis.Devis{}, devis.ErrUnknownDevis
	}

	return proposition, nil
}

func (r *memRepo) ListDevisByDemande(_ context.Context, demandeID devis.ID) ([]devis.Devis, error) {
	if err := r.fail("ListDevisByDemande"); err != nil {
		return nil, err
	}

	var propositions []devis.Devis
	for _, id := range r.devisOrder {
		if r.propositions[id].DemandeID == demandeID {
			propositions = append(propositions, r.propositions[id])
		}
	}

	return propositions, nil
}

func (r *memRepo) ListDevis(_ context.Context) ([]devis.Devis, error) {
	if err := r.fail("ListDevis"); err != nil {
		return nil, err
	}

	propositions := make([]devis.Devis, 0, len(r.devisOrder))
	for _, id := range r.devisOrder {
		propositions = append(propositions, r.propositions[id])
	}

	return propositions, nil
}

// Retain reproduit ce que fait la transaction PostgreSQL : le devis passe
// retenu et ses concurrents encore reçus passent refusés, ou rien ne bouge.
func (r *memRepo) Retain(_ context.Context, devisID devis.ID, by devis.ActeurID, at time.Time) error {
	if err := r.fail("Retain"); err != nil {
		return err
	}

	target, ok := r.propositions[devisID]
	if !ok {
		return devis.ErrUnknownDevis
	}
	if target.Statut != devis.StatutRecu {
		return devis.ErrDevisAlreadyDecided
	}

	for _, id := range r.devisOrder {
		sibling := r.propositions[id]
		if sibling.DemandeID != target.DemandeID || sibling.ID == target.ID || sibling.Statut != devis.StatutRecu {
			continue
		}
		r.propositions[id] = decided(sibling, devis.StatutRefuse, by, at)
	}

	r.propositions[devisID] = decided(target, devis.StatutRetenu, by, at)

	return nil
}

func (r *memRepo) Reject(_ context.Context, devisID devis.ID, by devis.ActeurID, at time.Time) error {
	if err := r.fail("Reject"); err != nil {
		return err
	}

	target, ok := r.propositions[devisID]
	if !ok {
		return devis.ErrUnknownDevis
	}
	if target.Statut != devis.StatutRecu {
		return devis.ErrDevisAlreadyDecided
	}

	r.propositions[devisID] = decided(target, devis.StatutRefuse, by, at)

	return nil
}

// decided applique le résultat d'une décision, comme le ferait l'UPDATE du
// dépôt réel.
func decided(proposition devis.Devis, statut devis.Statut, by devis.ActeurID, at time.Time) devis.Devis {
	proposition.Statut = statut
	proposition.DecidedBy = by
	proposition.DecidedAt = at
	proposition.UpdatedAt = at

	return proposition
}

// insert pose un devis directement dans le dépôt, sans passer par le service.
//
// C'est ainsi qu'on fabrique les états qu'une écriture concurrente peut
// produire — un devis encore « recu » sur une demande déjà tranchée, par
// exemple — et qu'aucun enchaînement de cas d'usage ne saurait créer.
func (r *memRepo) insert(proposition devis.Devis) {
	r.propositions[proposition.ID] = proposition
	r.devisOrder = append(r.devisOrder, proposition.ID)
}

// fixture monte un service sur un dépôt neuf, avec une horloge arrêtée et des
// identifiants prévisibles.
type fixture struct {
	service *devis.Service
	repo    *memRepo
	now     time.Time
	ids     int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	f := &fixture{repo: newMemRepo(), now: instantSaisie}

	service, err := devis.NewService(devis.ServiceOptions{
		Repo:  f.repo,
		Clock: func() time.Time { return f.now },
		NewID: func() (devis.ID, error) {
			f.ids++
			return devis.ID("id-" + strconv.Itoa(f.ids)), nil
		},
	})
	if err != nil {
		t.Fatalf("devis.NewService() échoué : %v", err)
	}
	f.service = service

	return f
}

// demande ouvre une consultation valide et rend la demande créée.
func (f *fixture) demande(t *testing.T) devis.DemandeDevis {
	t.Helper()

	demande, err := f.service.CreateDemande(t.Context(), devis.DemandeInput{
		Lot:      "Charpente",
		SentAt:   instantEnvoi,
		Artisans: []devis.Artisan{{Entreprise: "Charpentes du Val"}},
		By:       acteur,
	})
	if err != nil {
		t.Fatalf("CreateDemande() échoué : %v", err)
	}

	return demande
}

// devisRecu enregistre un devis valide sur la demande donnée.
func (f *fixture) devisRecu(t *testing.T, demandeID devis.ID, entreprise string, montant devis.Montant) devis.Devis {
	t.Helper()

	proposition, err := f.service.RecordDevis(t.Context(), devis.DevisInput{
		DemandeID:  demandeID,
		Artisan:    devis.Artisan{Entreprise: entreprise},
		Montant:    montant,
		ReceivedAt: instantReponse,
		By:         acteur,
	})
	if err != nil {
		t.Fatalf("RecordDevis(%s) échoué : %v", entreprise, err)
	}

	return proposition
}

// statuts relit les statuts des devis d'une demande, indexés par entreprise :
// c'est ce qu'on vérifie après une décision, et le lire par entreprise évite de
// dépendre de l'ordre de tri.
func (f *fixture) statuts(t *testing.T, demandeID devis.ID) map[string]devis.Statut {
	t.Helper()

	propositions, err := f.repo.ListDevisByDemande(t.Context(), demandeID)
	if err != nil {
		t.Fatalf("ListDevisByDemande() échoué : %v", err)
	}

	statuts := make(map[string]devis.Statut, len(propositions))
	for _, proposition := range propositions {
		statuts[proposition.Artisan.Entreprise] = proposition.Statut
	}

	return statuts
}

// entreprises rend les raisons sociales des devis d'une comparaison, dans
// l'ordre où elle les présente.
func entreprises(comparaison devis.Comparaison) []string {
	noms := make([]string, 0, len(comparaison.Devis))
	for _, proposition := range comparaison.Devis {
		noms = append(noms, proposition.Artisan.Entreprise)
	}

	return noms
}
