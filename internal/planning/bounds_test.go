package planning_test

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Romain-Badino/Avanti/internal/planning"
)

// Ces tests fixent les FRONTIÈRES exactes du domaine — la valeur limite passe,
// la valeur juste au-delà est refusée — et l'usage réel des dépendances
// injectées (horloge, générateur d'identifiants). Ils sont nés de la campagne
// de mutation du lot 10 : les mutants de bornes (> devenu >=) et de câblage
// (horloge écrasée) survivaient, faute d'un test qui regarde pile la limite.

// TestCreateEtapeUsesInjectedClockAndIDs : l'horloge et le générateur
// d'identifiants injectés sont réellement ceux qui estampillent l'étape. Un
// service qui les remplacerait en douce par time.Now ou par l'UUID par défaut
// rendrait les tests du domaine non déterministes sans que rien n'échoue.
func TestCreateEtapeUsesInjectedClockAndIDs(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	created, err := f.service.CreateEtape(t.Context(), etapeInput("Gros œuvre"))
	if err != nil {
		t.Fatalf("CreateEtape() échoué : %v", err)
	}

	if created.ID != planning.ID("id-1") {
		t.Errorf("ID = %q, attendu id-1 — le générateur injecté n'est pas utilisé", created.ID)
	}
	if !created.CreatedAt.Equal(instantSaisie) || !created.UpdatedAt.Equal(instantSaisie) {
		t.Errorf("horodatages = %s / %s, attendu %s — l'horloge injectée n'est pas utilisée",
			created.CreatedAt, created.UpdatedAt, instantSaisie)
	}

	jalon, err := f.service.CreateJalon(t.Context(), planning.JalonInput{
		Name: "Hors d'eau",
		Date: finPrevue,
		By:   acteur,
	})
	if err != nil {
		t.Fatalf("CreateJalon() échoué : %v", err)
	}
	if jalon.ID != planning.ID("id-2") || !jalon.CreatedAt.Equal(instantSaisie) {
		t.Errorf("jalon : ID = %q, CreatedAt = %s — dépendances injectées non utilisées",
			jalon.ID, jalon.CreatedAt)
	}
}

// TestEtapeTextBoundsAreExact : la valeur limite exacte est acceptée, un
// caractère de plus est refusé. Les longueurs (120, 2000, 255) sont celles des
// constantes du domaine (etape.go) ; les recopier ici est le prix pour que le
// test échoue si la borne bouge sans que ce soit un choix.
func TestEtapeTextBoundsAreExact(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(*planning.EtapeInput, string)
		limit  int
	}{
		{
			name:   "nom",
			mutate: func(in *planning.EtapeInput, v string) { in.Name = v },
			limit:  120,
		},
		{
			name:   "description",
			mutate: func(in *planning.EtapeInput, v string) { in.Description = v },
			limit:  2000,
		},
		{
			name:   "référence de devis",
			mutate: func(in *planning.EtapeInput, v string) { in.DevisID = v },
			limit:  255,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t)

			exact := etapeInput("Lot à la limite")
			tc.mutate(&exact, strings.Repeat("x", tc.limit))
			if _, err := f.service.CreateEtape(t.Context(), exact); err != nil {
				t.Errorf("%d caractères (la limite exacte) doivent passer : %v", tc.limit, err)
			}

			over := etapeInput("Lot au-delà")
			tc.mutate(&over, strings.Repeat("x", tc.limit+1))
			if _, err := f.service.CreateEtape(t.Context(), over); err == nil {
				t.Errorf("%d caractères (un de trop) doivent être refusés", tc.limit+1)
			}
		})
	}
}

// TestDependenciesBoundIsExact : 50 prérequis passent, 51 sont refusés — la
// borne de maxDependencies, au caractère près.
func TestDependenciesBoundIsExact(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	deps := make([]planning.ID, 0, 51)
	for i := range 50 {
		created := f.etape(t, etapeInput("Prérequis "+strconv.Itoa(i)))
		deps = append(deps, created.ID)
	}

	full := etapeInput("Étape à cinquante prérequis")
	full.DependsOn = deps
	if _, err := f.service.CreateEtape(t.Context(), full); err != nil {
		t.Fatalf("50 prérequis (la limite exacte) doivent passer : %v", err)
	}

	over := etapeInput("Étape à cinquante-et-un prérequis")
	over.DependsOn = append(slices.Clone(deps), planning.ID("un-de-trop"))
	_, err := f.service.CreateEtape(t.Context(), over)
	if !errors.Is(err, planning.ErrTooManyDependencies) {
		t.Errorf("erreur = %v, attendu ErrTooManyDependencies", err)
	}
}

// TestStartEtapeNamesEveryBlockingPrerequisite : le refus de démarrage nomme
// TOUTES les étapes bloquantes d'un coup — c'est la promesse du service, que
// le rejeu du dépôt (qui s'arrête à la première) ne tient pas à sa place.
func TestStartEtapeNamesEveryBlockingPrerequisite(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	first := f.etape(t, etapeInput("Maçonnerie"))
	second := f.etape(t, etapeInput("Charpente"))

	blocked := etapeInput("Couverture")
	blocked.DependsOn = []planning.ID{first.ID, second.ID}
	created := f.etape(t, blocked)

	_, err := f.service.StartEtape(t.Context(), created.ID, created.UpdatedAt, acteur)
	if !errors.Is(err, planning.ErrPrerequisitesNotDone) {
		t.Fatalf("erreur = %v, attendu ErrPrerequisitesNotDone", err)
	}
	for _, name := range []string{"Maçonnerie", "Charpente"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("le refus %q ne nomme pas l'étape bloquante %q — il doit les nommer toutes", err, name)
		}
	}
}

// TestCheckAcyclicNamesTheExactCycle : le message ne nomme que la boucle, pas
// le chemin qui y mène. « A → B → C → B » ferait chercher un cycle passant par
// A, qui n'en fait pas partie.
func TestCheckAcyclicNamesTheExactCycle(t *testing.T) {
	t.Parallel()

	etapes := []planning.Etape{
		{ID: "a", Name: "Accès chantier", DependsOn: []planning.ID{"b"}},
		{ID: "b", Name: "Bardage", DependsOn: []planning.ID{"c"}},
		{ID: "c", Name: "Cloisons", DependsOn: []planning.ID{"b"}},
	}

	err := planning.CheckAcyclic(etapes)
	if !errors.Is(err, planning.ErrDependencyCycle) {
		t.Fatalf("erreur = %v, attendu ErrDependencyCycle", err)
	}
	if !strings.Contains(err.Error(), "Bardage → Cloisons → Bardage") {
		t.Errorf("message = %q, doit nommer la boucle refermée sur son départ", err.Error())
	}
	if strings.Contains(err.Error(), "Accès chantier") {
		t.Errorf("message = %q, ne doit pas embarquer le chemin d'accès au cycle", err.Error())
	}
}
