package web

import (
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Romain-Badino/Avanti/internal/identity"
)

func TestNewCatalogLoadsFrench(t *testing.T) {
	t.Parallel()

	catalog, err := NewCatalog()
	if err != nil {
		t.Fatalf("NewCatalog() a échoué : %v", err)
	}

	tr := catalog.Translator("fr")
	if got := tr.T("app.name"); got != "Avanti" {
		t.Errorf("T(\"app.name\") = %q", got)
	}
	if got := tr.Lang(); got != "fr" {
		t.Errorf("Lang() = %q, attendu \"fr\"", got)
	}
}

func TestTranslatorSubstitutions(t *testing.T) {
	t.Parallel()

	catalog, err := NewCatalog()
	if err != nil {
		t.Fatalf("NewCatalog() a échoué : %v", err)
	}
	tr := catalog.Translator("fr")

	got := tr.T("footer.version", "Version", "v1.2.3")
	if !strings.Contains(got, "v1.2.3") {
		t.Errorf("T() = %q, la substitution n'a pas eu lieu", got)
	}
}

// TestTranslatorMarksFailures : une traduction manquante doit sauter aux yeux
// plutôt que laisser un trou dans la page.
func TestTranslatorMarksFailures(t *testing.T) {
	t.Parallel()

	catalog, err := NewCatalog()
	if err != nil {
		t.Fatalf("NewCatalog() a échoué : %v", err)
	}
	tr := catalog.Translator("fr")

	cases := []struct {
		name  string
		id    string
		pairs []string
	}{
		{name: "identifiant inconnu", id: "message.qui.n.existe.pas"},
		{name: "substitutions dépareillées", id: "footer.version", pairs: []string{"Version"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := tr.T(tc.id, tc.pairs...); got != "!"+tc.id+"!" {
				t.Errorf("T() = %q, attendu le marqueur !%s!", got, tc.id)
			}
		})
	}
}

// TestTranslatorFallsBackToFrench : la seule langue fournie est le français, une
// demande en anglais ne doit pas produire une page vide.
func TestTranslatorFallsBackToFrench(t *testing.T) {
	t.Parallel()

	catalog, err := NewCatalog()
	if err != nil {
		t.Fatalf("NewCatalog() a échoué : %v", err)
	}

	cases := []struct {
		name      string
		preferred []string
		wantLang  string
	}{
		{name: "aucune préférence", preferred: nil, wantLang: "fr"},
		{name: "en-tête vide", preferred: []string{""}, wantLang: "fr"},
		{name: "langue inconnue", preferred: []string{"xx-YY"}, wantLang: "fr"},
		{name: "anglais", preferred: []string{"en-US"}, wantLang: "en-US"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tr := catalog.Translator(tc.preferred...)
			if got := tr.Lang(); got != tc.wantLang {
				t.Errorf("Lang() = %q, attendu %q", got, tc.wantLang)
			}
			if got := tr.T("app.tagline"); got != "Pilotage de la reconstruction" {
				t.Errorf("T() = %q, le repli sur le français n'a pas eu lieu", got)
			}
		})
	}
}

// messageIDPattern relève les appels {{ .T "id" }} et {{ $.T "id" }} des
// gabarits.
var messageIDPattern = regexp.MustCompile(`\$?\.T\s+"([^"]+)"`)

// TestEveryTemplateMessageExists relie les gabarits au catalogue. C'est le test
// qui rend tenable la règle « toute chaîne affichée passe par le catalogue » :
// ajouter un {{ .T "…" }} sans le message correspondant fait échouer la suite,
// au lieu d'attendre qu'un utilisateur tombe sur un marqueur.
func TestEveryTemplateMessageExists(t *testing.T) {
	t.Parallel()

	catalog, err := NewCatalog()
	if err != nil {
		t.Fatalf("NewCatalog() a échoué : %v", err)
	}
	tr := catalog.Translator("fr")

	used := make(map[string]string)

	err = fs.WalkDir(templatesFS, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}

		content, err := fs.ReadFile(templatesFS, path)
		if err != nil {
			return err
		}

		for _, match := range messageIDPattern.FindAllStringSubmatch(string(content), -1) {
			used[match[1]] = path
		}

		return nil
	})
	if err != nil {
		t.Fatalf("parcours des gabarits : %v", err)
	}

	if len(used) == 0 {
		t.Fatal("aucun identifiant de message trouvé dans les gabarits : le motif de détection est cassé")
	}

	ids := make([]string, 0, len(used))
	for id := range used {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		if got := tr.T(id); got == "!"+id+"!" {
			t.Errorf("le gabarit %s utilise %q, absent du catalogue français", used[id], id)
		}
	}
}

// TestEveryScopeHasLabel exige un libellé traduit pour chaque scope du domaine.
//
// Il complète [TestEveryTemplateMessageExists], qui ne voit que les
// identifiants écrits en clair dans les gabarits. Les libellés de scopes sont
// calculés depuis le nom du scope : le parcours des gabarits ne les trouve pas,
// et sans ce test, ajouter un scope au domaine ferait apparaître un marqueur
// « !oauth.scope.…! » sur la page de consentement — c'est-à-dire à l'endroit
// précis où l'utilisateur doit comprendre ce qu'il accorde.
func TestEveryScopeHasLabel(t *testing.T) {
	t.Parallel()

	catalog, err := NewCatalog()
	if err != nil {
		t.Fatalf("NewCatalog() a échoué : %v", err)
	}
	tr := catalog.Translator("fr")

	for _, scope := range identity.AllScopes() {
		id := scopeMessageID(scope.String())
		if got := tr.T(id); got == "!"+id+"!" {
			t.Errorf("le scope %q n'a pas de libellé : %q est absent du catalogue français", scope, id)
		}
	}
}
