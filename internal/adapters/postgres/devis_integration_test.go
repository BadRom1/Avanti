package postgres_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Romain-Badino/Avanti/internal/adapters/postgres"
	"github.com/Romain-Badino/Avanti/internal/devis"
)

// Repères temporels des tests de devis.
var (
	envoiTest     = time.Date(2026, time.March, 2, 9, 0, 0, 0, time.UTC)
	receptionTest = time.Date(2026, time.March, 12, 14, 30, 0, 0, time.UTC)
	saisieTest    = time.Date(2026, time.March, 15, 10, 0, 0, 0, time.UTC)
	decisionTest  = time.Date(2026, time.March, 20, 8, 0, 0, 0, time.UTC)
)

// newDevisRepo monte une base neuve et rend le dépôt des devis avec le pool qui
// le porte : quelques vérifications visent les contraintes de table plutôt que
// le dépôt, et n'ont pas d'autre chemin que le SQL direct.
func newDevisRepo(t *testing.T) (*postgres.DevisRepo, *pgxpool.Pool) {
	t.Helper()

	pool := openPool(t, freshDatabase(t))
	applyMigrations(t, pool)

	repo, err := postgres.NewDevisRepo(pool)
	if err != nil {
		t.Fatalf("postgres.NewDevisRepo() échoué : %v", err)
	}

	return repo, pool
}

func TestNewDevisRepoRejectsMissingPool(t *testing.T) {
	t.Parallel()

	if _, err := postgres.NewDevisRepo(nil); err == nil {
		t.Error("NewDevisRepo(nil) doit échouer")
	}
}

// testActeur fabrique un identifiant d'acteur. La colonne ne porte pas de clé
// étrangère vers users — c'est une référence faible (R2) — donc un UUID
// quelconque suffit et le test n'a pas à créer de compte.
func testActeur(t *testing.T) devis.ActeurID {
	t.Helper()

	id, err := devis.NewID()
	if err != nil {
		t.Fatalf("devis.NewID() échoué : %v", err)
	}

	return devis.ActeurID(id.String())
}

func testDemande(t *testing.T, acteur devis.ActeurID, lot string) devis.DemandeDevis {
	t.Helper()

	id, err := devis.NewID()
	if err != nil {
		t.Fatalf("devis.NewID() échoué : %v", err)
	}

	return devis.DemandeDevis{
		ID:          id,
		Lot:         lot,
		Description: "Remplacement complet, 90 m².",
		Artisans: []devis.Artisan{
			{Entreprise: "Charpentes du Val", Email: "contact@val.fr", Telephone: "04 78 00 00 00"},
			{Entreprise: "Toiture Ain"},
		},
		SentAt:    envoiTest,
		CreatedBy: acteur,
		CreatedAt: saisieTest,
		UpdatedAt: saisieTest,
	}
}

func testDevis(t *testing.T, demandeID devis.ID, acteur devis.ActeurID, entreprise string, montant devis.Montant) devis.Devis {
	t.Helper()

	id, err := devis.NewID()
	if err != nil {
		t.Fatalf("devis.NewID() échoué : %v", err)
	}

	return devis.Devis{
		ID:         id,
		DemandeID:  demandeID,
		Artisan:    devis.Artisan{Entreprise: entreprise},
		Montant:    montant,
		ReceivedAt: receptionTest,
		Statut:     devis.StatutRecu,
		RecordedBy: acteur,
		CreatedAt:  saisieTest,
		UpdatedAt:  saisieTest,
	}
}

// TestDemandeRoundTrip : ce qui est écrit se relit à l'identique, artisans en
// JSONB compris. C'est le seul moyen de vérifier qu'aucune valeur ne se perd
// dans la traduction vers le SQL.
func TestDemandeRoundTrip(t *testing.T) {
	t.Parallel()

	repo, _ := newDevisRepo(t)
	acteur := testActeur(t)
	demande := testDemande(t, acteur, "Charpente")

	if err := repo.CreateDemande(t.Context(), demande); err != nil {
		t.Fatalf("CreateDemande() échoué : %v", err)
	}

	stored, err := repo.DemandeByID(t.Context(), demande.ID)
	if err != nil {
		t.Fatalf("DemandeByID() échoué : %v", err)
	}

	switch {
	case stored.ID != demande.ID:
		t.Errorf("ID = %q, attendu %q", stored.ID, demande.ID)
	case stored.Lot != demande.Lot:
		t.Errorf("Lot = %q, attendu %q", stored.Lot, demande.Lot)
	case stored.Description != demande.Description:
		t.Errorf("Description = %q", stored.Description)
	case stored.CreatedBy != acteur:
		t.Errorf("CreatedBy = %q, attendu %q", stored.CreatedBy, acteur)
	case !stored.SentAt.Equal(envoiTest):
		t.Errorf("SentAt = %s, attendu %s", stored.SentAt, envoiTest)
	case len(stored.Artisans) != 2:
		t.Fatalf("Artisans = %+v, attendu deux entrées", stored.Artisans)
	case stored.Artisans[0] != demande.Artisans[0]:
		t.Errorf("Artisans[0] = %+v, attendu %+v", stored.Artisans[0], demande.Artisans[0])
	case stored.Artisans[1] != demande.Artisans[1]:
		t.Errorf("Artisans[1] = %+v, attendu %+v", stored.Artisans[1], demande.Artisans[1])
	}
}

func TestDemandeUnknown(t *testing.T) {
	t.Parallel()

	repo, _ := newDevisRepo(t)

	unknown, err := devis.NewID()
	if err != nil {
		t.Fatalf("devis.NewID() échoué : %v", err)
	}

	if _, err := repo.DemandeByID(t.Context(), unknown); !errors.Is(err, devis.ErrUnknownDemande) {
		t.Errorf("DemandeByID() = %v, attendu %v", err, devis.ErrUnknownDemande)
	}
	if _, err := repo.DemandeByID(t.Context(), "pas-un-uuid"); err == nil {
		t.Error("DemandeByID() doit refuser un identifiant illisible")
	}
}

// TestDemandeSansArtisan : une consultation s'ouvre souvent avant qu'on ait
// arrêté qui consulter. La colonne JSONB doit alors contenir un tableau vide, ce
// que la contrainte de table exige, et la lecture rendre nil.
func TestDemandeSansArtisan(t *testing.T) {
	t.Parallel()

	repo, _ := newDevisRepo(t)
	demande := testDemande(t, testActeur(t), "Électricité")
	demande.Artisans = nil

	if err := repo.CreateDemande(t.Context(), demande); err != nil {
		t.Fatalf("CreateDemande() échoué : %v", err)
	}

	stored, err := repo.DemandeByID(t.Context(), demande.ID)
	if err != nil {
		t.Fatalf("DemandeByID() échoué : %v", err)
	}
	if len(stored.Artisans) != 0 {
		t.Errorf("Artisans = %+v, attendu aucune entrée", stored.Artisans)
	}
}

// TestListDemandesOrder : la plus récemment envoyée en tête. C'est l'ordre que
// la liste affiche, et le tri appartient à la requête plutôt qu'au Go — la base
// a l'index pour le faire.
func TestListDemandesOrder(t *testing.T) {
	t.Parallel()

	repo, _ := newDevisRepo(t)
	acteur := testActeur(t)

	ancienne := testDemande(t, acteur, "Terrassement")
	ancienne.SentAt = envoiTest.Add(-30 * 24 * time.Hour)
	recente := testDemande(t, acteur, "Charpente")

	for _, demande := range []devis.DemandeDevis{ancienne, recente} {
		if err := repo.CreateDemande(t.Context(), demande); err != nil {
			t.Fatalf("CreateDemande(%s) échoué : %v", demande.Lot, err)
		}
	}

	demandes, err := repo.ListDemandes(t.Context())
	if err != nil {
		t.Fatalf("ListDemandes() échoué : %v", err)
	}
	if len(demandes) != 2 {
		t.Fatalf("ListDemandes() = %d entrées, attendu 2", len(demandes))
	}
	if demandes[0].Lot != "Charpente" || demandes[1].Lot != "Terrassement" {
		t.Errorf("ordre = [%s %s], attendu la plus récente en tête", demandes[0].Lot, demandes[1].Lot)
	}
}

// TestDevisRoundTrip vérifie l'aller-retour d'un devis, y compris les deux
// conversions qui n'ont rien d'évident : le montant en centimes et la durée de
// validité en INTERVAL.
func TestDevisRoundTrip(t *testing.T) {
	t.Parallel()

	repo, _ := newDevisRepo(t)
	acteur := testActeur(t)
	demande := testDemande(t, acteur, "Charpente")

	if err := repo.CreateDemande(t.Context(), demande); err != nil {
		t.Fatalf("CreateDemande() échoué : %v", err)
	}

	proposition := testDevis(t, demande.ID, acteur, "Charpentes du Val", 1_180_050)
	proposition.Artisan = devis.Artisan{Entreprise: "Charpentes du Val", Email: "contact@val.fr", Telephone: "04 78 00 00 00"}
	// Une durée qui n'est pas un multiple de jours : c'est elle qui dirait qu'un
	// arrondi s'est glissé dans la traduction vers INTERVAL.
	proposition.Validity = 30*24*time.Hour + 90*time.Minute
	proposition.Notes = "Pose sous quinze jours."
	proposition.DocumentIDs = []string{"doc-1", "doc-2"}

	if err := repo.CreateDevis(t.Context(), proposition); err != nil {
		t.Fatalf("CreateDevis() échoué : %v", err)
	}

	stored, err := repo.DevisByID(t.Context(), proposition.ID)
	if err != nil {
		t.Fatalf("DevisByID() échoué : %v", err)
	}

	switch {
	case stored.Montant != 1_180_050:
		t.Errorf("Montant = %d, attendu 1180050", int64(stored.Montant))
	case stored.Validity != proposition.Validity:
		t.Errorf("Validity = %s, attendu %s", stored.Validity, proposition.Validity)
	case stored.Statut != devis.StatutRecu:
		t.Errorf("Statut = %q, attendu %q", stored.Statut, devis.StatutRecu)
	case stored.Artisan != proposition.Artisan:
		t.Errorf("Artisan = %+v, attendu %+v", stored.Artisan, proposition.Artisan)
	case stored.Notes != proposition.Notes:
		t.Errorf("Notes = %q", stored.Notes)
	case len(stored.DocumentIDs) != 2 || stored.DocumentIDs[0] != "doc-1":
		t.Errorf("DocumentIDs = %v", stored.DocumentIDs)
	case stored.RecordedBy != acteur:
		t.Errorf("RecordedBy = %q, attendu %q", stored.RecordedBy, acteur)
	case stored.DecidedBy != "" || !stored.DecidedAt.IsZero():
		t.Errorf("un devis reçu ne porte aucune décision : %q, %s", stored.DecidedBy, stored.DecidedAt)
	}
}

// TestDevisSansValidite : le cas courant. Zéro doit revenir zéro, et non une
// durée fantôme.
func TestDevisSansValidite(t *testing.T) {
	t.Parallel()

	repo, _ := newDevisRepo(t)
	acteur := testActeur(t)
	demande := testDemande(t, acteur, "Charpente")

	if err := repo.CreateDemande(t.Context(), demande); err != nil {
		t.Fatalf("CreateDemande() échoué : %v", err)
	}

	proposition := testDevis(t, demande.ID, acteur, "Toiture Ain", 1_250_000)
	if err := repo.CreateDevis(t.Context(), proposition); err != nil {
		t.Fatalf("CreateDevis() échoué : %v", err)
	}

	stored, err := repo.DevisByID(t.Context(), proposition.ID)
	if err != nil {
		t.Fatalf("DevisByID() échoué : %v", err)
	}
	if stored.Validity != 0 {
		t.Errorf("Validity = %s, attendu 0", stored.Validity)
	}
	if _, known := stored.ValidUntil(); known {
		t.Error("ValidUntil() annonce une échéance sur un devis sans durée de validité")
	}
}

// TestRetainIsAtomic est l'épreuve de l'invariant central : retenir un devis
// refuse ses concurrents, et les deux écritures tiennent ou tombent ensemble.
func TestRetainIsAtomic(t *testing.T) {
	t.Parallel()

	repo, _ := newDevisRepo(t)
	acteur := testActeur(t)
	demande := testDemande(t, acteur, "Charpente")

	if err := repo.CreateDemande(t.Context(), demande); err != nil {
		t.Fatalf("CreateDemande() échoué : %v", err)
	}

	cher := testDevis(t, demande.ID, acteur, "Bois & Cie", 1_400_000)
	choisi := testDevis(t, demande.ID, acteur, "Toiture Ain", 1_180_050)
	moyen := testDevis(t, demande.ID, acteur, "Charpentes du Val", 1_250_000)

	for _, proposition := range []devis.Devis{cher, choisi, moyen} {
		if err := repo.CreateDevis(t.Context(), proposition); err != nil {
			t.Fatalf("CreateDevis(%s) échoué : %v", proposition.Artisan.Entreprise, err)
		}
	}

	decideur := testActeur(t)
	if err := repo.Retain(t.Context(), choisi.ID, decideur, decisionTest); err != nil {
		t.Fatalf("Retain() échoué : %v", err)
	}

	propositions, err := repo.ListDevisByDemande(t.Context(), demande.ID)
	if err != nil {
		t.Fatalf("ListDevisByDemande() échoué : %v", err)
	}
	if len(propositions) != 3 {
		t.Fatalf("ListDevisByDemande() = %d devis, attendu 3", len(propositions))
	}

	// La requête trie par montant : le moins-disant vient en tête, et c'est lui
	// qui a été retenu.
	if propositions[0].ID != choisi.ID {
		t.Errorf("le tri par montant ne place pas le moins-disant en tête : %s", propositions[0].Artisan.Entreprise)
	}

	for _, proposition := range propositions {
		want := devis.StatutRefuse
		if proposition.ID == choisi.ID {
			want = devis.StatutRetenu
		}
		if proposition.Statut != want {
			t.Errorf("statut de %s = %q, attendu %q", proposition.Artisan.Entreprise, proposition.Statut, want)
		}
		if proposition.DecidedBy != decideur || !proposition.DecidedAt.Equal(decisionTest) {
			t.Errorf("décision de %s = (%q, %s)", proposition.Artisan.Entreprise, proposition.DecidedBy, proposition.DecidedAt)
		}
	}
}

// TestRetainStopsAtTheDemande : le ricochet ne franchit pas la frontière de la
// consultation. Un devis d'une autre demande n'a aucune raison d'être refusé.
func TestRetainStopsAtTheDemande(t *testing.T) {
	t.Parallel()

	repo, _ := newDevisRepo(t)
	acteur := testActeur(t)

	charpente := testDemande(t, acteur, "Charpente")
	electricite := testDemande(t, acteur, "Électricité")
	for _, demande := range []devis.DemandeDevis{charpente, electricite} {
		if err := repo.CreateDemande(t.Context(), demande); err != nil {
			t.Fatalf("CreateDemande(%s) échoué : %v", demande.Lot, err)
		}
	}

	choisi := testDevis(t, charpente.ID, acteur, "Toiture Ain", 1_180_050)
	voisin := testDevis(t, electricite.ID, acteur, "Élec du Bugey", 800_000)
	for _, proposition := range []devis.Devis{choisi, voisin} {
		if err := repo.CreateDevis(t.Context(), proposition); err != nil {
			t.Fatalf("CreateDevis() échoué : %v", err)
		}
	}

	if err := repo.Retain(t.Context(), choisi.ID, acteur, decisionTest); err != nil {
		t.Fatalf("Retain() échoué : %v", err)
	}

	stored, err := repo.DevisByID(t.Context(), voisin.ID)
	if err != nil {
		t.Fatalf("DevisByID() échoué : %v", err)
	}
	if stored.Statut != devis.StatutRecu {
		t.Errorf("le devis d'une autre demande est passé %q", stored.Statut)
	}
}

// TestCreateDevisRefusedWhenDemandeClosed : le dernier rempart. Un devis reçu
// n'entre plus sur une demande tranchée, et c'est le trigger qui le dit — pas
// une lecture faite avant l'écriture, qu'une rétention concurrente aurait pu
// démentir entre les deux.
func TestCreateDevisRefusedWhenDemandeClosed(t *testing.T) {
	t.Parallel()

	repo, _ := newDevisRepo(t)
	acteur := testActeur(t)
	demande := testDemande(t, acteur, "Charpente")

	if err := repo.CreateDemande(t.Context(), demande); err != nil {
		t.Fatalf("CreateDemande() échoué : %v", err)
	}

	premier := testDevis(t, demande.ID, acteur, "Toiture Ain", 1_180_050)
	if err := repo.CreateDevis(t.Context(), premier); err != nil {
		t.Fatalf("CreateDevis() échoué : %v", err)
	}
	if err := repo.Retain(t.Context(), premier.ID, acteur, decisionTest); err != nil {
		t.Fatalf("Retain() échoué : %v", err)
	}

	tardif := testDevis(t, demande.ID, acteur, "Charpentes du Val", 900_000)
	if err := repo.CreateDevis(t.Context(), tardif); !errors.Is(err, devis.ErrDemandeClosed) {
		t.Fatalf("CreateDevis() sur une demande close = %v, attendu %v", err, devis.ErrDemandeClosed)
	}

	// Le refus ne laisse rien derrière : la comparaison n'a que son devis retenu.
	propositions, err := repo.ListDevisByDemande(t.Context(), demande.ID)
	if err != nil {
		t.Fatalf("ListDevisByDemande() échoué : %v", err)
	}
	if len(propositions) != 1 || propositions[0].ID != premier.ID {
		t.Fatalf("ListDevisByDemande() = %d devis, attendu le seul devis retenu", len(propositions))
	}
}

// TestCreateDevisAllowedWhenDemandeOnlyRefused : le trigger ne ferme la demande
// que sur une *rétention*. Refuser une offre n'est pas en choisir une, et la
// consultation reste ouverte aux suivantes.
func TestCreateDevisAllowedWhenDemandeOnlyRefused(t *testing.T) {
	t.Parallel()

	repo, _ := newDevisRepo(t)
	acteur := testActeur(t)
	demande := testDemande(t, acteur, "Charpente")

	if err := repo.CreateDemande(t.Context(), demande); err != nil {
		t.Fatalf("CreateDemande() échoué : %v", err)
	}

	ecarte := testDevis(t, demande.ID, acteur, "Toiture Ain", 1_180_050)
	if err := repo.CreateDevis(t.Context(), ecarte); err != nil {
		t.Fatalf("CreateDevis() échoué : %v", err)
	}
	if err := repo.Reject(t.Context(), ecarte.ID, acteur, decisionTest); err != nil {
		t.Fatalf("Reject() échoué : %v", err)
	}

	suivant := testDevis(t, demande.ID, acteur, "Charpentes du Val", 900_000)
	if err := repo.CreateDevis(t.Context(), suivant); err != nil {
		t.Fatalf("CreateDevis() après un refus = %v, attendu aucune erreur", err)
	}
}

func TestDecisionOnDecidedOrUnknownDevis(t *testing.T) {
	t.Parallel()

	repo, _ := newDevisRepo(t)
	acteur := testActeur(t)
	demande := testDemande(t, acteur, "Charpente")

	if err := repo.CreateDemande(t.Context(), demande); err != nil {
		t.Fatalf("CreateDemande() échoué : %v", err)
	}

	proposition := testDevis(t, demande.ID, acteur, "Toiture Ain", 1_180_050)
	if err := repo.CreateDevis(t.Context(), proposition); err != nil {
		t.Fatalf("CreateDevis() échoué : %v", err)
	}
	if err := repo.Reject(t.Context(), proposition.ID, acteur, decisionTest); err != nil {
		t.Fatalf("Reject() échoué : %v", err)
	}

	if err := repo.Reject(t.Context(), proposition.ID, acteur, decisionTest); !errors.Is(err, devis.ErrDevisAlreadyDecided) {
		t.Errorf("Reject() sur un devis tranché = %v, attendu %v", err, devis.ErrDevisAlreadyDecided)
	}
	if err := repo.Retain(t.Context(), proposition.ID, acteur, decisionTest); !errors.Is(err, devis.ErrDevisAlreadyDecided) {
		t.Errorf("Retain() sur un devis tranché = %v, attendu %v", err, devis.ErrDevisAlreadyDecided)
	}

	unknown, err := devis.NewID()
	if err != nil {
		t.Fatalf("devis.NewID() échoué : %v", err)
	}
	if err := repo.Retain(t.Context(), unknown, acteur, decisionTest); !errors.Is(err, devis.ErrUnknownDevis) {
		t.Errorf("Retain() sur un devis inconnu = %v, attendu %v", err, devis.ErrUnknownDevis)
	}
	if err := repo.Reject(t.Context(), unknown, acteur, decisionTest); !errors.Is(err, devis.ErrUnknownDevis) {
		t.Errorf("Reject() sur un devis inconnu = %v, attendu %v", err, devis.ErrUnknownDevis)
	}
}

// TestDevisTableConstraints vérifie que les garde-fous de la table mordent, et
// non seulement ceux du domaine. Ce sont eux qui protègent la base d'une
// écriture directe en psql ou d'un futur chemin de code qui court-circuiterait
// le domaine.
func TestDevisTableConstraints(t *testing.T) {
	t.Parallel()

	repo, pool := newDevisRepo(t)
	acteur := testActeur(t)
	demande := testDemande(t, acteur, "Charpente")

	if err := repo.CreateDemande(t.Context(), demande); err != nil {
		t.Fatalf("CreateDemande() échoué : %v", err)
	}

	base := `INSERT INTO devis (id, demande_id, entreprise, montant, recu_le, statut, saisi_par, cree_le, modifie_le)
	         VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $7)`

	cases := map[string]struct {
		entreprise string
		montant    int64
		statut     string
	}{
		"montant nul":      {entreprise: "Toiture Ain", montant: 0, statut: "recu"},
		"montant négatif":  {entreprise: "Toiture Ain", montant: -1, statut: "recu"},
		"entreprise vide":  {entreprise: "   ", montant: 1_180_050, statut: "recu"},
		"statut inventé":   {entreprise: "Toiture Ain", montant: 1_180_050, statut: "brouillon"},
		"montant démesuré": {entreprise: "Toiture Ain", montant: 10_000_000_001, statut: "recu"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := pool.Exec(t.Context(), base,
				demande.ID.String(), tc.entreprise, tc.montant, receptionTest, tc.statut, acteur.String(), saisieTest)
			if err == nil {
				t.Errorf("l'insertion de %q a été acceptée, la contrainte ne mord pas", name)
			}
		})
	}
}

// TestDevisDecisionCoherence : un devis reçu ne porte pas de décision, un devis
// tranché en porte une. Sans cette contrainte, l'interface afficherait une
// décision qui n'a pas eu lieu.
func TestDevisDecisionCoherence(t *testing.T) {
	t.Parallel()

	repo, pool := newDevisRepo(t)
	acteur := testActeur(t)
	demande := testDemande(t, acteur, "Charpente")

	if err := repo.CreateDemande(t.Context(), demande); err != nil {
		t.Fatalf("CreateDemande() échoué : %v", err)
	}

	const query = `INSERT INTO devis (id, demande_id, entreprise, montant, recu_le, statut, saisi_par, decide_par, decide_le, cree_le, modifie_le)
	               VALUES (gen_random_uuid(), $1, 'Toiture Ain', 1180050, $2, $3, $4, $5, $6, $7, $7)`

	// Un devis reçu qui porterait un décideur.
	if _, err := pool.Exec(t.Context(), query,
		demande.ID.String(), receptionTest, "recu", acteur.String(), acteur.String(), decisionTest, saisieTest); err == nil {
		t.Error("un devis « recu » portant une décision a été accepté")
	}

	// Un devis retenu sans décideur.
	if _, err := pool.Exec(t.Context(), query,
		demande.ID.String(), receptionTest, "retenu", acteur.String(), nil, nil, saisieTest); err == nil {
		t.Error("un devis « retenu » sans décision a été accepté")
	}
}

// TestUnSeulRetenuParDemande vise l'index unique partiel directement : deux
// devis retenus sur la même demande sont refusés, deux refusés ne le sont pas.
func TestUnSeulRetenuParDemande(t *testing.T) {
	t.Parallel()

	repo, pool := newDevisRepo(t)
	acteur := testActeur(t)
	demande := testDemande(t, acteur, "Charpente")

	if err := repo.CreateDemande(t.Context(), demande); err != nil {
		t.Fatalf("CreateDemande() échoué : %v", err)
	}

	const query = `INSERT INTO devis (id, demande_id, entreprise, montant, recu_le, statut, saisi_par, decide_par, decide_le, cree_le, modifie_le)
	               VALUES (gen_random_uuid(), $1, $2, 1180050, $3, $4, $5, $5, $6, $7, $7)`

	for _, statut := range []string{"retenu", "refuse"} {
		if _, err := pool.Exec(t.Context(), query,
			demande.ID.String(), "Premier "+statut, receptionTest, statut, acteur.String(), decisionTest, saisieTest); err != nil {
			t.Fatalf("première insertion %q refusée : %v", statut, err)
		}
	}

	// Un second refusé passe : l'index ne contraint que les lignes retenues.
	if _, err := pool.Exec(t.Context(), query,
		demande.ID.String(), "Second refuse", receptionTest, "refuse", acteur.String(), decisionTest, saisieTest); err != nil {
		t.Errorf("un second devis refusé a été rejeté : %v", err)
	}

	// Un second retenu ne passe pas.
	if _, err := pool.Exec(t.Context(), query,
		demande.ID.String(), "Second retenu", receptionTest, "retenu", acteur.String(), decisionTest, saisieTest); err == nil {
		t.Error("un second devis retenu a été accepté sur la même demande")
	}
}

// TestListDevisSpansDemandes : la lecture d'ensemble rend bien tous les devis,
// c'est elle qui alimente la page de liste sans une requête par demande.
func TestListDevisSpansDemandes(t *testing.T) {
	t.Parallel()

	repo, _ := newDevisRepo(t)
	acteur := testActeur(t)

	charpente := testDemande(t, acteur, "Charpente")
	electricite := testDemande(t, acteur, "Électricité")
	for _, demande := range []devis.DemandeDevis{charpente, electricite} {
		if err := repo.CreateDemande(t.Context(), demande); err != nil {
			t.Fatalf("CreateDemande() échoué : %v", err)
		}
	}

	for _, proposition := range []devis.Devis{
		testDevis(t, charpente.ID, acteur, "Toiture Ain", 1_180_050),
		testDevis(t, charpente.ID, acteur, "Bois & Cie", 1_400_000),
		testDevis(t, electricite.ID, acteur, "Élec du Bugey", 800_000),
	} {
		if err := repo.CreateDevis(t.Context(), proposition); err != nil {
			t.Fatalf("CreateDevis() échoué : %v", err)
		}
	}

	propositions, err := repo.ListDevis(t.Context())
	if err != nil {
		t.Fatalf("ListDevis() échoué : %v", err)
	}
	if len(propositions) != 3 {
		t.Errorf("ListDevis() = %d devis, attendu 3", len(propositions))
	}

	if empty, err := repo.ListDevisByDemande(t.Context(), electricite.ID); err != nil || len(empty) != 1 {
		t.Errorf("ListDevisByDemande() = %d devis, %v", len(empty), err)
	}
}

// TestDevisCascadeOnDemande : supprimer une demande emporte ses devis. Les
// laisser derrière donnerait des propositions qui ne se comparent plus à rien.
func TestDevisCascadeOnDemande(t *testing.T) {
	t.Parallel()

	repo, pool := newDevisRepo(t)
	acteur := testActeur(t)
	demande := testDemande(t, acteur, "Charpente")

	if err := repo.CreateDemande(t.Context(), demande); err != nil {
		t.Fatalf("CreateDemande() échoué : %v", err)
	}

	proposition := testDevis(t, demande.ID, acteur, "Toiture Ain", 1_180_050)
	if err := repo.CreateDevis(t.Context(), proposition); err != nil {
		t.Fatalf("CreateDevis() échoué : %v", err)
	}

	if _, err := pool.Exec(t.Context(), `DELETE FROM demandes_devis WHERE id = $1`, demande.ID.String()); err != nil {
		t.Fatalf("suppression de la demande échouée : %v", err)
	}

	if _, err := repo.DevisByID(t.Context(), proposition.ID); !errors.Is(err, devis.ErrUnknownDevis) {
		t.Errorf("DevisByID() = %v, attendu %v", err, devis.ErrUnknownDevis)
	}
}

// concurrentRounds est le nombre de courses jouées par
// [TestConcurrentRetainAndCreateDevis]. Une seule ne prouverait rien : ce qu'on
// cherche est une fenêtre, et une fenêtre ne s'ouvre pas à tous les coups.
const concurrentRounds = 20

// insertionDelay laisse à l'insertion concurrente le temps de buter sur le
// verrou avant que la rétention ne soit validée.
const insertionDelay = 200 * time.Millisecond

// TestConcurrentRetainAndCreateDevis met en vraie concurrence la décision et
// l'arrivée d'un devis sur la même demande. C'est le scénario qu'une
// vérification en Go ne pouvait pas tenir : entre la lecture qui disait la
// demande ouverte et l'insertion qui suivait, la rétention passait, et un devis
// « recu » se posait sur une comparaison déjà tranchée.
//
// La post-condition ne présume pas de l'ordre — les deux sont légitimes — mais
// exige qu'il y en ait un : une fois les deux terminées, aucun devis n'est resté
// en attente. Ou l'insertion a précédé la décision, qui l'a refusée avec les
// autres concurrents, ou elle l'a suivie et s'est vu refuser l'entrée.
func TestConcurrentRetainAndCreateDevis(t *testing.T) {
	t.Parallel()

	repo, _ := newDevisRepo(t)
	acteur := testActeur(t)

	for round := range concurrentRounds {
		demande := testDemande(t, acteur, fmt.Sprintf("Charpente %d", round))
		if err := repo.CreateDemande(t.Context(), demande); err != nil {
			t.Fatalf("tour %d : CreateDemande() échoué : %v", round, err)
		}

		premier := testDevis(t, demande.ID, acteur, "Toiture Ain", 1_180_050)
		if err := repo.CreateDevis(t.Context(), premier); err != nil {
			t.Fatalf("tour %d : CreateDevis() échoué : %v", round, err)
		}

		tardif := testDevis(t, demande.ID, acteur, "Charpentes du Val", 900_000)
		retainErr, createErr := raceRetainAndCreate(t, repo, premier.ID, acteur, tardif)

		if retainErr != nil {
			t.Fatalf("tour %d : Retain() = %v, attendu aucune erreur", round, retainErr)
		}
		if createErr != nil && !errors.Is(createErr, devis.ErrDemandeClosed) {
			t.Fatalf("tour %d : CreateDevis() = %v, attendu nil ou %v", round, createErr, devis.ErrDemandeClosed)
		}

		assertDemandeTranchee(t, repo, demande.ID, round)
	}
}

// raceRetainAndCreate lance les deux écritures ensemble et rend leurs erreurs.
//
// Le départ est donné par la fermeture d'un canal plutôt que par deux `go`
// successifs : les goroutines sont déjà lancées et bloquées dessus, ce qui
// resserre la fenêtre au lieu de laisser la première prendre de l'avance.
func raceRetainAndCreate(
	t *testing.T, repo *postgres.DevisRepo, retenu devis.ID, acteur devis.ActeurID, tardif devis.Devis,
) (retainErr, createErr error) {
	t.Helper()

	depart := make(chan struct{})

	var course sync.WaitGroup
	course.Add(2)

	go func() {
		defer course.Done()
		<-depart
		retainErr = repo.Retain(t.Context(), retenu, acteur, decisionTest)
	}()
	go func() {
		defer course.Done()
		<-depart
		createErr = repo.CreateDevis(t.Context(), tardif)
	}()

	close(depart)
	course.Wait()

	return retainErr, createErr
}

// assertDemandeTranchee vérifie l'état que la course doit laisser : un devis
// retenu, et aucun devis encore en attente de décision.
func assertDemandeTranchee(t *testing.T, repo *postgres.DevisRepo, demandeID devis.ID, round int) {
	t.Helper()

	propositions, err := repo.ListDevisByDemande(t.Context(), demandeID)
	if err != nil {
		t.Fatalf("tour %d : ListDevisByDemande() échoué : %v", round, err)
	}

	retenus := 0
	for _, proposition := range propositions {
		switch proposition.Statut {
		case devis.StatutRecu:
			t.Fatalf("tour %d : le devis de %s est resté « %s » sur une demande tranchée",
				round, proposition.Artisan.Entreprise, proposition.Statut)
		case devis.StatutRetenu:
			retenus++
		case devis.StatutRefuse:
		}
	}

	if retenus != 1 {
		t.Fatalf("tour %d : %d devis retenus, attendu 1", round, retenus)
	}
}

// TestCreateDevisWaitsForConcurrentRetain fixe l'ordre que la course, elle, ne
// fixe pas : la rétention tient le verrou, l'insertion arrive derrière et
// n'est pas encore validée quand le trigger regarde.
//
// C'est le cas qui distingue un verrou d'une simple relecture. Sans lui, le
// trigger lirait « aucun devis retenu » — la rétention concurrente est
// invisible tant qu'elle n'est pas validée — et laisserait entrer le devis.
// Avec lui, il attend la fin de la rétention, puis la voit et refuse.
func TestCreateDevisWaitsForConcurrentRetain(t *testing.T) {
	t.Parallel()

	repo, pool := newDevisRepo(t)
	acteur := testActeur(t)
	demande := testDemande(t, acteur, "Charpente")

	if err := repo.CreateDemande(t.Context(), demande); err != nil {
		t.Fatalf("CreateDemande() échoué : %v", err)
	}

	premier := testDevis(t, demande.ID, acteur, "Toiture Ain", 1_180_050)
	if err := repo.CreateDevis(t.Context(), premier); err != nil {
		t.Fatalf("CreateDevis() échoué : %v", err)
	}

	// La transaction refait ce que fait Retain, en gardant la main sur le moment
	// de la validation : verrou sur la demande, puis retenue.
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("ouverture de la transaction de rétention : %v", err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(t.Context()); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			t.Errorf("annulation de la transaction de rétention : %v", rollbackErr)
		}
	}()

	if _, err := tx.Exec(t.Context(), `SELECT id FROM demandes_devis WHERE id = $1 FOR UPDATE`, demande.ID.String()); err != nil {
		t.Fatalf("verrouillage de la demande : %v", err)
	}

	tardif := testDevis(t, demande.ID, acteur, "Charpentes du Val", 900_000)
	insertion := make(chan error, 1)
	go func() { insertion <- repo.CreateDevis(t.Context(), tardif) }()

	// Le délai laisse l'insertion buter sur le verrou. Qu'elle y soit arrivée ou
	// non ne change pas le verdict attendu : dans un cas elle attend puis voit la
	// demande close, dans l'autre elle la lit close d'emblée.
	time.Sleep(insertionDelay)

	const retenirQuery = `
		UPDATE devis
		   SET statut = 'retenu', decide_par = $2, decide_le = $3, modifie_le = $3
		 WHERE id = $1`

	if _, err := tx.Exec(t.Context(), retenirQuery, premier.ID.String(), acteur.String(), decisionTest); err != nil {
		t.Fatalf("retenue du premier devis : %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("validation de la rétention : %v", err)
	}

	if err := <-insertion; !errors.Is(err, devis.ErrDemandeClosed) {
		t.Fatalf("CreateDevis() concurrent = %v, attendu %v", err, devis.ErrDemandeClosed)
	}
}
