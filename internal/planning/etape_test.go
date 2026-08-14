package planning_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Romain-Badino/Avanti/internal/planning"
)

// baseEtape rend une étape valide et prévue, à écraser champ par champ.
func baseEtape(id planning.ID, name string) planning.Etape {
	return planning.Etape{
		ID:           id,
		Name:         name,
		PlannedStart: debutPrevu,
		PlannedEnd:   finPrevue,
		CreatedBy:    acteur,
		CreatedAt:    instantSaisie,
		UpdatedAt:    instantSaisie,
	}
}

func TestEtapeStatutDerive(t *testing.T) {
	t.Parallel()

	etape := baseEtape("e-1", "Charpente")
	if got := etape.Statut(); got != planning.StatutPrevue {
		t.Errorf("Statut() = %q, attendu prevue", got)
	}

	started, err := etape.Start(debutPrevu)
	if err != nil {
		t.Fatalf("Start() échoué : %v", err)
	}
	if got := started.Statut(); got != planning.StatutEnCours {
		t.Errorf("Statut() après Start = %q, attendu en_cours", got)
	}

	finished, err := started.Finish(finPrevue)
	if err != nil {
		t.Fatalf("Finish() échoué : %v", err)
	}
	if got := finished.Statut(); got != planning.StatutTerminee {
		t.Errorf("Statut() après Finish = %q, attendu terminee", got)
	}
}

func TestEtapeStartTransitions(t *testing.T) {
	t.Parallel()

	etape := baseEtape("e-1", "Charpente")

	t.Run("un démarrage pose le début réel en UTC et ne mute pas l'original", func(t *testing.T) {
		t.Parallel()

		paris := time.FixedZone("Europe/Paris", 2*3600)
		at := time.Date(2026, time.April, 2, 10, 0, 0, 0, paris)

		started, err := etape.Start(at)
		if err != nil {
			t.Fatalf("Start() échoué : %v", err)
		}
		if started.ActualStart.Location() != time.UTC {
			t.Errorf("ActualStart en %v, attendu UTC", started.ActualStart.Location())
		}
		if !started.ActualStart.Equal(at) {
			t.Errorf("ActualStart = %v, attendu %v", started.ActualStart, at)
		}
		if !etape.ActualStart.IsZero() {
			t.Error("l'étape d'origine a été mutée")
		}
	})

	t.Run("une date nulle est refusée", func(t *testing.T) {
		t.Parallel()

		if _, err := etape.Start(time.Time{}); !errors.Is(err, planning.ErrMissingDate) {
			t.Errorf("Start(zéro) = %v, attendu ErrMissingDate", err)
		}
	})

	t.Run("un double démarrage est refusé", func(t *testing.T) {
		t.Parallel()

		started, err := etape.Start(debutPrevu)
		if err != nil {
			t.Fatalf("Start() échoué : %v", err)
		}
		if _, err := started.Start(debutPrevu.Add(time.Hour)); !errors.Is(err, planning.ErrEtapeAlreadyStarted) {
			t.Errorf("second Start() = %v, attendu ErrEtapeAlreadyStarted", err)
		}
	})
}

func TestEtapeFinishTransitions(t *testing.T) {
	t.Parallel()

	etape := baseEtape("e-1", "Charpente")
	started, err := etape.Start(debutPrevu)
	if err != nil {
		t.Fatalf("Start() échoué : %v", err)
	}

	cases := []struct {
		name  string
		etape planning.Etape
		at    time.Time
		want  error
	}{
		{name: "date nulle", etape: started, at: time.Time{}, want: planning.ErrMissingDate},
		{name: "non démarrée", etape: etape, at: finPrevue, want: planning.ErrEtapeNotStarted},
		{name: "fin avant le début réel", etape: started, at: debutPrevu.Add(-time.Hour), want: planning.ErrFinishBeforeStart},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := tc.etape.Finish(tc.at); !errors.Is(err, tc.want) {
				t.Errorf("Finish() = %v, attendu %v", err, tc.want)
			}
		})
	}

	t.Run("une double terminaison est refusée", func(t *testing.T) {
		t.Parallel()

		finished, err := started.Finish(finPrevue)
		if err != nil {
			t.Fatalf("Finish() échoué : %v", err)
		}
		if _, err := finished.Finish(finPrevue.Add(time.Hour)); !errors.Is(err, planning.ErrEtapeAlreadyFinished) {
			t.Errorf("second Finish() = %v, attendu ErrEtapeAlreadyFinished", err)
		}
	})

	t.Run("finir le jour même du démarrage est permis", func(t *testing.T) {
		t.Parallel()

		if _, err := started.Finish(debutPrevu); err != nil {
			t.Errorf("Finish(jour du début) = %v, attendu nil", err)
		}
	})
}

// TestEtapeEnRetard vérifie les bornes de la détection : le jour même n'est
// jamais un retard, la veille non plus, le lendemain oui.
func TestEtapeEnRetard(t *testing.T) {
	t.Parallel()

	etape := baseEtape("e-1", "Charpente") // prévue du 1er au 20 avril
	started, err := etape.Start(debutPrevu)
	if err != nil {
		t.Fatalf("Start() échoué : %v", err)
	}
	finished, err := started.Finish(finPrevue.Add(30 * 24 * time.Hour)) // finie très en retard
	if err != nil {
		t.Fatalf("Finish() échoué : %v", err)
	}

	cases := []struct {
		name      string
		etape     planning.Etape
		today     time.Time
		want      bool
		wantJours int
	}{
		{name: "prévue, avant le début prévu", etape: etape, today: debutPrevu.Add(-24 * time.Hour), want: false},
		{name: "prévue, le jour du début prévu", etape: etape, today: debutPrevu, want: false},
		{name: "prévue, le jour du début prévu en fin de journée", etape: etape, today: debutPrevu.Add(23 * time.Hour), want: false},
		{name: "prévue, le lendemain du début prévu", etape: etape, today: debutPrevu.Add(24 * time.Hour), want: true, wantJours: 1},
		{name: "prévue, après la fin prévue", etape: etape, today: finPrevue.Add(72 * time.Hour), want: true, wantJours: 22},
		{name: "en cours, le jour de la fin prévue", etape: started, today: finPrevue, want: false},
		{name: "en cours, trois jours après la fin prévue", etape: started, today: finPrevue.Add(72 * time.Hour), want: true, wantJours: 3},
		{name: "terminée, même très au-delà de la fin prévue", etape: finished, today: finPrevue.Add(100 * 24 * time.Hour), want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := tc.etape.EnRetard(tc.today); got != tc.want {
				t.Errorf("EnRetard(%v) = %t, attendu %t", tc.today, got, tc.want)
			}
			if got := tc.etape.RetardConstate(tc.today); got != tc.wantJours {
				t.Errorf("RetardConstate(%v) = %d, attendu %d", tc.today, got, tc.wantJours)
			}
		})
	}
}

func TestJalonTransitions(t *testing.T) {
	t.Parallel()

	jalon := planning.Jalon{
		ID:        "j-1",
		Name:      "Hors d'eau",
		Date:      finPrevue,
		CreatedBy: acteur,
		CreatedAt: instantSaisie,
		UpdatedAt: instantSaisie,
	}

	t.Run("atteindre pose la date en UTC, une seule fois", func(t *testing.T) {
		t.Parallel()

		reached, err := jalon.Reach(finPrevue)
		if err != nil {
			t.Fatalf("Reach() échoué : %v", err)
		}
		if !reached.Atteint() {
			t.Error("Atteint() = false après Reach")
		}
		if _, err := reached.Reach(finPrevue.Add(time.Hour)); !errors.Is(err, planning.ErrJalonAlreadyReached) {
			t.Errorf("second Reach() = %v, attendu ErrJalonAlreadyReached", err)
		}
		if _, err := jalon.Reach(time.Time{}); !errors.Is(err, planning.ErrMissingDate) {
			t.Errorf("Reach(zéro) = %v, attendu ErrMissingDate", err)
		}
	})

	t.Run("le retard suit la date, jamais un jalon atteint", func(t *testing.T) {
		t.Parallel()

		if jalon.EnRetard(finPrevue) {
			t.Error("EnRetard(le jour même) = true, le jour même n'est pas un retard")
		}
		if !jalon.EnRetard(finPrevue.Add(24 * time.Hour)) {
			t.Error("EnRetard(le lendemain) = false")
		}
		if got := jalon.RetardConstate(finPrevue.Add(5 * 24 * time.Hour)); got != 5 {
			t.Errorf("RetardConstate() = %d, attendu 5", got)
		}

		reached, err := jalon.Reach(finPrevue.Add(10 * 24 * time.Hour))
		if err != nil {
			t.Fatalf("Reach() échoué : %v", err)
		}
		if reached.EnRetard(finPrevue.Add(20 * 24 * time.Hour)) {
			t.Error("EnRetard() = true sur un jalon atteint")
		}
		if got := reached.RetardConstate(finPrevue.Add(20 * 24 * time.Hour)); got != 0 {
			t.Errorf("RetardConstate() = %d sur un jalon atteint, attendu 0", got)
		}
	})
}

// TestCheckAcyclic couvre les formes de graphes qui comptent : l'auto-boucle,
// le cycle à deux, le cycle long, et les faux positifs — un DAG en losange
// n'est pas un cycle même si deux chemins y convergent.
func TestCheckAcyclic(t *testing.T) {
	t.Parallel()

	etape := func(id planning.ID, deps ...planning.ID) planning.Etape {
		e := baseEtape(id, "Étape "+id.String())
		e.DependsOn = deps
		return e
	}

	cases := []struct {
		name   string
		etapes []planning.Etape
		cycle  bool
	}{
		{name: "aucune étape", etapes: nil, cycle: false},
		{name: "aucune dépendance", etapes: []planning.Etape{etape("a"), etape("b")}, cycle: false},
		{name: "chaîne simple", etapes: []planning.Etape{etape("a"), etape("b", "a"), etape("c", "b")}, cycle: false},
		{
			name: "losange — deux chemins convergents ne sont pas un cycle",
			etapes: []planning.Etape{
				etape("a"), etape("b", "a"), etape("c", "a"), etape("d", "b", "c"),
			},
			cycle: false,
		},
		{name: "auto-référence", etapes: []planning.Etape{etape("a", "a")}, cycle: true},
		{name: "cycle à deux", etapes: []planning.Etape{etape("a", "b"), etape("b", "a")}, cycle: true},
		{
			name: "cycle long au bout d'une chaîne",
			etapes: []planning.Etape{
				etape("a"), etape("b", "a", "e"), etape("c", "b"), etape("d", "c"), etape("e", "d"),
			},
			cycle: true,
		},
		{
			name:   "dépendance vers une étape absente de la liste — ignorée ici",
			etapes: []planning.Etape{etape("a", "fantome")},
			cycle:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := planning.CheckAcyclic(tc.etapes)
			if tc.cycle && !errors.Is(err, planning.ErrDependencyCycle) {
				t.Errorf("CheckAcyclic() = %v, attendu ErrDependencyCycle", err)
			}
			if !tc.cycle && err != nil {
				t.Errorf("CheckAcyclic() = %v, attendu nil", err)
			}
		})
	}

	t.Run("le message nomme le cycle trouvé", func(t *testing.T) {
		t.Parallel()

		a := baseEtape("a", "Gros œuvre")
		a.DependsOn = []planning.ID{"b"}
		b := baseEtape("b", "Charpente")
		b.DependsOn = []planning.ID{"a"}

		err := planning.CheckAcyclic([]planning.Etape{a, b})
		if err == nil {
			t.Fatal("CheckAcyclic() = nil, un cycle était attendu")
		}
		if !strings.Contains(err.Error(), "Gros œuvre") || !strings.Contains(err.Error(), "Charpente") {
			t.Errorf("le message %q ne nomme pas les étapes du cycle", err)
		}
	})
}

func TestNewIDShape(t *testing.T) {
	t.Parallel()

	id, err := planning.NewID()
	if err != nil {
		t.Fatalf("NewID() échoué : %v", err)
	}
	if len(id.String()) != 36 {
		t.Errorf("NewID() = %q, longueur %d, attendu 36", id, len(id.String()))
	}
	if id.String()[14] != '4' {
		t.Errorf("NewID() = %q, la version n'est pas 4", id)
	}

	other, err := planning.NewID()
	if err != nil {
		t.Fatalf("NewID() échoué : %v", err)
	}
	if id == other {
		t.Error("deux tirages ont rendu le même identifiant")
	}
}
