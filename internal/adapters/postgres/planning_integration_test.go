package postgres_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Romain-Badino/Avanti/internal/adapters/postgres"
	"github.com/Romain-Badino/Avanti/internal/planning"
)

// Repères temporels des tests de planning.
var (
	planningDebut  = time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)
	planningFin    = time.Date(2026, time.April, 20, 0, 0, 0, 0, time.UTC)
	planningSaisie = time.Date(2026, time.March, 2, 9, 0, 0, 0, time.UTC)
)

// newPlanningRepo monte une base neuve et rend le dépôt planning avec le pool
// qui le porte : quelques vérifications visent les contraintes de table plutôt
// que le dépôt, et n'ont pas d'autre chemin que le SQL direct.
func newPlanningRepo(t *testing.T) (*postgres.PlanningRepo, *pgxpool.Pool) {
	t.Helper()

	pool := openPool(t, freshDatabase(t))
	applyMigrations(t, pool)

	repo, err := postgres.NewPlanningRepo(pool)
	if err != nil {
		t.Fatalf("postgres.NewPlanningRepo() échoué : %v", err)
	}

	return repo, pool
}

func TestNewPlanningRepoRejectsMissingPool(t *testing.T) {
	t.Parallel()

	if _, err := postgres.NewPlanningRepo(nil); err == nil {
		t.Error("NewPlanningRepo(nil) doit échouer")
	}
}

// planningID tire un identifiant du domaine planning.
func planningID(t *testing.T) planning.ID {
	t.Helper()

	id, err := planning.NewID()
	if err != nil {
		t.Fatalf("planning.NewID() échoué : %v", err)
	}

	return id
}

// planningActeur fabrique un identifiant d'acteur. La colonne ne porte pas de
// clé étrangère vers users — référence faible (R2) — donc un UUID quelconque
// suffit.
func planningActeur(t *testing.T) planning.ActeurID {
	t.Helper()

	return planning.ActeurID(planningID(t).String())
}

// testEtape fabrique une étape valide et complète, prête à être insérée. Les
// champs qu'un test veut particuliers, il les écrase après coup.
func testEtape(t *testing.T, name string, deps ...planning.ID) planning.Etape {
	t.Helper()

	return planning.Etape{
		ID:           planningID(t),
		Name:         name,
		Description:  "Lot de travaux du chantier.",
		PlannedStart: planningDebut,
		PlannedEnd:   planningFin,
		DependsOn:    deps,
		CreatedBy:    planningActeur(t),
		CreatedAt:    planningSaisie,
		UpdatedAt:    planningSaisie,
	}
}

func testJalon(t *testing.T, name string) planning.Jalon {
	t.Helper()

	return planning.Jalon{
		ID:        planningID(t),
		Name:      name,
		Date:      planningFin,
		CreatedBy: planningActeur(t),
		CreatedAt: planningSaisie,
		UpdatedAt: planningSaisie,
	}
}

// TestEtapeRoundTrip : ce qui est écrit se relit à l'identique, dépendances et
// transitions comprises — le seul moyen de vérifier qu'aucune valeur ne se
// perd dans la traduction vers le SQL, notamment les horodatages optionnels.
func TestEtapeRoundTrip(t *testing.T) {
	t.Parallel()

	repo, _ := newPlanningRepo(t)

	first := testEtape(t, "Gros œuvre")
	first.DevisID = "5b9d2c40-8f6e-4c11-9d7a-3e2f1a0b9c8d"
	if err := repo.CreateEtape(t.Context(), first); err != nil {
		t.Fatalf("CreateEtape() échoué : %v", err)
	}

	second := testEtape(t, "Charpente", first.ID)
	second.PlannedStart = planningDebut.Add(30 * 24 * time.Hour)
	second.PlannedEnd = second.PlannedStart.Add(10 * 24 * time.Hour)
	if err := repo.CreateEtape(t.Context(), second); err != nil {
		t.Fatalf("CreateEtape(avec prérequis) échoué : %v", err)
	}

	stored, err := repo.EtapeByID(t.Context(), second.ID)
	if err != nil {
		t.Fatalf("EtapeByID() échoué : %v", err)
	}
	if stored.Name != "Charpente" || stored.Description != second.Description {
		t.Errorf("étape relue : %+v", stored)
	}
	if len(stored.DependsOn) != 1 || stored.DependsOn[0] != first.ID {
		t.Errorf("DependsOn = %v, attendu [%s]", stored.DependsOn, first.ID)
	}
	if !stored.ActualStart.IsZero() || !stored.ActualEnd.IsZero() {
		t.Error("les dates réelles d'une étape neuve doivent être nulles")
	}
	if stored.Statut() != planning.StatutPrevue {
		t.Errorf("Statut() = %q", stored.Statut())
	}

	// Transition Start puis Finish, par les chemins réels du dépôt.
	started, err := stored.Start(planningDebut.Add(31 * 24 * time.Hour))
	if err != nil {
		t.Fatalf("Start() échoué : %v", err)
	}
	// Le prérequis n'est pas terminé : le rejeu sous verrou doit refuser.
	err = repo.StartEtape(t.Context(), started, stored.UpdatedAt)
	if !errors.Is(err, planning.ErrPrerequisitesNotDone) {
		t.Fatalf("StartEtape(prérequis non terminé) = %v, attendu ErrPrerequisitesNotDone", err)
	}
	if !strings.Contains(err.Error(), "Gros œuvre") {
		t.Errorf("le refus %q ne nomme pas l'étape bloquante", err)
	}

	// Terminer le prérequis, puis redémarrer.
	prerequisite, err := repo.EtapeByID(t.Context(), first.ID)
	if err != nil {
		t.Fatalf("EtapeByID(prérequis) échoué : %v", err)
	}
	startedFirst, err := prerequisite.Start(planningDebut)
	if err != nil {
		t.Fatalf("Start(prérequis) échoué : %v", err)
	}
	if startErr := repo.StartEtape(t.Context(), startedFirst, prerequisite.UpdatedAt); startErr != nil {
		t.Fatalf("StartEtape(prérequis) échoué : %v", startErr)
	}
	finishedFirst, err := startedFirst.Finish(planningDebut.Add(20 * 24 * time.Hour))
	if err != nil {
		t.Fatalf("Finish(prérequis) échoué : %v", err)
	}
	if updateErr := repo.UpdateEtape(t.Context(), finishedFirst, startedFirst.UpdatedAt); updateErr != nil {
		t.Fatalf("UpdateEtape(fin du prérequis) échoué : %v", updateErr)
	}

	if startErr := repo.StartEtape(t.Context(), started, stored.UpdatedAt); startErr != nil {
		t.Fatalf("StartEtape() échoué : %v", startErr)
	}

	reread, err := repo.EtapeByID(t.Context(), second.ID)
	if err != nil {
		t.Fatalf("EtapeByID() après démarrage échoué : %v", err)
	}
	if reread.Statut() != planning.StatutEnCours {
		t.Errorf("Statut() = %q après démarrage", reread.Statut())
	}
	if !reread.ActualStart.Equal(started.ActualStart) {
		t.Errorf("ActualStart = %v, attendu %v", reread.ActualStart, started.ActualStart)
	}
}

func TestListEtapesOrdersAndAssemblesDependencies(t *testing.T) {
	t.Parallel()

	repo, _ := newPlanningRepo(t)

	late := testEtape(t, "Peinture")
	late.PlannedStart = planningDebut.Add(60 * 24 * time.Hour)
	late.PlannedEnd = late.PlannedStart.Add(5 * 24 * time.Hour)
	if err := repo.CreateEtape(t.Context(), late); err != nil {
		t.Fatalf("CreateEtape() échoué : %v", err)
	}

	early := testEtape(t, "Gros œuvre")
	if err := repo.CreateEtape(t.Context(), early); err != nil {
		t.Fatalf("CreateEtape() échoué : %v", err)
	}

	middle := testEtape(t, "Charpente", early.ID)
	middle.PlannedStart = planningDebut.Add(30 * 24 * time.Hour)
	middle.PlannedEnd = middle.PlannedStart.Add(10 * 24 * time.Hour)
	if err := repo.CreateEtape(t.Context(), middle); err != nil {
		t.Fatalf("CreateEtape() échoué : %v", err)
	}

	etapes, err := repo.ListEtapes(t.Context())
	if err != nil {
		t.Fatalf("ListEtapes() échoué : %v", err)
	}
	if len(etapes) != 3 {
		t.Fatalf("len(etapes) = %d, attendu 3", len(etapes))
	}
	if etapes[0].ID != early.ID || etapes[1].ID != middle.ID || etapes[2].ID != late.ID {
		t.Errorf("ordre des étapes : %s, %s, %s", etapes[0].Name, etapes[1].Name, etapes[2].Name)
	}
	if len(etapes[1].DependsOn) != 1 || etapes[1].DependsOn[0] != early.ID {
		t.Errorf("dépendances mal assemblées : %v", etapes[1].DependsOn)
	}
	if len(etapes[0].DependsOn) != 0 || len(etapes[2].DependsOn) != 0 {
		t.Error("des dépendances sont apparues sur les mauvaises étapes")
	}
}

func TestCreateEtapeRejectsUnknownDependency(t *testing.T) {
	t.Parallel()

	repo, _ := newPlanningRepo(t)

	orphan := testEtape(t, "Charpente", planningID(t))
	if err := repo.CreateEtape(t.Context(), orphan); !errors.Is(err, planning.ErrUnknownDependency) {
		t.Errorf("CreateEtape(prérequis inconnu) = %v, attendu ErrUnknownDependency", err)
	}
}

func TestUpdateEtapeRejectsCycleUnderLock(t *testing.T) {
	t.Parallel()

	repo, _ := newPlanningRepo(t)

	a := testEtape(t, "Gros œuvre")
	if err := repo.CreateEtape(t.Context(), a); err != nil {
		t.Fatalf("CreateEtape(A) échoué : %v", err)
	}
	b := testEtape(t, "Charpente", a.ID)
	if err := repo.CreateEtape(t.Context(), b); err != nil {
		t.Fatalf("CreateEtape(B) échoué : %v", err)
	}

	// Refermer le cycle : A dépendrait de B.
	looped := a
	looped.DependsOn = []planning.ID{b.ID}
	looped.UpdatedAt = planningSaisie.Add(time.Hour)

	if err := repo.UpdateEtape(t.Context(), looped, a.UpdatedAt); !errors.Is(err, planning.ErrDependencyCycle) {
		t.Errorf("UpdateEtape(cycle) = %v, attendu ErrDependencyCycle", err)
	}

	// Le rollback a tout rendu : A n'a pas de prérequis.
	stored, err := repo.EtapeByID(t.Context(), a.ID)
	if err != nil {
		t.Fatalf("EtapeByID() échoué : %v", err)
	}
	if len(stored.DependsOn) != 0 {
		t.Errorf("DependsOn = %v après un cycle refusé, attendu vide", stored.DependsOn)
	}
}

// TestConcurrentCycleCreation est la course que le verrou consultatif doit
// gagner : deux éditions simultanées, chacune innocente sur l'état qu'elle a
// lu, qui fermeraient un cycle à elles deux. Exactement une doit passer.
func TestConcurrentCycleCreation(t *testing.T) {
	t.Parallel()

	repo, _ := newPlanningRepo(t)

	a := testEtape(t, "Gros œuvre")
	b := testEtape(t, "Charpente")
	for _, etape := range []planning.Etape{a, b} {
		if err := repo.CreateEtape(t.Context(), etape); err != nil {
			t.Fatalf("CreateEtape() échoué : %v", err)
		}
	}

	// A → B et B → A, soumis en même temps avec des gardes valides.
	aOnB := a
	aOnB.DependsOn = []planning.ID{b.ID}
	aOnB.UpdatedAt = planningSaisie.Add(time.Hour)

	bOnA := b
	bOnA.DependsOn = []planning.ID{a.ID}
	bOnA.UpdatedAt = planningSaisie.Add(time.Hour)

	// La barrière force les deux transactions à se chevaucher réellement :
	// sans elle, la première goroutine peut avoir commité avant que la seconde
	// ne démarre, et la course ne serait qu'un enchaînement.
	start := make(chan struct{})
	results := make([]error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		results[0] = repo.UpdateEtape(context.Background(), aOnB, a.UpdatedAt)
	}()
	go func() {
		defer wg.Done()
		<-start
		results[1] = repo.UpdateEtape(context.Background(), bOnA, b.UpdatedAt)
	}()
	close(start)
	wg.Wait()

	var passed, refused int
	for _, err := range results {
		switch {
		case err == nil:
			passed++
		case errors.Is(err, planning.ErrDependencyCycle):
			refused++
		default:
			t.Fatalf("erreur inattendue dans la course : %v", err)
		}
	}
	if passed != 1 || refused != 1 {
		t.Errorf("course au cycle : %d passées, %d refusées — attendu exactement 1 et 1", passed, refused)
	}
}

// TestConcurrentStartVersusPrerequisiteWrite est la course entre le démarrage
// d'une étape et l'écriture de son prérequis. Quel que soit l'ordre dans
// lequel les transactions prennent le verrou, l'état final est cohérent : si
// le démarrage a réussi, c'est que le prérequis était terminé AU MOMENT du
// rejeu sous verrou.
func TestConcurrentStartVersusPrerequisiteWrite(t *testing.T) {
	t.Parallel()

	repo, _ := newPlanningRepo(t)

	prerequisite := testEtape(t, "Gros œuvre")
	if err := repo.CreateEtape(t.Context(), prerequisite); err != nil {
		t.Fatalf("CreateEtape(prérequis) échoué : %v", err)
	}
	dependent := testEtape(t, "Charpente", prerequisite.ID)
	if err := repo.CreateEtape(t.Context(), dependent); err != nil {
		t.Fatalf("CreateEtape(dépendante) échoué : %v", err)
	}

	// Le prérequis est démarré ; sa terminaison va courir contre le démarrage
	// de la dépendante.
	startedPrereq, err := prerequisite.Start(planningDebut)
	if err != nil {
		t.Fatalf("Start(prérequis) échoué : %v", err)
	}
	if startErr := repo.StartEtape(t.Context(), startedPrereq, prerequisite.UpdatedAt); startErr != nil {
		t.Fatalf("StartEtape(prérequis) échoué : %v", startErr)
	}

	finishedPrereq, err := startedPrereq.Finish(planningFin)
	if err != nil {
		t.Fatalf("Finish(prérequis) échoué : %v", err)
	}

	startedDependent, err := dependent.Start(planningFin.Add(24 * time.Hour))
	if err != nil {
		t.Fatalf("Start(dépendante) échoué : %v", err)
	}

	// Même barrière que pour la course au cycle : les deux transactions
	// doivent réellement se disputer le verrou.
	start := make(chan struct{})
	var finishErr, startErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		finishErr = repo.UpdateEtape(context.Background(), finishedPrereq, startedPrereq.UpdatedAt)
	}()
	go func() {
		defer wg.Done()
		<-start
		startErr = repo.StartEtape(context.Background(), startedDependent, dependent.UpdatedAt)
	}()
	close(start)
	wg.Wait()

	if finishErr != nil {
		t.Fatalf("la terminaison du prérequis a échoué : %v", finishErr)
	}

	stored, err := repo.EtapeByID(t.Context(), dependent.ID)
	if err != nil {
		t.Fatalf("EtapeByID(dépendante) échoué : %v", err)
	}

	switch {
	case startErr == nil:
		// Le démarrage a gagné sa place APRÈS la terminaison : l'étape est en
		// cours et son prérequis terminé — l'invariant tient.
		if stored.Statut() != planning.StatutEnCours {
			t.Errorf("démarrage accepté mais statut = %q", stored.Statut())
		}
		reread, rereadErr := repo.EtapeByID(t.Context(), prerequisite.ID)
		if rereadErr != nil {
			t.Fatalf("EtapeByID(prérequis) échoué : %v", rereadErr)
		}
		if reread.Statut() != planning.StatutTerminee {
			t.Errorf("démarrage accepté alors que le prérequis est %q", reread.Statut())
		}
	case errors.Is(startErr, planning.ErrPrerequisitesNotDone):
		// Le démarrage a pris le verrou avant la terminaison : refusé, et
		// l'étape est restée prévue.
		if stored.Statut() != planning.StatutPrevue {
			t.Errorf("démarrage refusé mais statut = %q", stored.Statut())
		}
	default:
		t.Fatalf("erreur inattendue au démarrage : %v", startErr)
	}
}

func TestUpdateEtapeOptimisticGuard(t *testing.T) {
	t.Parallel()

	repo, _ := newPlanningRepo(t)

	etape := testEtape(t, "Charpente")
	if err := repo.CreateEtape(t.Context(), etape); err != nil {
		t.Fatalf("CreateEtape() échoué : %v", err)
	}

	renamed := etape
	renamed.Name = "Charpente traitée"
	renamed.UpdatedAt = planningSaisie.Add(time.Hour)

	t.Run("un expected périmé est refusé et se distingue de l'inconnu", func(t *testing.T) {
		if err := repo.UpdateEtape(t.Context(), renamed, planningSaisie.Add(-time.Minute)); !errors.Is(err, planning.ErrConcurrentUpdate) {
			t.Errorf("UpdateEtape(expected périmé) = %v, attendu ErrConcurrentUpdate", err)
		}

		ghost := testEtape(t, "Fantôme")
		if err := repo.UpdateEtape(t.Context(), ghost, planningSaisie); !errors.Is(err, planning.ErrUnknownEtape) {
			t.Errorf("UpdateEtape(inconnue) = %v, attendu ErrUnknownEtape", err)
		}
		if err := repo.StartEtape(t.Context(), ghost, planningSaisie); !errors.Is(err, planning.ErrUnknownEtape) {
			t.Errorf("StartEtape(inconnue) = %v, attendu ErrUnknownEtape", err)
		}
	})

	t.Run("le bon expected passe et la relecture voit la réécriture", func(t *testing.T) {
		if err := repo.UpdateEtape(t.Context(), renamed, etape.UpdatedAt); err != nil {
			t.Fatalf("UpdateEtape() échoué : %v", err)
		}

		stored, err := repo.EtapeByID(t.Context(), etape.ID)
		if err != nil {
			t.Fatalf("EtapeByID() échoué : %v", err)
		}
		if stored.Name != "Charpente traitée" {
			t.Errorf("Name = %q", stored.Name)
		}
	})
}

// TestEtapeTableConstraints vérifie que les contraintes de la table mordent en
// SQL direct — le chemin qu'aucun code Go ne prend, celui d'une correction en
// psql.
func TestEtapeTableConstraints(t *testing.T) {
	t.Parallel()

	_, pool := newPlanningRepo(t)

	const insert = `
		INSERT INTO etapes (id, nom, description, debut_prevu, fin_prevue, debut_reel, fin_reelle, devis_id, cree_par, cree_le, modifie_le)
		VALUES (gen_random_uuid(), $1, '', $2, $3, $4, $5, '', gen_random_uuid(), now(), now())`

	cases := []struct {
		name string
		args []any
	}{
		{name: "nom vide", args: []any{"   ", planningDebut, planningFin, nil, nil}},
		{name: "nom trop long", args: []any{strings.Repeat("a", 121), planningDebut, planningFin, nil, nil}},
		{name: "fin prévue avant début", args: []any{"Charpente", planningFin, planningDebut, nil, nil}},
		{name: "fin réelle sans début réel", args: []any{"Charpente", planningDebut, planningFin, nil, planningFin}},
		{name: "fin réelle avant début réel", args: []any{"Charpente", planningDebut, planningFin, planningFin, planningDebut}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := pool.Exec(t.Context(), insert, tc.args...); err == nil {
				t.Error("l'insertion aurait dû violer une contrainte")
			}
		})
	}

	t.Run("l'auto-référence est refusée par la table elle-même", func(t *testing.T) {
		t.Parallel()

		var id string
		if err := pool.QueryRow(t.Context(),
			`INSERT INTO etapes (id, nom, description, debut_prevu, fin_prevue, devis_id, cree_par, cree_le, modifie_le)
			 VALUES (gen_random_uuid(), 'Boucle', '', $1, $2, '', gen_random_uuid(), now(), now())
			 RETURNING id`, planningDebut, planningFin).Scan(&id); err != nil {
			t.Fatalf("insertion de l'étape témoin : %v", err)
		}

		if _, err := pool.Exec(t.Context(),
			`INSERT INTO etape_dependances (etape_id, prerequis_id) VALUES ($1, $1)`, id); err == nil {
			t.Error("l'auto-référence aurait dû violer la contrainte CHECK")
		}
	})
}

func TestJalonRoundTripAndGuard(t *testing.T) {
	t.Parallel()

	repo, _ := newPlanningRepo(t)

	jalon := testJalon(t, "Hors d'eau")
	if err := repo.CreateJalon(t.Context(), jalon); err != nil {
		t.Fatalf("CreateJalon() échoué : %v", err)
	}

	stored, err := repo.JalonByID(t.Context(), jalon.ID)
	if err != nil {
		t.Fatalf("JalonByID() échoué : %v", err)
	}
	if stored.Name != "Hors d'eau" || stored.Atteint() {
		t.Errorf("jalon relu : %+v", stored)
	}

	reached, err := stored.Reach(planningFin)
	if err != nil {
		t.Fatalf("Reach() échoué : %v", err)
	}

	if staleErr := repo.UpdateJalon(t.Context(), reached, planningSaisie.Add(-time.Minute)); !errors.Is(staleErr, planning.ErrConcurrentUpdate) {
		t.Errorf("UpdateJalon(expected périmé) = %v, attendu ErrConcurrentUpdate", staleErr)
	}
	if updateErr := repo.UpdateJalon(t.Context(), reached, stored.UpdatedAt); updateErr != nil {
		t.Fatalf("UpdateJalon() échoué : %v", updateErr)
	}

	reread, err := repo.JalonByID(t.Context(), jalon.ID)
	if err != nil {
		t.Fatalf("JalonByID() après atteinte échoué : %v", err)
	}
	if !reread.Atteint() || !reread.ReachedAt.Equal(planningFin) {
		t.Errorf("jalon relu après atteinte : %+v", reread)
	}

	ghost := testJalon(t, "Fantôme")
	if ghostErr := repo.UpdateJalon(t.Context(), ghost, planningSaisie); !errors.Is(ghostErr, planning.ErrUnknownJalon) {
		t.Errorf("UpdateJalon(inconnu) = %v, attendu ErrUnknownJalon", ghostErr)
	}

	jalons, err := repo.ListJalons(t.Context())
	if err != nil {
		t.Fatalf("ListJalons() échoué : %v", err)
	}
	if len(jalons) != 1 {
		t.Errorf("len(jalons) = %d, attendu 1", len(jalons))
	}
}
