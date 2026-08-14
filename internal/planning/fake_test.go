// Harnais des tests du domaine planning.
//
// Le dépôt en mémoire ci-dessous n'est pas une commodité : il tient les mêmes
// promesses que celles que [planning.Repository] exige d'une implémentation
// réelle — erreurs de lecture typées, garde optimiste sur les réécritures,
// rejeu des vérifications de graphe et des prérequis sous sérialisation. Un
// fake plus permissif laisserait passer des tests que PostgreSQL ferait
// échouer.
package planning_test

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Romain-Badino/Avanti/internal/planning"
)

// Repères temporels des tests. Des dates fixes plutôt que time.Now : une suite
// qui dépend de l'heure d'exécution finit par échouer une nuit de changement
// d'heure, et jamais sur le poste de qui l'a écrite.
var (
	instantSaisie = time.Date(2026, time.March, 2, 9, 0, 0, 0, time.UTC)
	debutPrevu    = time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)
	finPrevue     = time.Date(2026, time.April, 20, 0, 0, 0, 0, time.UTC)
)

// acteur est l'identifiant d'acteur employé par défaut : une valeur, jamais un
// compte — le domaine ne sait pas la résoudre et n'a pas à le savoir.
const acteur planning.ActeurID = "9f1c2f6e-2b4a-4d3c-9f6a-1c2d3e4f5a6b"

// memRepo est un [planning.Repository] en mémoire.
//
// Le verrou sérialise les écritures d'étapes comme le contrat du port
// l'exige, et les vérifications de graphe et de prérequis y sont rejouées —
// exactement ce que le verrou consultatif de l'adapter PostgreSQL garantit.
type memRepo struct {
	mu         sync.Mutex
	etapes     map[planning.ID]planning.Etape
	etapeOrder []planning.ID
	jalons     map[planning.ID]planning.Jalon
	jalonOrder []planning.ID

	// failures fait échouer une méthode nommée, pour vérifier que le service
	// propage une panne du dépôt au lieu de la déguiser en refus métier.
	failures map[string]error
}

func newMemRepo() *memRepo {
	return &memRepo{
		etapes:   make(map[planning.ID]planning.Etape),
		jalons:   make(map[planning.ID]planning.Jalon),
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

// CreateEtape rejoue sous verrou ce que le contrat exige : existence des
// prérequis et acyclicité sur l'état sérialisé.
func (r *memRepo) CreateEtape(_ context.Context, etape planning.Etape) error {
	if err := r.fail("CreateEtape"); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if err := r.checkGraphLocked(etape); err != nil {
		return err
	}

	r.etapes[etape.ID] = etape
	r.etapeOrder = append(r.etapeOrder, etape.ID)

	return nil
}

func (r *memRepo) EtapeByID(_ context.Context, id planning.ID) (planning.Etape, error) {
	if err := r.fail("EtapeByID"); err != nil {
		return planning.Etape{}, err
	}

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
func (r *memRepo) ListEtapes(_ context.Context) ([]planning.Etape, error) {
	if err := r.fail("ListEtapes"); err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	return r.listEtapesLocked(), nil
}

func (r *memRepo) listEtapesLocked() []planning.Etape {
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

	return etapes
}

// UpdateEtape honore la garde optimiste du contrat et rejoue les
// vérifications de graphe sous verrou.
func (r *memRepo) UpdateEtape(_ context.Context, etape planning.Etape, expected time.Time) error {
	if err := r.fail("UpdateEtape"); err != nil {
		return err
	}

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

// StartEtape rejoue sous verrou la vérification des prérequis terminés, comme
// le contrat du port l'exige.
func (r *memRepo) StartEtape(_ context.Context, etape planning.Etape, expected time.Time) error {
	if err := r.fail("StartEtape"); err != nil {
		return err
	}

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

// checkGraphLocked rejoue existence des prérequis et acyclicité sur l'état
// verrouillé, l'étape écrite comprise.
func (r *memRepo) checkGraphLocked(etape planning.Etape) error {
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

func (r *memRepo) CreateJalon(_ context.Context, jalon planning.Jalon) error {
	if err := r.fail("CreateJalon"); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.jalons[jalon.ID] = jalon
	r.jalonOrder = append(r.jalonOrder, jalon.ID)

	return nil
}

func (r *memRepo) JalonByID(_ context.Context, id planning.ID) (planning.Jalon, error) {
	if err := r.fail("JalonByID"); err != nil {
		return planning.Jalon{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	jalon, ok := r.jalons[id]
	if !ok {
		return planning.Jalon{}, planning.ErrUnknownJalon
	}

	return jalon, nil
}

func (r *memRepo) ListJalons(_ context.Context) ([]planning.Jalon, error) {
	if err := r.fail("ListJalons"); err != nil {
		return nil, err
	}

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

func (r *memRepo) UpdateJalon(_ context.Context, jalon planning.Jalon, expected time.Time) error {
	if err := r.fail("UpdateJalon"); err != nil {
		return err
	}

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

// fixture monte un service sur un dépôt neuf, avec une horloge réglable et des
// identifiants prévisibles.
type fixture struct {
	service *planning.Service
	repo    *memRepo
	now     time.Time
	ids     int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	f := &fixture{repo: newMemRepo(), now: instantSaisie}

	service, err := planning.NewService(planning.ServiceOptions{
		Repo:  f.repo,
		Clock: func() time.Time { return f.now },
		NewID: func() (planning.ID, error) {
			f.ids++
			return planning.ID("id-" + strconv.Itoa(f.ids)), nil
		},
	})
	if err != nil {
		t.Fatalf("planning.NewService() échoué : %v", err)
	}
	f.service = service

	return f
}

// etapeInput rend une entrée d'étape valide, à écraser champ par champ.
func etapeInput(name string) planning.EtapeInput {
	return planning.EtapeInput{
		Name:         name,
		Description:  "Lot de travaux du chantier.",
		PlannedStart: debutPrevu,
		PlannedEnd:   finPrevue,
		By:           acteur,
	}
}

// etape crée une étape valide et la rend.
func (f *fixture) etape(t *testing.T, in planning.EtapeInput) planning.Etape {
	t.Helper()

	etape, err := f.service.CreateEtape(t.Context(), in)
	if err != nil {
		t.Fatalf("CreateEtape(%s) échoué : %v", in.Name, err)
	}

	return etape
}

// jalon crée un jalon valide et le rend.
func (f *fixture) jalon(t *testing.T, name string, date time.Time) planning.Jalon {
	t.Helper()

	jalon, err := f.service.CreateJalon(t.Context(), planning.JalonInput{Name: name, Date: date, By: acteur})
	if err != nil {
		t.Fatalf("CreateJalon(%s) échoué : %v", name, err)
	}

	return jalon
}

// chain crée une chaîne A ← B (B dépend de A) et rend les deux étapes.
func (f *fixture) chain(t *testing.T) (first, second planning.Etape) {
	t.Helper()

	first = f.etape(t, etapeInput("Gros œuvre"))

	in := etapeInput("Charpente")
	in.DependsOn = []planning.ID{first.ID}
	second = f.etape(t, in)

	return first, second
}

// startAndFinish démarre puis termine une étape, en suivant les horodatages.
func (f *fixture) startAndFinish(t *testing.T, id planning.ID) planning.Etape {
	t.Helper()

	current, err := f.service.Etape(t.Context(), id)
	if err != nil {
		t.Fatalf("Etape(%s) échoué : %v", id, err)
	}

	started, err := f.service.StartEtape(t.Context(), id, current.UpdatedAt, acteur)
	if err != nil {
		t.Fatalf("StartEtape(%s) échoué : %v", id, err)
	}

	finished, err := f.service.FinishEtape(t.Context(), id, started.UpdatedAt, acteur)
	if err != nil {
		t.Fatalf("FinishEtape(%s) échoué : %v", id, err)
	}

	return finished
}
