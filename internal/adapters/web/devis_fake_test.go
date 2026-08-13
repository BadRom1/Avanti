package web_test

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Romain-Badino/Avanti/internal/devis"
)

// memDevisRepo est un [devis.Repository] en mémoire pour les tests de
// l'adapter web.
//
// Il tient les mêmes promesses que le dépôt PostgreSQL — erreurs de lecture
// typées, décision refusée sur un devis déjà tranché, refus des concurrents en
// même temps que le retenu — parce que les gestionnaires HTTP distinguent ces
// cas et que les vérifier contre un fake plus permissif ne prouverait rien.
//
// Le verrou n'est pas décoratif : le gestionnaire sous test est exercé par
// plusieurs requêtes, et `go test -race` le remarquerait.
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

// ListDemandes rend les demandes de la plus récemment envoyée à la plus
// ancienne, comme le fait la requête SQL.
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

// withDecision applique le résultat d'une décision, comme le ferait l'UPDATE du
// dépôt réel.
func withDecision(proposition devis.Devis, statut devis.Statut, by devis.ActeurID, at time.Time) devis.Devis {
	proposition.Statut = statut
	proposition.DecidedBy = by
	proposition.DecidedAt = at
	proposition.UpdatedAt = at

	return proposition
}

// demandeParLot retrouve une demande par son intitulé, pour que les tests
// désignent « Charpente » plutôt qu'un UUID tiré au hasard.
func (r *memDevisRepo) demandeParLot(lot string) (devis.DemandeDevis, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, id := range r.demandeOrder {
		if strings.EqualFold(r.demandes[id].Lot, lot) {
			return r.demandes[id], true
		}
	}

	return devis.DemandeDevis{}, false
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
