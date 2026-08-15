// Dépôts en mémoire pour les tests de l'adapter MCP — le modèle des fakes de
// l'adapter web, qu'un test de cette famille n'a pas le droit d'importer (R4 :
// une famille d'adapters n'en importe pas une autre, tests compris).
//
// Ils tiennent les mêmes promesses que les dépôts PostgreSQL — erreurs de
// lecture typées, garde optimiste, invariants revérifiés sous verrou — parce
// que les tools distinguent ces cas et que les vérifier contre un fake plus
// permissif ne prouverait rien.
package mcp_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Romain-Badino/Avanti/internal/devis"
	"github.com/Romain-Badino/Avanti/internal/document"
	"github.com/Romain-Badino/Avanti/internal/finance"
	"github.com/Romain-Badino/Avanti/internal/planning"
)

// --- devis --------------------------------------------------------------------

type memDevisRepo struct {
	mu           sync.Mutex
	demandes     map[devis.ID]devis.DemandeDevis
	demandeOrder []devis.ID
	propositions map[devis.ID]devis.Devis
	devisOrder   []devis.ID
}

func newMemDevisRepo() *memDevisRepo {
	return &memDevisRepo{
		demandes:     make(map[devis.ID]devis.DemandeDevis),
		propositions: make(map[devis.ID]devis.Devis),
	}
}

func (r *memDevisRepo) CreateDemande(_ context.Context, demande devis.DemandeDevis) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.demandes[demande.ID] = demande
	r.demandeOrder = append(r.demandeOrder, demande.ID)

	return nil
}

func (r *memDevisRepo) DemandeByID(_ context.Context, id devis.ID) (devis.DemandeDevis, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	demande, ok := r.demandes[id]
	if !ok {
		return devis.DemandeDevis{}, devis.ErrUnknownDemande
	}

	return demande, nil
}

func (r *memDevisRepo) ListDemandes(_ context.Context) ([]devis.DemandeDevis, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	demandes := make([]devis.DemandeDevis, 0, len(r.demandeOrder))
	for _, id := range r.demandeOrder {
		demandes = append(demandes, r.demandes[id])
	}
	slices.SortStableFunc(demandes, func(a, b devis.DemandeDevis) int {
		return b.SentAt.Compare(a.SentAt)
	})

	return demandes, nil
}

func (r *memDevisRepo) CreateDevis(_ context.Context, proposition devis.Devis) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.propositions[proposition.ID] = proposition
	r.devisOrder = append(r.devisOrder, proposition.ID)

	return nil
}

func (r *memDevisRepo) DevisByID(_ context.Context, id devis.ID) (devis.Devis, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	proposition, ok := r.propositions[id]
	if !ok {
		return devis.Devis{}, devis.ErrUnknownDevis
	}

	return proposition, nil
}

func (r *memDevisRepo) ListDevisByDemande(_ context.Context, demandeID devis.ID) ([]devis.Devis, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var propositions []devis.Devis
	for _, id := range r.devisOrder {
		if r.propositions[id].DemandeID == demandeID {
			propositions = append(propositions, r.propositions[id])
		}
	}

	return propositions, nil
}

func (r *memDevisRepo) ListDevis(_ context.Context) ([]devis.Devis, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	propositions := make([]devis.Devis, 0, len(r.devisOrder))
	for _, id := range r.devisOrder {
		propositions = append(propositions, r.propositions[id])
	}

	return propositions, nil
}

func (r *memDevisRepo) Retain(_ context.Context, devisID devis.ID, by devis.ActeurID, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()

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
		r.propositions[id] = withDecision(sibling, devis.StatutRefuse, by, at)
	}
	r.propositions[devisID] = withDecision(target, devis.StatutRetenu, by, at)

	return nil
}

func (r *memDevisRepo) Reject(_ context.Context, devisID devis.ID, by devis.ActeurID, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	target, ok := r.propositions[devisID]
	if !ok {
		return devis.ErrUnknownDevis
	}
	if target.Statut != devis.StatutRecu {
		return devis.ErrDevisAlreadyDecided
	}

	r.propositions[devisID] = withDecision(target, devis.StatutRefuse, by, at)

	return nil
}

func withDecision(proposition devis.Devis, statut devis.Statut, by devis.ActeurID, at time.Time) devis.Devis {
	proposition.Statut = statut
	proposition.DecidedBy = by
	proposition.DecidedAt = at
	proposition.UpdatedAt = at

	return proposition
}

// devisParEntreprise retrouve un devis par la raison sociale de son artisan.
func (r *memDevisRepo) devisParEntreprise(entreprise string) (devis.Devis, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, id := range r.devisOrder {
		if strings.EqualFold(r.propositions[id].Artisan.Entreprise, entreprise) {
			return r.propositions[id], true
		}
	}

	return devis.Devis{}, false
}

// --- finance --------------------------------------------------------------------

type memFinanceRepo struct {
	mu           sync.Mutex
	factures     map[finance.ID]finance.Facture
	factureOrder []finance.ID
	acomptes     map[finance.ID]finance.Acompte
	acompteOrder []finance.ID
}

func newMemFinanceRepo() *memFinanceRepo {
	return &memFinanceRepo{
		factures: make(map[finance.ID]finance.Facture),
		acomptes: make(map[finance.ID]finance.Acompte),
	}
}

func (r *memFinanceRepo) CreateFacture(_ context.Context, facture finance.Facture) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.factures[facture.ID] = facture
	r.factureOrder = append(r.factureOrder, facture.ID)

	return nil
}

func (r *memFinanceRepo) FactureByID(_ context.Context, id finance.ID) (finance.Facture, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	facture, ok := r.factures[id]
	if !ok {
		return finance.Facture{}, finance.ErrUnknownFacture
	}

	return facture, nil
}

func (r *memFinanceRepo) ListFactures(_ context.Context) ([]finance.Facture, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	factures := make([]finance.Facture, 0, len(r.factureOrder))
	for _, id := range r.factureOrder {
		factures = append(factures, r.factures[id])
	}

	return factures, nil
}

func (r *memFinanceRepo) UpdateFacture(_ context.Context, facture finance.Facture, expected time.Time) error {
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

func (r *memFinanceRepo) CreateAcompte(_ context.Context, acompte finance.Acompte, montantEngage finance.Montant) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if acompte.DevisID != "" {
		var cumul finance.Montant
		for _, id := range r.acompteOrder {
			if r.acomptes[id].DevisID == acompte.DevisID {
				cumul += r.acomptes[id].Montant
			}
		}
		if cumul+acompte.Montant > montantEngage {
			return fmt.Errorf("%w : %s", finance.ErrAcomptesExceedEngagement, acompte.DevisID)
		}
	}

	r.acomptes[acompte.ID] = acompte
	r.acompteOrder = append(r.acompteOrder, acompte.ID)

	return nil
}

func (r *memFinanceRepo) AcompteByID(_ context.Context, id finance.ID) (finance.Acompte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	acompte, ok := r.acomptes[id]
	if !ok {
		return finance.Acompte{}, finance.ErrUnknownAcompte
	}

	return acompte, nil
}

func (r *memFinanceRepo) ListAcomptes(_ context.Context) ([]finance.Acompte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	acomptes := make([]finance.Acompte, 0, len(r.acompteOrder))
	for _, id := range r.acompteOrder {
		acomptes = append(acomptes, r.acomptes[id])
	}

	return acomptes, nil
}

func (r *memFinanceRepo) UpdateAcompte(_ context.Context, acompte finance.Acompte, expected time.Time) error {
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

func (r *memFinanceRepo) SumAcomptesByDevis(_ context.Context, devisID string) (finance.Montant, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var cumul finance.Montant
	for _, id := range r.acompteOrder {
		if r.acomptes[id].DevisID == devisID {
			cumul += r.acomptes[id].Montant
		}
	}

	return cumul, nil
}

// --- planning --------------------------------------------------------------------

type memPlanningRepo struct {
	mu         sync.Mutex
	etapes     map[planning.ID]planning.Etape
	etapeOrder []planning.ID
	jalons     map[planning.ID]planning.Jalon
	jalonOrder []planning.ID
}

func newMemPlanningRepo() *memPlanningRepo {
	return &memPlanningRepo{
		etapes: make(map[planning.ID]planning.Etape),
		jalons: make(map[planning.ID]planning.Jalon),
	}
}

func (r *memPlanningRepo) CreateEtape(_ context.Context, etape planning.Etape) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := r.checkGraphLocked(etape); err != nil {
		return err
	}

	r.etapes[etape.ID] = etape
	r.etapeOrder = append(r.etapeOrder, etape.ID)

	return nil
}

func (r *memPlanningRepo) EtapeByID(_ context.Context, id planning.ID) (planning.Etape, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	etape, ok := r.etapes[id]
	if !ok {
		return planning.Etape{}, planning.ErrUnknownEtape
	}

	return etape, nil
}

func (r *memPlanningRepo) ListEtapes(_ context.Context) ([]planning.Etape, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	etapes := make([]planning.Etape, 0, len(r.etapeOrder))
	for _, id := range r.etapeOrder {
		etapes = append(etapes, r.etapes[id])
	}
	slices.SortStableFunc(etapes, func(a, b planning.Etape) int {
		if c := a.PlannedStart.Compare(b.PlannedStart); c != 0 {
			return c
		}
		return strings.Compare(a.ID.String(), b.ID.String())
	})

	return etapes, nil
}

func (r *memPlanningRepo) UpdateEtape(_ context.Context, etape planning.Etape, expected time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	current, ok := r.etapes[etape.ID]
	if !ok {
		return planning.ErrUnknownEtape
	}
	if !current.UpdatedAt.Equal(expected) {
		return planning.ErrConcurrentUpdate
	}
	if err := r.checkGraphLocked(etape); err != nil {
		return err
	}
	r.etapes[etape.ID] = etape

	return nil
}

func (r *memPlanningRepo) StartEtape(_ context.Context, etape planning.Etape, expected time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	current, ok := r.etapes[etape.ID]
	if !ok {
		return planning.ErrUnknownEtape
	}
	if !current.UpdatedAt.Equal(expected) {
		return planning.ErrConcurrentUpdate
	}
	for _, dep := range current.DependsOn {
		prerequisite, known := r.etapes[dep]
		if !known {
			return fmt.Errorf("%w : %s", planning.ErrPrerequisitesNotDone, dep)
		}
		if prerequisite.Statut() != planning.StatutTerminee {
			return fmt.Errorf("%w : %s", planning.ErrPrerequisitesNotDone, prerequisite.Name)
		}
	}
	r.etapes[etape.ID] = etape

	return nil
}

func (r *memPlanningRepo) checkGraphLocked(etape planning.Etape) error {
	graph := make([]planning.Etape, 0, len(r.etapeOrder)+1)
	for _, id := range r.etapeOrder {
		if id == etape.ID {
			continue
		}
		graph = append(graph, r.etapes[id])
	}
	graph = append(graph, etape)

	for _, dep := range etape.DependsOn {
		if _, known := r.etapes[dep]; !known || dep == etape.ID {
			return fmt.Errorf("%w : %s", planning.ErrUnknownDependency, dep)
		}
	}

	return planning.CheckAcyclic(graph)
}

func (r *memPlanningRepo) CreateJalon(_ context.Context, jalon planning.Jalon) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.jalons[jalon.ID] = jalon
	r.jalonOrder = append(r.jalonOrder, jalon.ID)

	return nil
}

func (r *memPlanningRepo) JalonByID(_ context.Context, id planning.ID) (planning.Jalon, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	jalon, ok := r.jalons[id]
	if !ok {
		return planning.Jalon{}, planning.ErrUnknownJalon
	}

	return jalon, nil
}

func (r *memPlanningRepo) ListJalons(_ context.Context) ([]planning.Jalon, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	jalons := make([]planning.Jalon, 0, len(r.jalonOrder))
	for _, id := range r.jalonOrder {
		jalons = append(jalons, r.jalons[id])
	}
	slices.SortStableFunc(jalons, func(a, b planning.Jalon) int {
		if c := a.Date.Compare(b.Date); c != 0 {
			return c
		}
		return strings.Compare(a.ID.String(), b.ID.String())
	})

	return jalons, nil
}

func (r *memPlanningRepo) UpdateJalon(_ context.Context, jalon planning.Jalon, expected time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	current, ok := r.jalons[jalon.ID]
	if !ok {
		return planning.ErrUnknownJalon
	}
	if !current.UpdatedAt.Equal(expected) {
		return planning.ErrConcurrentUpdate
	}
	r.jalons[jalon.ID] = jalon

	return nil
}

// --- document --------------------------------------------------------------------

type memDocumentRepo struct {
	mu        sync.Mutex
	documents map[document.ID]document.Document
	order     []document.ID
}

func newMemDocumentRepo() *memDocumentRepo {
	return &memDocumentRepo{documents: make(map[document.ID]document.Document)}
}

func (r *memDocumentRepo) Create(_ context.Context, doc document.Document) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.documents[doc.ID] = doc
	r.order = append(r.order, doc.ID)

	return nil
}

func (r *memDocumentRepo) ByID(_ context.Context, id document.ID) (document.Document, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	doc, ok := r.documents[id]
	if !ok {
		return document.Document{}, document.ErrUnknownDocument
	}

	return doc, nil
}

func (r *memDocumentRepo) List(_ context.Context) ([]document.Document, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	documents := make([]document.Document, 0, len(r.order))
	for i := len(r.order) - 1; i >= 0; i-- {
		documents = append(documents, r.documents[r.order[i]])
	}

	return documents, nil
}

func (r *memDocumentRepo) ListByTarget(_ context.Context, target document.Target) ([]document.Document, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var documents []document.Document
	for i := len(r.order) - 1; i >= 0; i-- {
		if doc := r.documents[r.order[i]]; doc.Target == target {
			documents = append(documents, doc)
		}
	}

	return documents, nil
}

func (r *memDocumentRepo) ListByTargets(_ context.Context, targetType document.TargetType, ids []string) (map[string][]document.Document, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}

	grouped := make(map[string][]document.Document, len(ids))
	for i := len(r.order) - 1; i >= 0; i-- {
		doc := r.documents[r.order[i]]
		if doc.Target.Type == targetType && wanted[doc.Target.ID] {
			grouped[doc.Target.ID] = append(grouped[doc.Target.ID], doc)
		}
	}

	return grouped, nil
}

type memDocumentStorage struct {
	mu       sync.Mutex
	contents map[string][]byte
}

func newMemDocumentStorage() *memDocumentStorage {
	return &memDocumentStorage{contents: make(map[string][]byte)}
}

func (s *memDocumentStorage) Save(_ context.Context, key string, content io.Reader) error {
	raw, err := io.ReadAll(content)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.contents[key]; exists {
		return document.ErrContentAlreadyExists
	}
	s.contents[key] = raw

	return nil
}

func (s *memDocumentStorage) Open(_ context.Context, key string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, ok := s.contents[key]
	if !ok {
		return nil, document.ErrContentNotFound
	}

	return io.NopCloser(bytes.NewReader(raw)), nil
}

func (s *memDocumentStorage) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.contents, key)

	return nil
}
