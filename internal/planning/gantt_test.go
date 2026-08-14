package planning_test

import (
	"testing"
	"time"

	"github.com/Romain-Badino/Avanti/internal/planning"
)

// day rend le jour d'avril 2026 demandé, à minuit UTC — les cas de Gantt se
// calculent à la main, mieux vaut des dates qui se lisent.
func day(d int) time.Time {
	return time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(d-1) * 24 * time.Hour)
}

// TestBuildGanttPositions vérifie les millièmes sur un cas calculé à la main :
// une plage du 1er avril (inclus) au 30 avril (inclus, donc fin exclusive au
// 1er mai), soit 30 jours — chaque jour vaut 1000/30 ≈ 33 millièmes.
func TestBuildGanttPositions(t *testing.T) {
	t.Parallel()

	etapeA := baseEtape("a", "Gros œuvre")
	etapeA.PlannedStart = day(1)
	etapeA.PlannedEnd = day(10)

	etapeB := baseEtape("b", "Charpente")
	etapeB.PlannedStart = day(11)
	etapeB.PlannedEnd = day(20)
	etapeB.DependsOn = []planning.ID{"a"}

	jalon := planning.Jalon{ID: "j", Name: "Hors d'eau", Date: day(30)}

	view := planning.BuildGantt([]planning.Etape{etapeA, etapeB}, []planning.Jalon{jalon}, day(15))

	if view.Empty {
		t.Fatal("Empty = true avec deux étapes et un jalon")
	}
	if !view.From.Equal(day(1)) {
		t.Errorf("From = %v, attendu le 1er avril", view.From)
	}
	// Le jalon du 30 étend la plage au-delà des étapes ; sa fin exclusive est
	// le 1er mai.
	if !view.To.Equal(day(31)) {
		t.Errorf("To = %v, attendu le 1er mai (fin exclusive du 30 avril)", view.To)
	}

	if len(view.Rows) != 2 {
		t.Fatalf("len(Rows) = %d, attendu 2", len(view.Rows))
	}

	// A couvre les jours 1 à 10 : de 0 à 10/30 de la plage.
	a := view.Rows[0]
	if a.PlannedFrom != 0 || a.PlannedTo != 333 {
		t.Errorf("A : [%d, %d] millièmes, attendu [0, 333]", a.PlannedFrom, a.PlannedTo)
	}
	// B couvre les jours 11 à 20 : de 10/30 à 20/30.
	b := view.Rows[1]
	if b.PlannedFrom != 333 || b.PlannedTo != 666 {
		t.Errorf("B : [%d, %d] millièmes, attendu [333, 666]", b.PlannedFrom, b.PlannedTo)
	}

	// Le jalon marque le début de son jour : 29/30 de la plage.
	if len(view.Jalons) != 1 {
		t.Fatalf("len(Jalons) = %d, attendu 1", len(view.Jalons))
	}
	if got := view.Jalons[0].Position; got != 966 {
		t.Errorf("Jalon.Position = %d, attendu 966", got)
	}
	if view.Jalons[0].EnRetard {
		t.Error("le jalon du 30 n'est pas en retard le 15")
	}
}

func TestBuildGanttRowsCarryStatusAndDelay(t *testing.T) {
	t.Parallel()

	// A démarrée le 2, toujours en cours le 15 alors qu'elle devait finir le
	// 10 : cinq jours de retard, et sa barre réelle court jusqu'à aujourd'hui.
	etapeA := baseEtape("a", "Gros œuvre")
	etapeA.PlannedStart = day(1)
	etapeA.PlannedEnd = day(10)
	etapeA.ActualStart = day(2)

	// B dépend de A (pas prête), C est indépendante (prête).
	etapeB := baseEtape("b", "Charpente")
	etapeB.PlannedStart = day(11)
	etapeB.PlannedEnd = day(20)
	etapeB.DependsOn = []planning.ID{"a"}

	etapeC := baseEtape("c", "Clôture")
	etapeC.PlannedStart = day(16)
	etapeC.PlannedEnd = day(30)

	view := planning.BuildGantt([]planning.Etape{etapeA, etapeB, etapeC}, nil, day(15))

	// Plage : du 1er au 30 inclus → 30 jours.
	a, b, c := view.Rows[0], view.Rows[1], view.Rows[2]

	if a.Statut != planning.StatutEnCours || !a.EnRetard || a.RetardJours != 5 {
		t.Errorf("A : statut %q, retard %t/%d — attendu en_cours, 5 jours", a.Statut, a.EnRetard, a.RetardJours)
	}
	if !a.Started {
		t.Fatal("A.Started = false")
	}
	// Barre réelle : du jour 2 (1/30) à la fin d'aujourd'hui, jour 15 (15/30).
	if a.ActualFrom != 33 || a.ActualTo != 500 {
		t.Errorf("A réel : [%d, %d] millièmes, attendu [33, 500]", a.ActualFrom, a.ActualTo)
	}

	if a.PreteADemarrer {
		t.Error("A démarrée ne peut pas être « prête à démarrer »")
	}
	if b.PreteADemarrer {
		t.Error("B attend A : pas prête à démarrer")
	}
	if !c.PreteADemarrer {
		t.Error("C sans prérequis doit être prête à démarrer")
	}
	if b.Started || c.Started {
		t.Error("B et C n'ont pas de barre réelle")
	}
}

func TestBuildGanttEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("aucune étape ni jalon : vue vide", func(t *testing.T) {
		t.Parallel()

		view := planning.BuildGantt(nil, nil, day(1))
		if !view.Empty {
			t.Error("Empty = false sans rien à dessiner")
		}
	})

	t.Run("une seule étape d'un jour remplit la plage", func(t *testing.T) {
		t.Parallel()

		etape := baseEtape("a", "Livraison")
		etape.PlannedStart = day(5)
		etape.PlannedEnd = day(5)

		view := planning.BuildGantt([]planning.Etape{etape}, nil, day(5))
		if view.Empty {
			t.Fatal("Empty = true avec une étape")
		}
		row := view.Rows[0]
		if row.PlannedFrom != 0 || row.PlannedTo != 1000 {
			t.Errorf("étape d'un jour : [%d, %d] millièmes, attendu [0, 1000]", row.PlannedFrom, row.PlannedTo)
		}
	})

	t.Run("une étape démarrée aujourd'hui a une barre réelle visible", func(t *testing.T) {
		t.Parallel()

		etape := baseEtape("a", "Gros œuvre")
		etape.PlannedStart = day(1)
		etape.PlannedEnd = day(30)
		etape.ActualStart = day(30)

		view := planning.BuildGantt([]planning.Etape{etape}, nil, day(30))
		row := view.Rows[0]
		if !row.Started || row.ActualTo <= row.ActualFrom {
			t.Errorf("barre réelle invisible : [%d, %d]", row.ActualFrom, row.ActualTo)
		}
	})

	t.Run("une étape d'un jour reste visible sur une plage de plus de mille jours", func(t *testing.T) {
		t.Parallel()

		// La plage fait 1201 jours : un jour vaut moins d'un millième, et sans
		// largeur minimale la barre de l'étape courte s'écraserait à zéro —
		// en contradiction avec l'invariant PlannedTo > PlannedFrom.
		short := baseEtape("court", "Livraison")
		short.PlannedStart = day(1)
		short.PlannedEnd = day(1)

		long := baseEtape("long", "Gros œuvre")
		long.PlannedStart = day(1)
		long.PlannedEnd = day(1).Add(1200 * 24 * time.Hour)

		// Une étape d'un jour posée sur le DERNIER jour de la plage : la
		// largeur minimale ne doit pas la faire déborder de [0, 1000].
		last := baseEtape("fin", "Réception")
		last.PlannedStart = long.PlannedEnd
		last.PlannedEnd = long.PlannedEnd

		view := planning.BuildGantt([]planning.Etape{short, long, last}, nil, day(1))

		for _, row := range view.Rows {
			if row.PlannedTo <= row.PlannedFrom {
				t.Errorf("%s : [%d, %d] millièmes, l'invariant To > From est rompu", row.Name, row.PlannedFrom, row.PlannedTo)
			}
			if row.PlannedFrom < 0 || row.PlannedTo > 1000 {
				t.Errorf("%s : [%d, %d] millièmes, hors de [0, 1000]", row.Name, row.PlannedFrom, row.PlannedTo)
			}
		}
	})

	t.Run("un jalon seul suffit à dessiner", func(t *testing.T) {
		t.Parallel()

		jalon := planning.Jalon{ID: "j", Name: "Signature", Date: day(3)}

		view := planning.BuildGantt(nil, []planning.Jalon{jalon}, day(10))
		if view.Empty {
			t.Fatal("Empty = true avec un jalon")
		}
		if got := view.Jalons[0].Position; got != 0 {
			t.Errorf("Jalon.Position = %d, attendu 0 (seul jour de la plage)", got)
		}
		if !view.Jalons[0].EnRetard || view.Jalons[0].RetardJours != 7 {
			t.Errorf("jalon du 3 au 10 : retard %t/%d, attendu 7 jours", view.Jalons[0].EnRetard, view.Jalons[0].RetardJours)
		}
	})

	t.Run("une étape en cours étend la plage jusqu'à aujourd'hui", func(t *testing.T) {
		t.Parallel()

		etape := baseEtape("a", "Gros œuvre")
		etape.PlannedStart = day(1)
		etape.PlannedEnd = day(5)
		etape.ActualStart = day(1)

		view := planning.BuildGantt([]planning.Etape{etape}, nil, day(20))
		if !view.To.Equal(day(21)) {
			t.Errorf("To = %v, attendu le 21 (aujourd'hui inclus)", view.To)
		}
	})
}

// TestServiceGantt vérifie l'assemblage par le service : lecture des deux
// listes, dérivation, rien de stocké.
func TestServiceGantt(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	in := etapeInput("Gros œuvre")
	in.PlannedStart = day(1)
	in.PlannedEnd = day(10)
	f.etape(t, in)

	f.jalon(t, "Hors d'eau", day(20))

	view, err := f.service.Gantt(t.Context(), day(15))
	if err != nil {
		t.Fatalf("Gantt() échoué : %v", err)
	}
	if len(view.Rows) != 1 || len(view.Jalons) != 1 {
		t.Fatalf("Gantt() : %d lignes, %d jalons — attendu 1 et 1", len(view.Rows), len(view.Jalons))
	}
	if !view.Rows[0].EnRetard {
		t.Error("l'étape prévue du 1 au 10 est en retard le 15")
	}
}
