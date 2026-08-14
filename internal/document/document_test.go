package document_test

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Romain-Badino/Avanti/internal/document"
)

// uuidV4Pattern est la forme canonique attendue d'un identifiant : version 4,
// variante RFC 4122.
var uuidV4Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewIDShape(t *testing.T) {
	t.Parallel()

	seen := make(map[document.ID]struct{})
	for range 64 {
		id, err := document.NewID()
		if err != nil {
			t.Fatalf("NewID() échoué : %v", err)
		}
		if !uuidV4Pattern.MatchString(id.String()) {
			t.Fatalf("NewID() = %q, forme UUID v4 attendue", id)
		}
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("NewID() a rendu deux fois %q", id)
		}
		seen[id] = struct{}{}
	}
}

func TestNormalizeFileName(t *testing.T) {
	t.Parallel()

	accepts := map[string]struct{ raw, want string }{
		"nom simple":            {raw: "devis.pdf", want: "devis.pdf"},
		"blancs de bordure":     {raw: "  devis.pdf  ", want: "devis.pdf"},
		"chemin unix retiré":    {raw: "/tmp/../dossier/devis.pdf", want: "devis.pdf"},
		"chemin windows retiré": {raw: `C:\Dossier\devis.pdf`, want: "devis.pdf"},
		"contrôles retirés":     {raw: "de\r\nvis\x00.pdf", want: "devis.pdf"},
		// U+202E (renversement droite-à-gauche) et U+200B (largeur nulle) : les
		// caractères de format qui maquillent une extension. En séquences
		// d'échappement — invisibles en clair, c'est bien le problème.
		"formats Unicode retirés": {raw: "fdp.\u202Egpj\u200B.pdf", want: "fdp.gpj.pdf"},
		"accents conservés":       {raw: "reçu-électricité.pdf", want: "reçu-électricité.pdf"},
		"255 caractères exacts":   {raw: strings.Repeat("a", 251) + ".pdf", want: strings.Repeat("a", 251) + ".pdf"},
		"nom caché mais licite":   {raw: ".htaccess", want: ".htaccess"},
		"contrôle seul en chemin": {raw: "dossier/\tdevis.pdf", want: "devis.pdf"},
	}
	for name, tc := range accepts {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := document.NormalizeFileName(tc.raw)
			if err != nil {
				t.Fatalf("NormalizeFileName(%q) échoué : %v", tc.raw, err)
			}
			if got != tc.want {
				t.Errorf("NormalizeFileName(%q) = %q, attendu %q", tc.raw, got, tc.want)
			}
		})
	}

	rejects := map[string]struct {
		raw  string
		want error
	}{
		"vide":                      {raw: "", want: document.ErrEmptyFileName},
		"blancs seuls":              {raw: "   ", want: document.ErrEmptyFileName},
		"point seul":                {raw: ".", want: document.ErrEmptyFileName},
		"double point":              {raw: "..", want: document.ErrEmptyFileName},
		"chemin sans nom":           {raw: "dossier/", want: document.ErrEmptyFileName},
		"contrôles seuls":           {raw: "\r\n\t", want: document.ErrEmptyFileName},
		"au-delà de 255 caractères": {raw: strings.Repeat("a", 252) + ".pdf", want: document.ErrFileNameTooLong},
	}
	for name, tc := range rejects {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := document.NormalizeFileName(tc.raw); !errors.Is(err, tc.want) {
				t.Errorf("NormalizeFileName(%q) = %v, attendu %v", tc.raw, err, tc.want)
			}
		})
	}
}

func TestNormalizeCategory(t *testing.T) {
	t.Parallel()

	for _, category := range document.AllCategories() {
		got, err := document.NormalizeCategory("  " + strings.ToUpper(category.String()) + "  ")
		if err != nil || got != category {
			t.Errorf("NormalizeCategory(%q) = %q, %v", category, got, err)
		}
	}

	for _, raw := range []string{"", "selfie", "devis signé"} {
		if _, err := document.NormalizeCategory(raw); !errors.Is(err, document.ErrUnknownCategory) {
			t.Errorf("NormalizeCategory(%q) = %v, attendu ErrUnknownCategory", raw, err)
		}
	}
}

func TestCategoryKnown(t *testing.T) {
	t.Parallel()

	if document.Category("selfie").Known() {
		t.Error("Known() accepte une catégorie inventée")
	}
	if got := document.CategoryFacture.String(); got != "facture" {
		t.Errorf("String() = %q", got)
	}
}

// TestAllCategoriesIsACopy : la liste rendue se modifie sans toucher au
// domaine.
func TestAllCategoriesIsACopy(t *testing.T) {
	t.Parallel()

	first := document.AllCategories()
	first[0] = "corrompue"

	if second := document.AllCategories(); slices.Contains(second, "corrompue") {
		t.Error("AllCategories() rend la tranche interne, pas une copie")
	}
}

func TestAllowedMimeTypesIsACopy(t *testing.T) {
	t.Parallel()

	first := document.AllowedMimeTypes()
	first[0] = "text/html"

	if second := document.AllowedMimeTypes(); slices.Contains(second, "text/html") {
		t.Error("AllowedMimeTypes() rend la tranche interne, pas une copie")
	}
}

func TestNormalizeTarget(t *testing.T) {
	t.Parallel()

	zero, err := document.NormalizeTarget(document.Target{})
	if err != nil || !zero.Zero() {
		t.Errorf("NormalizeTarget(zéro) = %+v, %v", zero, err)
	}

	got, err := document.NormalizeTarget(document.Target{Type: " Devis ", ID: " abc "})
	if err != nil {
		t.Fatalf("NormalizeTarget() échoué : %v", err)
	}
	if got != (document.Target{Type: document.TargetDevis, ID: "abc"}) {
		t.Errorf("NormalizeTarget() = %+v", got)
	}

	for name, raw := range map[string]document.Target{
		"type sans identifiant":     {Type: "devis"},
		"identifiant sans type":     {ID: "abc"},
		"type inconnu":              {Type: "chantier", ID: "abc"},
		"blancs déguisés":           {Type: "devis", ID: "   "},
		"identifiant hors de borne": {Type: "devis", ID: strings.Repeat("a", 256)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := document.NormalizeTarget(raw); !errors.Is(err, document.ErrInvalidTarget) {
				t.Errorf("NormalizeTarget(%+v) = %v, attendu ErrInvalidTarget", raw, err)
			}
		})
	}
}

func TestTargetTypeKnown(t *testing.T) {
	t.Parallel()

	for _, targetType := range []document.TargetType{document.TargetDevis, document.TargetFacture, document.TargetEtape} {
		if !targetType.Known() {
			t.Errorf("Known(%q) = false", targetType)
		}
	}
	if document.TargetType("chantier").Known() {
		t.Error("Known() accepte un type inventé")
	}
	if got := document.TargetEtape.String(); got != "etape" {
		t.Errorf("String() = %q", got)
	}
}

func TestActeurIDString(t *testing.T) {
	t.Parallel()

	if got := acteur.String(); got != string(acteur) {
		t.Errorf("String() = %q", got)
	}
}
