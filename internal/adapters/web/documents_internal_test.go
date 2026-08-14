package web

import (
	"testing"

	"github.com/Romain-Badino/Avanti/internal/document"
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
