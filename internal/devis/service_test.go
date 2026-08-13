package devis_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Romain-Badino/Avanti/internal/devis"
)

// errDepot est la panne injectée dans le dépôt : elle n'a aucun sens métier, ce
// qui est exactement le point — le service doit la laisser passer telle quelle
// plutôt que de la traduire en refus.
var errDepot = errors.New("dépôt injoignable")

func TestNewServiceRejectsMissingRepo(t *testing.T) {
	t.Parallel()

	if _, err := devis.NewService(devis.ServiceOptions{}); err == nil {
		t.Error("NewService() sans dépôt doit échouer")
	}
}

// TestNewServiceFallsBackToDefaults : sans horloge ni générateur, le service
// prend ceux de la bibliothèque standard plutôt que de déréférencer nil à la
// première écriture.
func TestNewServiceFallsBackToDefaults(t *testing.T) {
	t.Parallel()

	repo := newMemRepo()
	service, err := devis.NewService(devis.ServiceOptions{Repo: repo})
	if err != nil {
		t.Fatalf("NewService() échoué : %v", err)
	}

	before := time.Now().UTC()
	demande, err := service.CreateDemande(t.Context(), devis.DemandeInput{
		Lot: "Charpente", SentAt: instantEnvoi, By: acteur,
	})
	if err != nil {
		t.Fatalf("CreateDemande() échoué : %v", err)
	}

	if len(demande.ID.String()) != 36 {
		t.Errorf("ID = %q, un UUID canonique était attendu", demande.ID)
	}
	if demande.CreatedAt.Before(before) {
		t.Errorf("CreatedAt = %s, antérieur au début du test (%s)", demande.CreatedAt, before)
	}
}

func TestCreateDemande(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	demande, err := f.service.CreateDemande(t.Context(), devis.DemandeInput{
		Lot:         "  Charpente  et couverture ",
		Description: "  Remplacement de la charpente, 90 m².  ",
		Artisans: []devis.Artisan{
			{Entreprise: " Charpentes du Val ", Email: "Contact@Val.fr"},
			{Entreprise: "", Email: "", Telephone: ""},
		},
		SentAt: instantEnvoi,
		By:     acteur,
	})
	if err != nil {
		t.Fatalf("CreateDemande() échoué : %v", err)
	}

	switch {
	case demande.Lot != "Charpente et couverture":
		t.Errorf("Lot = %q, la normalisation n'a pas eu lieu", demande.Lot)
	case demande.Description != "Remplacement de la charpente, 90 m².":
		t.Errorf("Description = %q", demande.Description)
	case len(demande.Artisans) != 1 || demande.Artisans[0].Email != "contact@val.fr":
		t.Errorf("Artisans = %+v, attendu une seule entrée normalisée", demande.Artisans)
	case demande.CreatedBy != acteur:
		t.Errorf("CreatedBy = %q, attendu %q", demande.CreatedBy, acteur)
	case !demande.SentAt.Equal(instantEnvoi):
		t.Errorf("SentAt = %s, attendu %s", demande.SentAt, instantEnvoi)
	case !demande.CreatedAt.Equal(instantSaisie) || !demande.UpdatedAt.Equal(instantSaisie):
		t.Errorf("horodatages = (%s, %s), attendu %s", demande.CreatedAt, demande.UpdatedAt, instantSaisie)
	}

	// Ce qui est rendu est bien ce qui est stocké : un service qui rendrait un
	// objet plus complet que la ligne écrite mentirait à son appelant.
	stored, err := f.repo.DemandeByID(t.Context(), demande.ID)
	if err != nil {
		t.Fatalf("DemandeByID() échoué : %v", err)
	}
	if stored.Lot != demande.Lot || stored.CreatedBy != demande.CreatedBy {
		t.Errorf("la demande stockée diffère de celle rendue : %+v", stored)
	}
}

func TestCreateDemandeRejects(t *testing.T) {
	t.Parallel()

	valid := devis.DemandeInput{Lot: "Charpente", SentAt: instantEnvoi, By: acteur}

	cases := map[string]struct {
		mutate func(in *devis.DemandeInput)
		want   error
	}{
		"sans intitulé de lot": {
			mutate: func(in *devis.DemandeInput) { in.Lot = "  " },
			want:   devis.ErrEmptyLot,
		},
		"description trop longue": {
			mutate: func(in *devis.DemandeInput) { in.Description = strings.Repeat("a", 4001) },
			want:   devis.ErrTextTooLong,
		},
		"artisan sans entreprise": {
			mutate: func(in *devis.DemandeInput) {
				in.Artisans = []devis.Artisan{{Email: "contact@val.fr"}}
			},
			want: devis.ErrEmptyEntreprise,
		},
		"email d'artisan invalide": {
			mutate: func(in *devis.DemandeInput) {
				in.Artisans = []devis.Artisan{{Entreprise: "Toiture Ain", Email: "pas-une-adresse"}}
			},
			want: devis.ErrInvalidArtisanEmail,
		},
		"sans date d'envoi": {
			mutate: func(in *devis.DemandeInput) { in.SentAt = time.Time{} },
			want:   devis.ErrMissingDate,
		},
		"sans acteur": {
			mutate: func(in *devis.DemandeInput) { in.By = "" },
			want:   devis.ErrMissingActor,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t)
			in := valid
			tc.mutate(&in)

			if _, err := f.service.CreateDemande(t.Context(), in); !errors.Is(err, tc.want) {
				t.Fatalf("CreateDemande() = %v, attendu %v", err, tc.want)
			}
			// Un refus n'écrit rien : une validation qui laisserait une ligne
			// derrière elle rendrait la liste des demandes intenable.
			demandes, listErr := f.repo.ListDemandes(t.Context())
			if listErr != nil {
				t.Fatalf("ListDemandes() échoué : %v", listErr)
			}
			if len(demandes) != 0 {
				t.Errorf("%d demande(s) écrite(s) malgré le refus", len(demandes))
			}
		})
	}
}

func TestCreateDemandePropagatesRepoFailure(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.repo.failOn("CreateDemande", errDepot)

	_, err := f.service.CreateDemande(t.Context(), devis.DemandeInput{
		Lot: "Charpente", SentAt: instantEnvoi, By: acteur,
	})
	if !errors.Is(err, errDepot) {
		t.Errorf("CreateDemande() = %v, attendu la panne du dépôt", err)
	}
}

func TestRecordDevis(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	demande := f.demande(t)

	proposition, err := f.service.RecordDevis(t.Context(), devis.DevisInput{
		DemandeID:   demande.ID,
		Artisan:     devis.Artisan{Entreprise: " Charpentes du Val ", Email: "Contact@Val.fr"},
		Montant:     1_250_000,
		ReceivedAt:  instantReponse,
		Validity:    30 * 24 * time.Hour,
		Notes:       "  Pose sous quinze jours.  ",
		DocumentIDs: []string{" doc-1 ", "doc-1", "", "doc-2"},
		By:          acteur,
	})
	if err != nil {
		t.Fatalf("RecordDevis() échoué : %v", err)
	}

	switch {
	case proposition.Statut != devis.StatutRecu:
		t.Errorf("Statut = %q, un devis naît %q", proposition.Statut, devis.StatutRecu)
	case proposition.DemandeID != demande.ID:
		t.Errorf("DemandeID = %q, attendu %q", proposition.DemandeID, demande.ID)
	case proposition.Artisan.Entreprise != "Charpentes du Val":
		t.Errorf("Artisan = %+v, la normalisation n'a pas eu lieu", proposition.Artisan)
	case proposition.Montant != 1_250_000:
		t.Errorf("Montant = %d, attendu 1250000", int64(proposition.Montant))
	case proposition.Notes != "Pose sous quinze jours.":
		t.Errorf("Notes = %q", proposition.Notes)
	case !slices.Equal(proposition.DocumentIDs, []string{"doc-1", "doc-2"}):
		t.Errorf("DocumentIDs = %v, attendu [doc-1 doc-2]", proposition.DocumentIDs)
	case proposition.RecordedBy != acteur:
		t.Errorf("RecordedBy = %q, attendu %q", proposition.RecordedBy, acteur)
	case !proposition.DecidedAt.IsZero() || proposition.DecidedBy != "":
		t.Errorf("un devis reçu ne porte aucune décision : %q, %s", proposition.DecidedBy, proposition.DecidedAt)
	}
}

// TestRecordDevisWithoutDocuments : sans pièce jointe, la tranche est nil et non
// une tranche vide — c'est ce que la base stockera, et un test qui l'ignore
// laisse passer un aller-retour qui change la valeur.
func TestRecordDevisWithoutDocuments(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	demande := f.demande(t)

	proposition := f.devisRecu(t, demande.ID, "Toiture Ain", 1_180_050)
	if proposition.DocumentIDs != nil {
		t.Errorf("DocumentIDs = %v, attendu nil", proposition.DocumentIDs)
	}
}

func TestRecordDevisRejects(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		mutate func(in *devis.DevisInput)
		want   error
	}{
		"sans demande de rattachement": {
			mutate: func(in *devis.DevisInput) { in.DemandeID = "" },
			want:   devis.ErrMissingDemande,
		},
		"demande inconnue": {
			mutate: func(in *devis.DevisInput) { in.DemandeID = "demande-fantome" },
			want:   devis.ErrUnknownDemande,
		},
		"montant nul": {
			mutate: func(in *devis.DevisInput) { in.Montant = 0 },
			want:   devis.ErrInvalidMontant,
		},
		"montant négatif": {
			mutate: func(in *devis.DevisInput) { in.Montant = -1 },
			want:   devis.ErrInvalidMontant,
		},
		"montant au-delà de la borne": {
			mutate: func(in *devis.DevisInput) { in.Montant = devis.MaxMontant + 1 },
			want:   devis.ErrInvalidMontant,
		},
		"sans date de réception": {
			mutate: func(in *devis.DevisInput) { in.ReceivedAt = time.Time{} },
			want:   devis.ErrMissingDate,
		},
		"validité négative": {
			mutate: func(in *devis.DevisInput) { in.Validity = -time.Hour },
			want:   devis.ErrNegativeValidity,
		},
		"sans acteur": {
			mutate: func(in *devis.DevisInput) { in.By = "" },
			want:   devis.ErrMissingActor,
		},
		"artisan sans entreprise": {
			mutate: func(in *devis.DevisInput) { in.Artisan = devis.Artisan{Email: "contact@val.fr"} },
			want:   devis.ErrEmptyEntreprise,
		},
		"notes trop longues": {
			mutate: func(in *devis.DevisInput) { in.Notes = strings.Repeat("a", 4001) },
			want:   devis.ErrTextTooLong,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t)
			demande := f.demande(t)

			in := devis.DevisInput{
				DemandeID:  demande.ID,
				Artisan:    devis.Artisan{Entreprise: "Charpentes du Val"},
				Montant:    1_250_000,
				ReceivedAt: instantReponse,
				By:         acteur,
			}
			tc.mutate(&in)

			if _, err := f.service.RecordDevis(t.Context(), in); !errors.Is(err, tc.want) {
				t.Fatalf("RecordDevis() = %v, attendu %v", err, tc.want)
			}
			propositions, listErr := f.repo.ListDevis(t.Context())
			if listErr != nil {
				t.Fatalf("ListDevis() échoué : %v", listErr)
			}
			if len(propositions) != 0 {
				t.Errorf("%d devis écrit(s) malgré le refus", len(propositions))
			}
		})
	}
}

// TestRecordDevisAcceptsBorderlineMontant vérifie les deux bornes du montant
// dans le sens permis : sans elles, un test de refus resterait vert même si la
// validation refusait tout.
func TestRecordDevisAcceptsBorderlineMontant(t *testing.T) {
	t.Parallel()

	for _, montant := range []devis.Montant{1, devis.MaxMontant} {
		f := newFixture(t)
		demande := f.demande(t)

		if _, err := f.service.RecordDevis(t.Context(), devis.DevisInput{
			DemandeID:  demande.ID,
			Artisan:    devis.Artisan{Entreprise: "Charpentes du Val"},
			Montant:    montant,
			ReceivedAt: instantReponse,
			By:         acteur,
		}); err != nil {
			t.Errorf("RecordDevis(%d) = %v, ce montant est recevable", int64(montant), err)
		}
	}
}

// TestRecordDevisRefusedOnClosedDemande : une demande tranchée n'accepte plus
// de devis. Y ajouter une offre laisserait croire que la comparaison est encore
// en jeu.
func TestRecordDevisRefusedOnClosedDemande(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	demande := f.demande(t)
	retenu := f.devisRecu(t, demande.ID, "Charpentes du Val", 1_250_000)

	if _, err := f.service.Retain(t.Context(), retenu.ID, acteur); err != nil {
		t.Fatalf("Retain() échoué : %v", err)
	}

	_, err := f.service.RecordDevis(t.Context(), devis.DevisInput{
		DemandeID:  demande.ID,
		Artisan:    devis.Artisan{Entreprise: "Toiture Ain"},
		Montant:    1_180_050,
		ReceivedAt: instantReponse,
		By:         acteur,
	})
	if !errors.Is(err, devis.ErrDemandeClosed) {
		t.Errorf("RecordDevis() sur demande close = %v, attendu %v", err, devis.ErrDemandeClosed)
	}
}

// TestRecordDevisAllowedAfterSingleRefusal : refuser une offre ne clôt pas la
// consultation. C'est la différence entre écarter un devis et en choisir un.
func TestRecordDevisAllowedAfterSingleRefusal(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	demande := f.demande(t)
	ecarte := f.devisRecu(t, demande.ID, "Charpentes du Val", 1_250_000)

	if _, err := f.service.Reject(t.Context(), ecarte.ID, acteur); err != nil {
		t.Fatalf("Reject() échoué : %v", err)
	}

	if _, err := f.service.RecordDevis(t.Context(), devis.DevisInput{
		DemandeID:  demande.ID,
		Artisan:    devis.Artisan{Entreprise: "Toiture Ain"},
		Montant:    1_180_050,
		ReceivedAt: instantReponse,
		By:         acteur,
	}); err != nil {
		t.Errorf("RecordDevis() après un refus isolé = %v, la demande reste ouverte", err)
	}
}

// TestRetainRejectsSiblings est l'invariant central du domaine : retenir, c'est
// clore la comparaison. Les concurrents encore reçus sont refusés dans la même
// décision, et la demande n'en porte qu'un seul retenu.
func TestRetainRejectsSiblings(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	demande := f.demande(t)
	f.devisRecu(t, demande.ID, "Charpentes du Val", 1_250_000)
	choisi := f.devisRecu(t, demande.ID, "Toiture Ain", 1_180_050)
	f.devisRecu(t, demande.ID, "Bois & Cie", 1_400_000)

	// Une demande voisine ne doit pas bouger : le ricochet s'arrête à la
	// consultation tranchée.
	autre := f.demande(t)
	voisin := f.devisRecu(t, autre.ID, "Charpentes du Val", 900_000)

	decision := instantSaisie.Add(48 * time.Hour)
	f.now = decision

	retenu, err := f.service.Retain(t.Context(), choisi.ID, acteur)
	if err != nil {
		t.Fatalf("Retain() échoué : %v", err)
	}

	if retenu.Statut != devis.StatutRetenu {
		t.Errorf("Statut du devis retenu = %q", retenu.Statut)
	}
	if retenu.DecidedBy != acteur || !retenu.DecidedAt.Equal(decision) {
		t.Errorf("décision = (%q, %s), attendu (%q, %s)", retenu.DecidedBy, retenu.DecidedAt, acteur, decision)
	}

	statuts := f.statuts(t, demande.ID)
	want := map[string]devis.Statut{
		"Charpentes du Val": devis.StatutRefuse,
		"Toiture Ain":       devis.StatutRetenu,
		"Bois & Cie":        devis.StatutRefuse,
	}
	for entreprise, expected := range want {
		if statuts[entreprise] != expected {
			t.Errorf("statut de %s = %q, attendu %q", entreprise, statuts[entreprise], expected)
		}
	}

	if statuts := f.statuts(t, autre.ID); statuts[voisin.Artisan.Entreprise] != devis.StatutRecu {
		t.Errorf("le devis d'une autre demande a été touché : %q", statuts[voisin.Artisan.Entreprise])
	}
}

// TestRetainTwiceRefused : la seconde décision n'a nulle part où aller, que ce
// soit sur le devis déjà retenu ou sur un concurrent devenu refusé.
func TestRetainTwiceRefused(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	demande := f.demande(t)
	premier := f.devisRecu(t, demande.ID, "Charpentes du Val", 1_250_000)
	second := f.devisRecu(t, demande.ID, "Toiture Ain", 1_180_050)

	if _, err := f.service.Retain(t.Context(), premier.ID, acteur); err != nil {
		t.Fatalf("Retain() échoué : %v", err)
	}

	for name, id := range map[string]devis.ID{"le devis retenu": premier.ID, "un concurrent refusé": second.ID} {
		if _, err := f.service.Retain(t.Context(), id, acteur); !errors.Is(err, devis.ErrForbiddenTransition) {
			t.Errorf("Retain() sur %s = %v, attendu %v", name, err, devis.ErrForbiddenTransition)
		}
	}
}

// TestRetainRefusedOnClosedDemande couvre l'écriture concurrente : un devis
// encore « recu » sur une demande déjà tranchée. Aucun enchaînement de cas
// d'usage ne produit cet état — seule une insertion arrivée entre-temps le
// peut — et c'est justement pourquoi il est vérifié ici.
func TestRetainRefusedOnClosedDemande(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	demande := f.demande(t)
	premier := f.devisRecu(t, demande.ID, "Charpentes du Val", 1_250_000)

	if _, err := f.service.Retain(t.Context(), premier.ID, acteur); err != nil {
		t.Fatalf("Retain() échoué : %v", err)
	}

	tardif := devis.Devis{
		ID:         "devis-tardif",
		DemandeID:  demande.ID,
		Artisan:    devis.Artisan{Entreprise: "Toiture Ain"},
		Montant:    900_000,
		ReceivedAt: instantReponse,
		Statut:     devis.StatutRecu,
	}
	f.repo.insert(tardif)

	if _, err := f.service.Retain(t.Context(), tardif.ID, acteur); !errors.Is(err, devis.ErrDemandeClosed) {
		t.Errorf("Retain() = %v, attendu %v", err, devis.ErrDemandeClosed)
	}
	if _, err := f.service.Reject(t.Context(), tardif.ID, acteur); !errors.Is(err, devis.ErrDemandeClosed) {
		t.Errorf("Reject() = %v, attendu %v", err, devis.ErrDemandeClosed)
	}
}

// TestRejectLeavesSiblingsAlone : écarter une offre n'en choisit aucune. Les
// autres restent en jeu, et la demande reste ouverte.
func TestRejectLeavesSiblingsAlone(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	demande := f.demande(t)
	ecarte := f.devisRecu(t, demande.ID, "Charpentes du Val", 1_250_000)
	f.devisRecu(t, demande.ID, "Toiture Ain", 1_180_050)

	refuse, err := f.service.Reject(t.Context(), ecarte.ID, acteur)
	if err != nil {
		t.Fatalf("Reject() échoué : %v", err)
	}
	if refuse.Statut != devis.StatutRefuse {
		t.Errorf("Statut = %q, attendu %q", refuse.Statut, devis.StatutRefuse)
	}

	statuts := f.statuts(t, demande.ID)
	if statuts["Toiture Ain"] != devis.StatutRecu {
		t.Errorf("statut du concurrent = %q, un refus isolé ne le touche pas", statuts["Toiture Ain"])
	}

	comparaison, err := f.service.Compare(t.Context(), demande.ID)
	if err != nil {
		t.Fatalf("Compare() échoué : %v", err)
	}
	if comparaison.Closed() {
		t.Error("la demande est close après un simple refus")
	}
}

func TestDecisionRequiresActor(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	demande := f.demande(t)
	proposition := f.devisRecu(t, demande.ID, "Charpentes du Val", 1_250_000)

	if _, err := f.service.Retain(t.Context(), proposition.ID, ""); !errors.Is(err, devis.ErrMissingActor) {
		t.Errorf("Retain() sans acteur = %v, attendu %v", err, devis.ErrMissingActor)
	}
	if _, err := f.service.Reject(t.Context(), proposition.ID, ""); !errors.Is(err, devis.ErrMissingActor) {
		t.Errorf("Reject() sans acteur = %v, attendu %v", err, devis.ErrMissingActor)
	}
}

func TestDecisionOnUnknownDevis(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	if _, err := f.service.Retain(t.Context(), "devis-fantome", acteur); !errors.Is(err, devis.ErrUnknownDevis) {
		t.Errorf("Retain() = %v, attendu %v", err, devis.ErrUnknownDevis)
	}
	if _, err := f.service.Reject(t.Context(), "devis-fantome", acteur); !errors.Is(err, devis.ErrUnknownDevis) {
		t.Errorf("Reject() = %v, attendu %v", err, devis.ErrUnknownDevis)
	}
}

// TestDecisionPropagatesConcurrentRefusal : quand la base refuse l'écriture
// parce qu'un autre a tranché entre-temps, le service ne l'habille pas en
// succès. C'est le dernier rempart, celui que l'index unique partiel tient.
func TestDecisionPropagatesConcurrentRefusal(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	demande := f.demande(t)
	proposition := f.devisRecu(t, demande.ID, "Charpentes du Val", 1_250_000)

	f.repo.failOn("Retain", devis.ErrDevisAlreadyDecided)
	if _, err := f.service.Retain(t.Context(), proposition.ID, acteur); !errors.Is(err, devis.ErrDevisAlreadyDecided) {
		t.Errorf("Retain() = %v, attendu %v", err, devis.ErrDevisAlreadyDecided)
	}

	f.repo.failOn("Reject", devis.ErrDevisAlreadyDecided)
	if _, err := f.service.Reject(t.Context(), proposition.ID, acteur); !errors.Is(err, devis.ErrDevisAlreadyDecided) {
		t.Errorf("Reject() = %v, attendu %v", err, devis.ErrDevisAlreadyDecided)
	}
}

// TestCompareSortsByMontant : la comparaison se lit du moins-disant au
// plus-disant, quel que soit l'ordre d'arrivée des devis.
func TestCompareSortsByMontant(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	demande := f.demande(t)
	f.devisRecu(t, demande.ID, "Bois & Cie", 1_400_000)
	f.devisRecu(t, demande.ID, "Charpentes du Val", 1_250_000)
	f.devisRecu(t, demande.ID, "Toiture Ain", 1_180_050)

	comparaison, err := f.service.Compare(t.Context(), demande.ID)
	if err != nil {
		t.Fatalf("Compare() échoué : %v", err)
	}

	want := []string{"Toiture Ain", "Charpentes du Val", "Bois & Cie"}
	if got := entreprises(comparaison); !slices.Equal(got, want) {
		t.Errorf("ordre = %v, attendu %v", got, want)
	}

	moinsDisant, ok := comparaison.MoinsDisant()
	if !ok || moinsDisant.Artisan.Entreprise != "Toiture Ain" {
		t.Errorf("MoinsDisant() = %+v, %t", moinsDisant, ok)
	}
	if ecart := comparaison.Ecart(); ecart != 219_950 {
		t.Errorf("Ecart() = %d, attendu 219950", int64(ecart))
	}
	if _, decided := comparaison.Retenu(); decided {
		t.Error("Retenu() rend un devis alors qu'aucun n'est retenu")
	}
}

// TestCompareTieBreak : à montant égal, l'ordre reste stable d'une lecture à
// l'autre. Sans second critère, deux devis alignés changeraient de place au
// gré du dépôt.
func TestCompareTieBreak(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	demande := f.demande(t)

	f.repo.insert(devis.Devis{
		ID: "b", DemandeID: demande.ID, Statut: devis.StatutRecu, Montant: 1_000_000,
		Artisan: devis.Artisan{Entreprise: "Second"}, ReceivedAt: instantReponse.Add(time.Hour),
	})
	f.repo.insert(devis.Devis{
		ID: "a", DemandeID: demande.ID, Statut: devis.StatutRecu, Montant: 1_000_000,
		Artisan: devis.Artisan{Entreprise: "Premier"}, ReceivedAt: instantReponse,
	})
	f.repo.insert(devis.Devis{
		ID: "c", DemandeID: demande.ID, Statut: devis.StatutRecu, Montant: 1_000_000,
		Artisan: devis.Artisan{Entreprise: "Troisième"}, ReceivedAt: instantReponse.Add(time.Hour),
	})

	comparaison, err := f.service.Compare(t.Context(), demande.ID)
	if err != nil {
		t.Fatalf("Compare() échoué : %v", err)
	}

	want := []string{"Premier", "Second", "Troisième"}
	if got := entreprises(comparaison); !slices.Equal(got, want) {
		t.Errorf("ordre = %v, attendu %v (réception puis identifiant)", got, want)
	}
}

func TestCompareOnEmptyAndUnknownDemande(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	demande := f.demande(t)

	comparaison, err := f.service.Compare(t.Context(), demande.ID)
	if err != nil {
		t.Fatalf("Compare() échoué : %v", err)
	}
	if len(comparaison.Devis) != 0 {
		t.Errorf("une demande sans devis rend %d propositions", len(comparaison.Devis))
	}
	if _, ok := comparaison.MoinsDisant(); ok {
		t.Error("MoinsDisant() rend un devis alors qu'aucun n'est arrivé")
	}
	if ecart := comparaison.Ecart(); ecart != 0 {
		t.Errorf("Ecart() = %d sur une demande vide", int64(ecart))
	}

	// Un seul devis : il n'y a pas d'écart à annoncer.
	f.devisRecu(t, demande.ID, "Toiture Ain", 1_180_050)
	comparaison, err = f.service.Compare(t.Context(), demande.ID)
	if err != nil {
		t.Fatalf("Compare() échoué : %v", err)
	}
	if ecart := comparaison.Ecart(); ecart != 0 {
		t.Errorf("Ecart() = %d avec un seul devis", int64(ecart))
	}

	// Deux devis, en revanche, se comparent : c'est le cas courant, et la borne
	// exacte du « pas assez de devis pour un écart ».
	f.devisRecu(t, demande.ID, "Charpentes du Val", 1_250_000)
	comparaison, err = f.service.Compare(t.Context(), demande.ID)
	if err != nil {
		t.Fatalf("Compare() échoué : %v", err)
	}
	if ecart := comparaison.Ecart(); ecart != 69_950 {
		t.Errorf("Ecart() = %d avec deux devis, attendu 69950", int64(ecart))
	}

	if _, err := f.service.Compare(t.Context(), "demande-fantome"); !errors.Is(err, devis.ErrUnknownDemande) {
		t.Errorf("Compare() = %v, attendu %v", err, devis.ErrUnknownDemande)
	}
}

// TestComparaisonsGroupsByDemande : la vue d'ensemble n'entremêle pas les
// consultations, et n'oublie pas celles qui n'ont encore rien reçu.
func TestComparaisonsGroupsByDemande(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	charpente := f.demande(t)
	f.devisRecu(t, charpente.ID, "Charpentes du Val", 1_250_000)
	f.devisRecu(t, charpente.ID, "Toiture Ain", 1_180_050)

	electricite, err := f.service.CreateDemande(t.Context(), devis.DemandeInput{
		Lot: "Électricité", SentAt: instantEnvoi, By: acteur,
	})
	if err != nil {
		t.Fatalf("CreateDemande() échoué : %v", err)
	}

	comparaisons, err := f.service.Comparaisons(t.Context())
	if err != nil {
		t.Fatalf("Comparaisons() échoué : %v", err)
	}

	if len(comparaisons) != 2 {
		t.Fatalf("Comparaisons() = %d entrées, attendu 2", len(comparaisons))
	}
	if comparaisons[0].Demande.ID != charpente.ID || len(comparaisons[0].Devis) != 2 {
		t.Errorf("première comparaison = %s avec %d devis", comparaisons[0].Demande.Lot, len(comparaisons[0].Devis))
	}
	if comparaisons[1].Demande.ID != electricite.ID || len(comparaisons[1].Devis) != 0 {
		t.Errorf("seconde comparaison = %s avec %d devis", comparaisons[1].Demande.Lot, len(comparaisons[1].Devis))
	}
	if got := entreprises(comparaisons[0]); !slices.Equal(got, []string{"Toiture Ain", "Charpentes du Val"}) {
		t.Errorf("la vue d'ensemble ne trie pas par montant : %v", got)
	}
}

func TestReadsPropagateRepoFailure(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		method string
		call   func(f *fixture, t *testing.T) error
	}{
		"liste des demandes": {
			method: "ListDemandes",
			call: func(f *fixture, t *testing.T) error {
				_, err := f.service.Demandes(t.Context())
				return err
			},
		},
		"lecture d'une demande": {
			method: "DemandeByID",
			call: func(f *fixture, t *testing.T) error {
				_, err := f.service.Demande(t.Context(), "peu importe")
				return err
			},
		},
		"lecture d'un devis": {
			method: "DevisByID",
			call: func(f *fixture, t *testing.T) error {
				_, err := f.service.Devis(t.Context(), "peu importe")
				return err
			},
		},
		"liste de tous les devis": {
			method: "ListDevis",
			call: func(f *fixture, t *testing.T) error {
				_, err := f.service.AllDevis(t.Context())
				return err
			},
		},
		"devis d'une demande": {
			method: "ListDevisByDemande",
			call: func(f *fixture, t *testing.T) error {
				_, err := f.service.Compare(t.Context(), f.demande(t).ID)
				return err
			},
		},
		"vue d'ensemble": {
			method: "ListDevis",
			call: func(f *fixture, t *testing.T) error {
				_, err := f.service.Comparaisons(t.Context())
				return err
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t)
			// La panne est armée après la préparation des données, sans quoi le
			// harnais échouerait avant d'atteindre le cas testé.
			demande := f.demande(t)
			f.repo.failOn(tc.method, errDepot)

			if err := tc.call(f, t); !errors.Is(err, errDepot) {
				t.Errorf("%s (demande %s) = %v, attendu la panne du dépôt", name, demande.ID, err)
			}
		})
	}
}
