package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Romain-Badino/Avanti/internal/document"
	"github.com/Romain-Badino/Avanti/internal/planning"
)

// TestEveryCategorieHasLabel exige un libellé traduit pour chaque catégorie du
// domaine.
//
// Même raison d'être que TestEveryScopeHasLabel : les libellés de catégories
// sont calculés depuis la valeur (« document.categorie.<valeur> »), le
// parcours des gabarits ne les trouve pas, et sans ce test, ajouter une
// catégorie au domaine ferait apparaître un marqueur dans le sélecteur du
// formulaire de dépôt.
func TestEveryCategorieHasLabel(t *testing.T) {
	t.Parallel()

	catalog, err := NewCatalog()
	if err != nil {
		t.Fatalf("NewCatalog() a échoué : %v", err)
	}
	tr := catalog.Translator("fr")

	for _, category := range document.AllCategories() {
		id := "document.categorie." + category.String()
		if got := tr.T(id); got == "!"+id+"!" {
			t.Errorf("la catégorie %q n'a pas de libellé : %q est absent du catalogue français", category, id)
		}
	}
}

// countingPlanningRepo compte les lectures d'étapes. C'est le seul point de
// mesure honnête de la mémoïsation des rattachements : la liste des pièces ne
// dit rien du nombre de lectures qu'elle a coûté.
type countingPlanningRepo struct {
	etapes map[planning.ID]planning.Etape
	reads  int
}

func (r *countingPlanningRepo) EtapeByID(_ context.Context, id planning.ID) (planning.Etape, error) {
	r.reads++

	etape, ok := r.etapes[id]
	if !ok {
		return planning.Etape{}, planning.ErrUnknownEtape
	}

	return etape, nil
}

// Le reste du port n'est pas sollicité par la construction des lignes.
func (r *countingPlanningRepo) CreateEtape(context.Context, planning.Etape) error { return nil }

func (r *countingPlanningRepo) ListEtapes(context.Context) ([]planning.Etape, error) {
	return nil, nil
}

func (r *countingPlanningRepo) UpdateEtape(context.Context, planning.Etape, time.Time) error {
	return nil
}

func (r *countingPlanningRepo) StartEtape(context.Context, planning.Etape, time.Time) error {
	return nil
}

func (r *countingPlanningRepo) CreateJalon(context.Context, planning.Jalon) error { return nil }

func (r *countingPlanningRepo) JalonByID(context.Context, planning.ID) (planning.Jalon, error) {
	return planning.Jalon{}, planning.ErrUnknownJalon
}

func (r *countingPlanningRepo) ListJalons(context.Context) ([]planning.Jalon, error) {
	return nil, nil
}

func (r *countingPlanningRepo) UpdateJalon(context.Context, planning.Jalon, time.Time) error {
	return nil
}

// TestNewDocumentRowsMemoizesTargets : la liste des pièces lit chaque cible UNE
// fois, quel que soit le nombre de pièces qui la portent — et une cible
// disparue reste affichée comme telle, sur chacune de ses pièces.
func TestNewDocumentRowsMemoizesTargets(t *testing.T) {
	t.Parallel()

	catalog, err := NewCatalog()
	if err != nil {
		t.Fatalf("NewCatalog() a échoué : %v", err)
	}

	repo := &countingPlanningRepo{etapes: map[planning.ID]planning.Etape{
		"etape-vivante": {ID: "etape-vivante", Name: "Charpente"},
	}}
	service, err := planning.NewService(planning.ServiceOptions{Repo: repo})
	if err != nil {
		t.Fatalf("planning.NewService() a échoué : %v", err)
	}

	h := &Handler{catalog: catalog, planning: service}

	etape := func(id string) document.Target {
		return document.Target{Type: document.TargetEtape, ID: id}
	}
	documents := []document.Document{
		{FileName: "plan.pdf", Target: etape("etape-vivante")},
		{FileName: "photo.png", Target: etape("etape-vivante")},
		{FileName: "note.pdf", Target: etape("etape-disparue")},
		{FileName: "annexe.pdf", Target: etape("etape-disparue")},
		{FileName: "libre.pdf"},
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, documentsPath, http.NoBody)

	rows, err := h.newDocumentRows(req, documents)
	if err != nil {
		t.Fatalf("newDocumentRows() a échoué : %v", err)
	}
	if len(rows) != len(documents) {
		t.Fatalf("lignes = %d, attendu %d", len(rows), len(documents))
	}

	// Deux cibles DISTINCTES, deux lectures — pas une par pièce rattachée.
	if repo.reads != 2 {
		t.Errorf("lectures d'étapes = %d, attendu 2 (une par cible distincte)", repo.reads)
	}

	wanted := []string{"Étape Charpente", "Étape Charpente", "Étape disparue", "Étape disparue", ""}
	for i, want := range wanted {
		if rows[i].Rattachement != want {
			t.Errorf("rattachement de %s = %q, attendu %q", documents[i].FileName, rows[i].Rattachement, want)
		}
	}
}

// TestTenthsOf verrouille l'arithmétique entière des tailles affichées : pas
// de flottant, et l'arrondi se fait vers le bas, au dixième.
func TestTenthsOf(t *testing.T) {
	t.Parallel()

	cases := []struct {
		size, unit int64
		want       string
	}{
		{size: 1 << 20, unit: 1 << 20, want: "1"},
		{size: 1<<20 + 1<<19, unit: 1 << 20, want: "1,5"},
		{size: 25 << 20, unit: 1 << 20, want: "25"},
		{size: 1536, unit: 1 << 10, want: "1,5"},
		{size: 1075, unit: 1 << 10, want: "1"},
	}
	for _, tc := range cases {
		if got := tenthsOf(tc.size, tc.unit); got != tc.want {
			t.Errorf("tenthsOf(%d, %d) = %q, attendu %q", tc.size, tc.unit, got, tc.want)
		}
	}
}
