package postgres_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Romain-Badino/Avanti/internal/adapters/postgres"
	"github.com/Romain-Badino/Avanti/internal/document"
)

// depotTest est le repère temporel des tests de documents.
var depotTest = time.Date(2026, time.April, 3, 11, 0, 0, 0, time.UTC)

// newDocumentRepo monte une base neuve et rend le dépôt des pièces avec le
// pool qui le porte : quelques vérifications visent les contraintes de table
// plutôt que le dépôt, et n'ont pas d'autre chemin que le SQL direct.
func newDocumentRepo(t *testing.T) (*postgres.DocumentRepo, *pgxpool.Pool) {
	t.Helper()

	pool := openPool(t, freshDatabase(t))
	applyMigrations(t, pool)

	repo, err := postgres.NewDocumentRepo(pool)
	if err != nil {
		t.Fatalf("postgres.NewDocumentRepo() échoué : %v", err)
	}

	return repo, pool
}

func TestNewDocumentRepoRejectsMissingPool(t *testing.T) {
	t.Parallel()

	if _, err := postgres.NewDocumentRepo(nil); err == nil {
		t.Error("NewDocumentRepo(nil) doit échouer")
	}
}

// testDocument fabrique une pièce valide et complète, prête à être insérée.
// Les champs qu'un test veut particuliers, il les écrase après coup.
func testDocument(t *testing.T, fileName string) document.Document {
	t.Helper()

	id, err := document.NewID()
	if err != nil {
		t.Fatalf("document.NewID() échoué : %v", err)
	}
	auteur, err := document.NewID()
	if err != nil {
		t.Fatalf("document.NewID() échoué : %v", err)
	}

	return document.Document{
		ID:          id,
		FileName:    fileName,
		MimeType:    "application/pdf",
		SizeBytes:   2048,
		Category:    document.CategoryDevisSigne,
		Description: "Devis signé le 12 mars.",
		UploadedBy:  document.ActeurID(auteur.String()),
		CreatedAt:   depotTest,
		UpdatedAt:   depotTest,
	}
}

// TestDocumentRoundTrip : ce qui est écrit se relit à l'identique, cible
// comprise. C'est le seul moyen de vérifier qu'aucune valeur ne se perd dans
// la traduction vers le SQL.
func TestDocumentRoundTrip(t *testing.T) {
	t.Parallel()

	repo, _ := newDocumentRepo(t)

	doc := testDocument(t, "devis-charpente.pdf")
	doc.Target = document.Target{Type: document.TargetDevis, ID: "6bbd562d-51ab-4bee-ab5b-1b9ec2a08ec5"}

	if err := repo.Create(t.Context(), doc); err != nil {
		t.Fatalf("Create() échoué : %v", err)
	}

	got, err := repo.ByID(t.Context(), doc.ID)
	if err != nil {
		t.Fatalf("ByID() échoué : %v", err)
	}
	if got.FileName != doc.FileName || got.MimeType != doc.MimeType || got.SizeBytes != doc.SizeBytes ||
		got.Category != doc.Category || got.Description != doc.Description || got.Target != doc.Target ||
		got.UploadedBy != doc.UploadedBy {
		t.Errorf("ByID() = %+v, attendu %+v", got, doc)
	}
	if !got.CreatedAt.Equal(doc.CreatedAt) || !got.UpdatedAt.Equal(doc.UpdatedAt) {
		t.Errorf("horodatages = %s / %s", got.CreatedAt, got.UpdatedAt)
	}
}

// TestDocumentByIDUnknown : la lecture vide rend l'erreur du domaine — pour un
// identifiant bien formé qui n'existe pas comme pour un identifiant illisible,
// qui ne désigne rien lui non plus.
func TestDocumentByIDUnknown(t *testing.T) {
	t.Parallel()

	repo, _ := newDocumentRepo(t)

	for name, id := range map[string]document.ID{
		"identifiant inconnu":   "6bbd562d-51ab-4bee-ab5b-1b9ec2a08ec5",
		"identifiant illisible": "pas-un-uuid",
	} {
		if _, err := repo.ByID(t.Context(), id); !errors.Is(err, document.ErrUnknownDocument) {
			t.Errorf("ByID(%s) = %v, attendu ErrUnknownDocument", name, err)
		}
	}
}

// TestDocumentListNewestFirst : le listing rend les pièces de la plus récente
// à la plus ancienne, comme le port le promet.
func TestDocumentListNewestFirst(t *testing.T) {
	t.Parallel()

	repo, _ := newDocumentRepo(t)

	ancienne := testDocument(t, "ancienne.pdf")
	ancienne.CreatedAt = depotTest.Add(-24 * time.Hour)
	ancienne.UpdatedAt = ancienne.CreatedAt
	recente := testDocument(t, "recente.pdf")

	for _, doc := range []document.Document{ancienne, recente} {
		if err := repo.Create(t.Context(), doc); err != nil {
			t.Fatalf("Create(%s) échoué : %v", doc.FileName, err)
		}
	}

	documents, err := repo.List(t.Context())
	if err != nil {
		t.Fatalf("List() échoué : %v", err)
	}
	if len(documents) != 2 || documents[0].FileName != "recente.pdf" || documents[1].FileName != "ancienne.pdf" {
		t.Errorf("List() = %+v, attendu la plus récente d'abord", documents)
	}
}

// TestDocumentListByTarget : le filtre par cible ne rend que les pièces
// rattachées, et une cible sans pièce rend une liste vide.
func TestDocumentListByTarget(t *testing.T) {
	t.Parallel()

	repo, _ := newDocumentRepo(t)
	cible := document.Target{Type: document.TargetDevis, ID: "6bbd562d-51ab-4bee-ab5b-1b9ec2a08ec5"}

	attachee := testDocument(t, "attachee.pdf")
	attachee.Target = cible
	libre := testDocument(t, "libre.pdf")

	for _, doc := range []document.Document{attachee, libre} {
		if err := repo.Create(t.Context(), doc); err != nil {
			t.Fatalf("Create(%s) échoué : %v", doc.FileName, err)
		}
	}

	documents, err := repo.ListByTarget(t.Context(), cible)
	if err != nil {
		t.Fatalf("ListByTarget() échoué : %v", err)
	}
	if len(documents) != 1 || documents[0].FileName != "attachee.pdf" {
		t.Errorf("ListByTarget() = %+v", documents)
	}

	vide, err := repo.ListByTarget(t.Context(), document.Target{Type: document.TargetEtape, ID: "rien"})
	if err != nil || len(vide) != 0 {
		t.Errorf("ListByTarget(sans pièce) = %+v, %v", vide, err)
	}
}

// TestDocumentListByTargets : la lecture groupée rend les pièces de plusieurs
// cibles en une requête, indexées par identifiant de cible, sans mélanger les
// cibles ni ramener celles d'un autre type. Une cible sans pièce n'a pas
// d'entrée dans la carte.
func TestDocumentListByTargets(t *testing.T) {
	t.Parallel()

	repo, _ := newDocumentRepo(t)
	const (
		premiere = "6bbd562d-51ab-4bee-ab5b-1b9ec2a08ec5"
		seconde  = "0b1cba0a-8ba3-4fd8-8f8f-2c1e1d0f8a91"
	)

	facture := testDocument(t, "facture.pdf")
	facture.Target = document.Target{Type: document.TargetFacture, ID: premiere}
	avoir := testDocument(t, "avoir.pdf")
	avoir.Target = document.Target{Type: document.TargetFacture, ID: seconde}
	// Même identifiant de cible, autre type : le filtre sur cible_type doit
	// l'écarter.
	homonyme := testDocument(t, "homonyme.pdf")
	homonyme.Target = document.Target{Type: document.TargetDevis, ID: premiere}

	for _, doc := range []document.Document{facture, avoir, homonyme} {
		if err := repo.Create(t.Context(), doc); err != nil {
			t.Fatalf("Create(%s) échoué : %v", doc.FileName, err)
		}
	}

	grouped, err := repo.ListByTargets(t.Context(), document.TargetFacture,
		[]string{premiere, seconde, "sans-piece"})
	if err != nil {
		t.Fatalf("ListByTargets() échoué : %v", err)
	}

	if len(grouped) != 2 {
		t.Fatalf("ListByTargets() = %+v, attendu deux cibles", grouped)
	}
	if pieces := grouped[premiere]; len(pieces) != 1 || pieces[0].FileName != "facture.pdf" {
		t.Errorf("pièces de la première cible = %+v", pieces)
	}
	if pieces := grouped[seconde]; len(pieces) != 1 || pieces[0].FileName != "avoir.pdf" {
		t.Errorf("pièces de la seconde cible = %+v", pieces)
	}

	vide, err := repo.ListByTargets(t.Context(), document.TargetEtape, []string{"rien"})
	if err != nil || len(vide) != 0 {
		t.Errorf("ListByTargets(sans pièce) = %+v, %v", vide, err)
	}
}

// TestDocumentTableConstraints : les contraintes CHECK doublent le domaine, et
// une écriture qui les contourne est refusée par la base elle-même.
func TestDocumentTableConstraints(t *testing.T) {
	t.Parallel()

	repo, _ := newDocumentRepo(t)

	cases := map[string]func(*document.Document){
		"type de contenu inventé": func(doc *document.Document) { doc.MimeType = "text/html" },
		"catégorie inventée":      func(doc *document.Document) { doc.Category = "selfie" },
		"taille nulle":            func(doc *document.Document) { doc.SizeBytes = 0 },
		"taille au-delà de la borne": func(doc *document.Document) {
			doc.SizeBytes = 26214401
		},
		"nom de fichier vide": func(doc *document.Document) { doc.FileName = "   " },
		"cible sans identifiant": func(doc *document.Document) {
			doc.Target = document.Target{Type: document.TargetDevis}
		},
		"identifiant sans type de cible": func(doc *document.Document) {
			doc.Target = document.Target{ID: "abc"}
		},
		"type de cible inventé": func(doc *document.Document) {
			doc.Target = document.Target{Type: "chantier", ID: "abc"}
		},
		"identifiant de cible hors de borne": func(doc *document.Document) {
			doc.Target = document.Target{Type: document.TargetDevis, ID: strings.Repeat("a", 256)}
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			doc := testDocument(t, "contournement.pdf")
			mutate(&doc)

			if err := repo.Create(t.Context(), doc); err == nil {
				t.Error("Create() a accepté une ligne que la table doit refuser")
			}
		})
	}
}
