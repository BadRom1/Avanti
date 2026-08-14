package planning_test

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Romain-Badino/Avanti/internal/planning"
)

func TestNewServiceRequiresRepo(t *testing.T) {
	t.Parallel()

	if _, err := planning.NewService(planning.ServiceOptions{}); err == nil {
		t.Error("NewService(sans dépôt) doit échouer")
	}
}

func TestCreateEtapeStoresNormalizedEtape(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	in := etapeInput("  Réseau   électrique  ")
	in.Description = "  Tableau et circuits.  "
	in.DevisID = "  5b9d2c40-8f6e-4c11-9d7a-3e2f1a0b9c8d  "
	paris := time.FixedZone("Europe/Paris", 2*3600)
	in.PlannedStart = time.Date(2026, time.April, 1, 8, 0, 0, 0, paris)
	in.PlannedEnd = time.Date(2026, time.April, 20, 18, 0, 0, 0, paris)

	created, err := f.service.CreateEtape(t.Context(), in)
	if err != nil {
		t.Fatalf("CreateEtape() échoué : %v", err)
	}

	if created.Name != "Réseau électrique" {
		t.Errorf("Name = %q, blancs non normalisés", created.Name)
	}
	if created.Description != "Tableau et circuits." {
		t.Errorf("Description = %q, bordures non nettoyées", created.Description)
	}
	if created.DevisID != "5b9d2c40-8f6e-4c11-9d7a-3e2f1a0b9c8d" {
		t.Errorf("DevisID = %q, bordures non nettoyées", created.DevisID)
	}
	if created.PlannedStart.Location() != time.UTC || created.PlannedEnd.Location() != time.UTC {
		t.Error("les dates prévues ne sont pas en UTC")
	}
	if created.Statut() != planning.StatutPrevue {
		t.Errorf("Statut() = %q, une étape naît prévue", created.Statut())
	}

	stored, err := f.service.Etape(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("Etape() échoué : %v", err)
	}
	if stored.Name != created.Name {
		t.Errorf("l'étape stockée diffère : %q ≠ %q", stored.Name, created.Name)
	}
}

func TestCreateEtapeRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	longText := strings.Repeat("x", 2001)

	cases := []struct {
		name string
		mut  func(*planning.EtapeInput)
		want error
	}{
		{name: "nom vide", mut: func(in *planning.EtapeInput) { in.Name = "   " }, want: planning.ErrEmptyName},
		{name: "nom trop long", mut: func(in *planning.EtapeInput) { in.Name = strings.Repeat("a", 121) }, want: planning.ErrTextTooLong},
		{name: "description trop longue", mut: func(in *planning.EtapeInput) { in.Description = longText }, want: planning.ErrTextTooLong},
		{name: "devis trop long", mut: func(in *planning.EtapeInput) { in.DevisID = strings.Repeat("d", 256) }, want: planning.ErrInvalidDevisID},
		{name: "début manquant", mut: func(in *planning.EtapeInput) { in.PlannedStart = time.Time{} }, want: planning.ErrMissingDate},
		{name: "fin manquante", mut: func(in *planning.EtapeInput) { in.PlannedEnd = time.Time{} }, want: planning.ErrMissingDate},
		{name: "fin avant début", mut: func(in *planning.EtapeInput) { in.PlannedEnd = in.PlannedStart.Add(-time.Hour) }, want: planning.ErrInvalidPlannedRange},
		{name: "acteur manquant", mut: func(in *planning.EtapeInput) { in.By = "" }, want: planning.ErrMissingActor},
		{name: "prérequis en double", mut: func(in *planning.EtapeInput) { in.DependsOn = []planning.ID{"dep", "dep"} }, want: planning.ErrDuplicateDependency},
		{name: "trop de prérequis", mut: func(in *planning.EtapeInput) {
			for i := range 51 {
				in.DependsOn = append(in.DependsOn, planning.ID("dep-"+strconv.Itoa(i)))
			}
		}, want: planning.ErrTooManyDependencies},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t)
			in := etapeInput("Charpente")
			tc.mut(&in)

			if _, err := f.service.CreateEtape(t.Context(), in); !errors.Is(err, tc.want) {
				t.Errorf("CreateEtape() = %v, attendu %v", err, tc.want)
			}
		})
	}

	t.Run("un nom de 120 caractères passe, une fin égale au début aussi", func(t *testing.T) {
		t.Parallel()

		f := newFixture(t)
		in := etapeInput(strings.Repeat("a", 120))
		in.PlannedEnd = in.PlannedStart

		if _, err := f.service.CreateEtape(t.Context(), in); err != nil {
			t.Errorf("CreateEtape() = %v, attendu nil sur les bornes exactes", err)
		}
	})
}

func TestCreateEtapeChecksDependencies(t *testing.T) {
	t.Parallel()

	t.Run("un prérequis inconnu est refusé", func(t *testing.T) {
		t.Parallel()

		f := newFixture(t)
		in := etapeInput("Charpente")
		in.DependsOn = []planning.ID{"11111111-1111-4111-8111-111111111111"}

		if _, err := f.service.CreateEtape(t.Context(), in); !errors.Is(err, planning.ErrUnknownDependency) {
			t.Errorf("CreateEtape() = %v, attendu ErrUnknownDependency", err)
		}
	})

	t.Run("une chaîne valide passe, le losange aussi", func(t *testing.T) {
		t.Parallel()

		f := newFixture(t)
		a := f.etape(t, etapeInput("Gros œuvre"))

		b := etapeInput("Charpente")
		b.DependsOn = []planning.ID{a.ID}
		etapeB := f.etape(t, b)

		c := etapeInput("Couverture")
		c.DependsOn = []planning.ID{a.ID}
		etapeC := f.etape(t, c)

		d := etapeInput("Menuiseries")
		d.DependsOn = []planning.ID{etapeB.ID, etapeC.ID}
		if _, err := f.service.CreateEtape(t.Context(), d); err != nil {
			t.Errorf("CreateEtape(losange) = %v, attendu nil", err)
		}
	})
}

func TestUpdateEtapeRejectsCycle(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	first, second := f.chain(t) // second dépend de first

	// Refermer le cycle : first dépendrait de second.
	in := updateInputFrom(first)
	in.DependsOn = []planning.ID{second.ID}

	if _, err := f.service.UpdateEtape(t.Context(), first.ID, in); !errors.Is(err, planning.ErrDependencyCycle) {
		t.Errorf("UpdateEtape(cycle) = %v, attendu ErrDependencyCycle", err)
	}
}

// updateInputFrom rend l'entrée de modification qui resoumet l'étape telle
// quelle — ce qu'un formulaire pré-rempli enverrait.
func updateInputFrom(etape planning.Etape) planning.UpdateEtapeInput {
	return planning.UpdateEtapeInput{
		Name:         etape.Name,
		Description:  etape.Description,
		PlannedStart: etape.PlannedStart,
		PlannedEnd:   etape.PlannedEnd,
		DependsOn:    etape.DependsOn,
		DevisID:      etape.DevisID,
		Expected:     etape.UpdatedAt,
		By:           acteur,
	}
}

func TestUpdateEtape(t *testing.T) {
	t.Parallel()

	t.Run("modifie nom, dates, devis et dépendances", func(t *testing.T) {
		t.Parallel()

		f := newFixture(t)
		a := f.etape(t, etapeInput("Gros œuvre"))
		b := f.etape(t, etapeInput("Charpente"))

		in := updateInputFrom(b)
		in.Name = "Charpente et couverture"
		in.PlannedEnd = b.PlannedEnd.Add(7 * 24 * time.Hour)
		in.DependsOn = []planning.ID{a.ID}
		in.DevisID = "5b9d2c40-8f6e-4c11-9d7a-3e2f1a0b9c8d"

		f.now = f.now.Add(time.Hour)
		updated, err := f.service.UpdateEtape(t.Context(), b.ID, in)
		if err != nil {
			t.Fatalf("UpdateEtape() échoué : %v", err)
		}
		if updated.Name != "Charpente et couverture" {
			t.Errorf("Name = %q", updated.Name)
		}
		if len(updated.DependsOn) != 1 || updated.DependsOn[0] != a.ID {
			t.Errorf("DependsOn = %v", updated.DependsOn)
		}
		if !updated.UpdatedAt.After(b.UpdatedAt) {
			t.Error("UpdatedAt n'a pas avancé")
		}
	})

	t.Run("garde optimiste : un expected périmé est refusé", func(t *testing.T) {
		t.Parallel()

		f := newFixture(t)
		etape := f.etape(t, etapeInput("Charpente"))

		in := updateInputFrom(etape)
		in.Expected = etape.UpdatedAt.Add(-time.Minute)

		if _, err := f.service.UpdateEtape(t.Context(), etape.ID, in); !errors.Is(err, planning.ErrConcurrentUpdate) {
			t.Errorf("UpdateEtape(expected périmé) = %v, attendu ErrConcurrentUpdate", err)
		}
	})

	t.Run("étape inconnue", func(t *testing.T) {
		t.Parallel()

		f := newFixture(t)
		in := updateInputFrom(baseEtape("fantome", "Fantôme"))

		if _, err := f.service.UpdateEtape(t.Context(), "fantome", in); !errors.Is(err, planning.ErrUnknownEtape) {
			t.Errorf("UpdateEtape(inconnue) = %v, attendu ErrUnknownEtape", err)
		}
	})

	t.Run("le nom d'une étape terminée reste corrigeable", func(t *testing.T) {
		t.Parallel()

		f := newFixture(t)
		etape := f.etape(t, etapeInput("Charpente"))
		finished := f.startAndFinish(t, etape.ID)

		in := updateInputFrom(finished)
		in.Name = "Charpente (lot 2)"

		updated, err := f.service.UpdateEtape(t.Context(), etape.ID, in)
		if err != nil {
			t.Fatalf("UpdateEtape(terminée) = %v, attendu nil", err)
		}
		if updated.Name != "Charpente (lot 2)" {
			t.Errorf("Name = %q", updated.Name)
		}
	})

	t.Run("les dépendances d'une étape démarrée sont verrouillées", func(t *testing.T) {
		t.Parallel()

		f := newFixture(t)
		first, second := f.chain(t)
		f.startAndFinish(t, first.ID)

		current, err := f.service.Etape(t.Context(), second.ID)
		if err != nil {
			t.Fatalf("Etape() échoué : %v", err)
		}
		started, err := f.service.StartEtape(t.Context(), second.ID, current.UpdatedAt, acteur)
		if err != nil {
			t.Fatalf("StartEtape() échoué : %v", err)
		}

		// Retirer le prérequis après le démarrage : refusé.
		in := updateInputFrom(started)
		in.DependsOn = nil
		if _, err := f.service.UpdateEtape(t.Context(), second.ID, in); !errors.Is(err, planning.ErrDependenciesLocked) {
			t.Errorf("UpdateEtape(dépendances d'une étape démarrée) = %v, attendu ErrDependenciesLocked", err)
		}

		// Les resoumettre à l'identique, en corrigeant le nom : permis.
		in = updateInputFrom(started)
		in.Name = "Charpente traitée"
		if _, err := f.service.UpdateEtape(t.Context(), second.ID, in); err != nil {
			t.Errorf("UpdateEtape(mêmes dépendances) = %v, attendu nil", err)
		}
	})

	t.Run("acteur manquant", func(t *testing.T) {
		t.Parallel()

		f := newFixture(t)
		etape := f.etape(t, etapeInput("Charpente"))
		in := updateInputFrom(etape)
		in.By = ""

		if _, err := f.service.UpdateEtape(t.Context(), etape.ID, in); !errors.Is(err, planning.ErrMissingActor) {
			t.Errorf("UpdateEtape(sans acteur) = %v, attendu ErrMissingActor", err)
		}
	})
}

func TestStartEtape(t *testing.T) {
	t.Parallel()

	t.Run("refuse tant qu'un prérequis n'est pas terminé, en le nommant", func(t *testing.T) {
		t.Parallel()

		f := newFixture(t)
		_, second := f.chain(t)

		_, err := f.service.StartEtape(t.Context(), second.ID, second.UpdatedAt, acteur)
		if !errors.Is(err, planning.ErrPrerequisitesNotDone) {
			t.Fatalf("StartEtape(prérequis non terminé) = %v, attendu ErrPrerequisitesNotDone", err)
		}
		if !strings.Contains(err.Error(), "Gros œuvre") {
			t.Errorf("le refus %q ne nomme pas l'étape bloquante", err)
		}
	})

	t.Run("un prérequis seulement démarré bloque encore", func(t *testing.T) {
		t.Parallel()

		f := newFixture(t)
		first, second := f.chain(t)

		if _, err := f.service.StartEtape(t.Context(), first.ID, first.UpdatedAt, acteur); err != nil {
			t.Fatalf("StartEtape(première) échoué : %v", err)
		}

		if _, err := f.service.StartEtape(t.Context(), second.ID, second.UpdatedAt, acteur); !errors.Is(err, planning.ErrPrerequisitesNotDone) {
			t.Errorf("StartEtape(prérequis en cours) = %v, attendu ErrPrerequisitesNotDone", err)
		}
	})

	t.Run("démarre une fois les prérequis terminés", func(t *testing.T) {
		t.Parallel()

		f := newFixture(t)
		first, second := f.chain(t)
		f.startAndFinish(t, first.ID)

		started, err := f.service.StartEtape(t.Context(), second.ID, second.UpdatedAt, acteur)
		if err != nil {
			t.Fatalf("StartEtape() échoué : %v", err)
		}
		if started.Statut() != planning.StatutEnCours {
			t.Errorf("Statut() = %q, attendu en_cours", started.Statut())
		}
	})

	t.Run("garde optimiste et acteur", func(t *testing.T) {
		t.Parallel()

		f := newFixture(t)
		etape := f.etape(t, etapeInput("Charpente"))

		if _, err := f.service.StartEtape(t.Context(), etape.ID, etape.UpdatedAt.Add(-time.Minute), acteur); !errors.Is(err, planning.ErrConcurrentUpdate) {
			t.Errorf("StartEtape(expected périmé) = %v, attendu ErrConcurrentUpdate", err)
		}
		if _, err := f.service.StartEtape(t.Context(), etape.ID, etape.UpdatedAt, ""); !errors.Is(err, planning.ErrMissingActor) {
			t.Errorf("StartEtape(sans acteur) = %v, attendu ErrMissingActor", err)
		}
	})

	t.Run("un double démarrage est refusé par l'entité", func(t *testing.T) {
		t.Parallel()

		f := newFixture(t)
		etape := f.etape(t, etapeInput("Charpente"))

		started, err := f.service.StartEtape(t.Context(), etape.ID, etape.UpdatedAt, acteur)
		if err != nil {
			t.Fatalf("StartEtape() échoué : %v", err)
		}
		if _, err := f.service.StartEtape(t.Context(), etape.ID, started.UpdatedAt, acteur); !errors.Is(err, planning.ErrEtapeAlreadyStarted) {
			t.Errorf("second StartEtape() = %v, attendu ErrEtapeAlreadyStarted", err)
		}
	})
}

func TestFinishEtape(t *testing.T) {
	t.Parallel()

	t.Run("termine une étape démarrée", func(t *testing.T) {
		t.Parallel()

		f := newFixture(t)
		etape := f.etape(t, etapeInput("Charpente"))
		finished := f.startAndFinish(t, etape.ID)

		if finished.Statut() != planning.StatutTerminee {
			t.Errorf("Statut() = %q, attendu terminee", finished.Statut())
		}
	})

	t.Run("refuse une étape non démarrée", func(t *testing.T) {
		t.Parallel()

		f := newFixture(t)
		etape := f.etape(t, etapeInput("Charpente"))

		if _, err := f.service.FinishEtape(t.Context(), etape.ID, etape.UpdatedAt, acteur); !errors.Is(err, planning.ErrEtapeNotStarted) {
			t.Errorf("FinishEtape(non démarrée) = %v, attendu ErrEtapeNotStarted", err)
		}
	})

	t.Run("garde optimiste", func(t *testing.T) {
		t.Parallel()

		f := newFixture(t)
		etape := f.etape(t, etapeInput("Charpente"))

		if _, err := f.service.FinishEtape(t.Context(), etape.ID, etape.UpdatedAt.Add(time.Minute), acteur); !errors.Is(err, planning.ErrConcurrentUpdate) {
			t.Errorf("FinishEtape(expected périmé) = %v, attendu ErrConcurrentUpdate", err)
		}
	})
}

func TestEtapesAreSorted(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	late := etapeInput("Peinture")
	late.PlannedStart = debutPrevu.Add(60 * 24 * time.Hour)
	late.PlannedEnd = late.PlannedStart.Add(10 * 24 * time.Hour)
	f.etape(t, late)

	f.etape(t, etapeInput("Gros œuvre"))

	etapes, err := f.service.Etapes(t.Context())
	if err != nil {
		t.Fatalf("Etapes() échoué : %v", err)
	}
	if len(etapes) != 2 || etapes[0].Name != "Gros œuvre" {
		t.Errorf("Etapes() mal triées : %v", etapes)
	}
}

func TestJalonLifecycle(t *testing.T) {
	t.Parallel()

	t.Run("création, tri par date, atteinte", func(t *testing.T) {
		t.Parallel()

		f := newFixture(t)
		late := f.jalon(t, "Réception", finPrevue.Add(90*24*time.Hour))
		early := f.jalon(t, "Hors d'eau", finPrevue)

		jalons, err := f.service.Jalons(t.Context())
		if err != nil {
			t.Fatalf("Jalons() échoué : %v", err)
		}
		if len(jalons) != 2 || jalons[0].ID != early.ID || jalons[1].ID != late.ID {
			t.Errorf("Jalons() mal triés : %v", jalons)
		}

		reached, err := f.service.ReachJalon(t.Context(), early.ID, early.UpdatedAt, acteur)
		if err != nil {
			t.Fatalf("ReachJalon() échoué : %v", err)
		}
		if !reached.Atteint() {
			t.Error("Atteint() = false après ReachJalon")
		}

		if _, err := f.service.ReachJalon(t.Context(), early.ID, reached.UpdatedAt, acteur); !errors.Is(err, planning.ErrJalonAlreadyReached) {
			t.Errorf("second ReachJalon() = %v, attendu ErrJalonAlreadyReached", err)
		}
	})

	t.Run("refus de création invalide", func(t *testing.T) {
		t.Parallel()

		f := newFixture(t)

		if _, err := f.service.CreateJalon(t.Context(), planning.JalonInput{Name: " ", Date: finPrevue, By: acteur}); !errors.Is(err, planning.ErrEmptyName) {
			t.Errorf("CreateJalon(sans nom) = %v, attendu ErrEmptyName", err)
		}
		if _, err := f.service.CreateJalon(t.Context(), planning.JalonInput{Name: "Réception", By: acteur}); !errors.Is(err, planning.ErrMissingDate) {
			t.Errorf("CreateJalon(sans date) = %v, attendu ErrMissingDate", err)
		}
		if _, err := f.service.CreateJalon(t.Context(), planning.JalonInput{Name: "Réception", Date: finPrevue}); !errors.Is(err, planning.ErrMissingActor) {
			t.Errorf("CreateJalon(sans acteur) = %v, attendu ErrMissingActor", err)
		}
	})

	t.Run("garde optimiste sur l'atteinte", func(t *testing.T) {
		t.Parallel()

		f := newFixture(t)
		jalon := f.jalon(t, "Hors d'eau", finPrevue)

		if _, err := f.service.ReachJalon(t.Context(), jalon.ID, jalon.UpdatedAt.Add(-time.Second), acteur); !errors.Is(err, planning.ErrConcurrentUpdate) {
			t.Errorf("ReachJalon(expected périmé) = %v, attendu ErrConcurrentUpdate", err)
		}
	})

	t.Run("jalon inconnu", func(t *testing.T) {
		t.Parallel()

		f := newFixture(t)

		if _, err := f.service.ReachJalon(t.Context(), "fantome", instantSaisie, acteur); !errors.Is(err, planning.ErrUnknownJalon) {
			t.Errorf("ReachJalon(inconnu) = %v, attendu ErrUnknownJalon", err)
		}
	})
}

func TestUpdateEtapeRejectsSelfDependency(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	etape := f.etape(t, etapeInput("Charpente"))

	in := updateInputFrom(etape)
	in.DependsOn = []planning.ID{etape.ID}

	if _, err := f.service.UpdateEtape(t.Context(), etape.ID, in); !errors.Is(err, planning.ErrSelfDependency) {
		t.Errorf("UpdateEtape(auto-référence) = %v, attendu ErrSelfDependency", err)
	}
}

func TestJalonByID(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	jalon := f.jalon(t, "Hors d'eau", finPrevue)

	stored, err := f.service.Jalon(t.Context(), jalon.ID)
	if err != nil {
		t.Fatalf("Jalon() échoué : %v", err)
	}
	if stored.Name != "Hors d'eau" {
		t.Errorf("Jalon().Name = %q", stored.Name)
	}

	if _, err := f.service.Jalon(t.Context(), "fantome"); !errors.Is(err, planning.ErrUnknownJalon) {
		t.Errorf("Jalon(inconnu) = %v, attendu ErrUnknownJalon", err)
	}
}

func TestStringAccessors(t *testing.T) {
	t.Parallel()

	if got := planning.StatutEnCours.String(); got != "en_cours" {
		t.Errorf("Statut.String() = %q", got)
	}
	if got := planning.ActeurID("abc").String(); got != "abc" {
		t.Errorf("ActeurID.String() = %q", got)
	}
	if got := planning.ID("def").String(); got != "def" {
		t.Errorf("ID.String() = %q", got)
	}
}

// TestServicePropagatesRepoFailures vérifie qu'une panne du dépôt remonte
// telle quelle au lieu de se déguiser en refus métier.
func TestServicePropagatesRepoFailures(t *testing.T) {
	t.Parallel()

	breakdown := errors.New("panne de dépôt")

	f := newFixture(t)
	etape := f.etape(t, etapeInput("Charpente"))

	f.repo.failOn("ListEtapes", breakdown)
	in := etapeInput("Couverture")
	in.DependsOn = []planning.ID{etape.ID}
	if _, err := f.service.CreateEtape(t.Context(), in); !errors.Is(err, breakdown) {
		t.Errorf("CreateEtape(panne ListEtapes) = %v, attendu la panne", err)
	}

	f.repo.failOn("ListEtapes", nil)
	f.repo.failOn("EtapeByID", breakdown)
	if _, err := f.service.StartEtape(t.Context(), etape.ID, etape.UpdatedAt, acteur); !errors.Is(err, breakdown) {
		t.Errorf("StartEtape(panne EtapeByID) = %v, attendu la panne", err)
	}
	if _, err := f.service.FinishEtape(t.Context(), etape.ID, etape.UpdatedAt, acteur); !errors.Is(err, breakdown) {
		t.Errorf("FinishEtape(panne EtapeByID) = %v, attendu la panne", err)
	}
	f.repo.failOn("ListEtapes", breakdown)
	if _, err := f.service.Gantt(t.Context(), instantSaisie); !errors.Is(err, breakdown) {
		t.Errorf("Gantt(panne ListEtapes) = %v, attendu la panne", err)
	}
}

func TestFinishEtapeRequiresActor(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	etape := f.etape(t, etapeInput("Charpente"))

	if _, err := f.service.FinishEtape(t.Context(), etape.ID, etape.UpdatedAt, ""); !errors.Is(err, planning.ErrMissingActor) {
		t.Errorf("FinishEtape(sans acteur) = %v, attendu ErrMissingActor", err)
	}
	if _, err := f.service.ReachJalon(t.Context(), "j", instantSaisie, ""); !errors.Is(err, planning.ErrMissingActor) {
		t.Errorf("ReachJalon(sans acteur) = %v, attendu ErrMissingActor", err)
	}
}
