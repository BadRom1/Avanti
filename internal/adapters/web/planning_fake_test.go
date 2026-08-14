package web_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Romain-Badino/Avanti/internal/planning"
)

// memPlanningRepo est un [planning.Repository] en mémoire pour les tests de
// l'adapter web.
//
// Il tient les mêmes promesses que le dépôt PostgreSQL — erreurs de lecture
// typées, garde optimiste, rejeu des vérifications de graphe et des prérequis
// sous sérialisation — parce que les gestionnaires HTTP distinguent ces cas et
// que les vérifier contre un fake plus permissif ne prouverait rien.
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

// ListEtapes rend les étapes triées par début prévu puis identifiant, comme la
// requête SQL.
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

// UpdateEtape honore la garde optimiste du contrat et rejoue les
// vérifications de graphe.
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

// StartEtape rejoue la vérification des prérequis terminés, comme le contrat
// du port l'exige.
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
			// Un prérequis absent du dépôt n'a pas de nom : l'identifiant est
			// la seule désignation honnête.
			return fmt.Errorf("%w : %s", planning.ErrPrerequisitesNotDone, dep)
		}
		if prerequisite.Statut() != planning.StatutTerminee {
			return fmt.Errorf("%w : %s", planning.ErrPrerequisitesNotDone, prerequisite.Name)
		}
	}
	r.etapes[etape.ID] = etape

	return nil
}

// checkGraphLocked rejoue existence des prérequis et acyclicité sur l'état du
// dépôt, l'étape écrite comprise.
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

// etapeParNom retrouve une étape par son nom, pour que les tests désignent
// « Charpente » plutôt qu'un UUID.
func (r *memPlanningRepo) etapeParNom(name string) (planning.Etape, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, id := range r.etapeOrder {
		if r.etapes[id].Name == name {
			return r.etapes[id], true
		}
	}

	return planning.Etape{}, false
}

// jalonParNom retrouve un jalon par son nom.
func (r *memPlanningRepo) jalonParNom(name string) (planning.Jalon, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, id := range r.jalonOrder {
		if r.jalons[id].Name == name {
			return r.jalons[id], true
		}
	}

	return planning.Jalon{}, false
}
