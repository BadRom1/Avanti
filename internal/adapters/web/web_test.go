package web_test

import (
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Romain-Badino/Avanti/internal/adapters/web"
	"github.com/Romain-Badino/Avanti/internal/platform"
	"github.com/Romain-Badino/Avanti/internal/platform/logging"
)

func newHandler(t *testing.T) *web.Handler {
	t.Helper()

	handler, err := web.New(web.Options{
		Logger: logging.Discard(),
		Build:  platform.BuildInfo{Version: "v0.0.0-test"},
	})
	if err != nil {
		t.Fatalf("web.New() a échoué : %v", err)
	}

	return handler
}

// response est une réponse déjà lue et refermée.
type response struct {
	Status int
	Header http.Header
	Body   string
}

// get exerce le gestionnaire sans ouvrir de port.
func get(t *testing.T, handler http.Handler, target string) response {
	t.Helper()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, http.NoBody))

	result := rec.Result()
	defer func() {
		if err := result.Body.Close(); err != nil {
			t.Errorf("fermeture du corps de réponse : %v", err)
		}
	}()

	body, err := io.ReadAll(result.Body)
	if err != nil {
		t.Fatalf("lecture du corps de réponse : %v", err)
	}

	return response{Status: result.StatusCode, Header: result.Header, Body: string(body)}
}

func TestNewRefusesMissingLogger(t *testing.T) {
	t.Parallel()

	if _, err := web.New(web.Options{}); err == nil {
		t.Fatal("web.New() doit refuser un journal absent")
	}
}

func TestHomePage(t *testing.T) {
	t.Parallel()

	resp := get(t, newHandler(t), "/")

	if resp.Status != http.StatusOK {
		t.Fatalf("statut = %d, attendu 200", resp.Status)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Content-Type = %q", got)
	}

	for _, want := range []string{
		`lang="fr"`,
		"Avanti — tableau de bord à venir",
		"Pilotage de la reconstruction",
		"/static/css/avanti.css",
		"/static/vendor/htmx.min.js",
	} {
		if !strings.Contains(resp.Body, want) {
			t.Errorf("la page d'accueil ne contient pas %q", want)
		}
	}
}

// TestPagesHaveNoMissingTranslation est le filet de sécurité de la règle « toute
// chaîne affichée passe par le catalogue » : un identifiant absent se rend en
// marqueur !comme.ceci!, et ce test le refuse.
func TestPagesHaveNoMissingTranslation(t *testing.T) {
	t.Parallel()

	handler := newHandler(t)

	for _, target := range []string{"/", "/page-inexistante"} {
		resp := get(t, handler, target)
		if marker := findMarker(resp.Body); marker != "" {
			t.Errorf("%s affiche une traduction manquante : %s", target, marker)
		}
	}
}

// findMarker repère un marqueur de traduction manquante de la forme
// !section.message! dans une page rendue.
func findMarker(body string) string {
	for rest := body; ; {
		start := strings.Index(rest, "!")
		if start < 0 {
			return ""
		}
		rest = rest[start+1:]

		end := strings.Index(rest, "!")
		if end < 0 {
			return ""
		}

		candidate := rest[:end]
		if candidate != "" && strings.Contains(candidate, ".") && !strings.ContainsAny(candidate, " <>\n\t") {
			return "!" + candidate + "!"
		}
		rest = rest[end+1:]
	}
}

func TestUnknownPathRendersFrenchNotFound(t *testing.T) {
	t.Parallel()

	resp := get(t, newHandler(t), "/une/page/qui/n/existe/pas")

	if resp.Status != http.StatusNotFound {
		t.Fatalf("statut = %d, attendu 404", resp.Status)
	}
	// html/template échappe les apostrophes : la comparaison se fait sur le
	// texte tel que l'utilisateur le lit, pas tel qu'il est encodé.
	if !strings.Contains(html.UnescapeString(resp.Body), "Cette page n'existe pas") {
		t.Errorf("la page 404 ne porte pas le message français attendu : %s", resp.Body)
	}
}

// TestHomeIsExactPath : sans le motif {$}, la racine capterait toutes les URLs
// et la page 404 ne serait jamais servie.
func TestHomeIsExactPath(t *testing.T) {
	t.Parallel()

	resp := get(t, newHandler(t), "/quelque-chose")
	if resp.Status == http.StatusOK {
		t.Error("une URL inconnue ne doit pas rendre la page d'accueil")
	}
}

func TestStaticAssetsAreServed(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		path        string
		contentType string
		contains    string
	}{
		{name: "feuille de style", path: "/static/css/avanti.css", contentType: "text/css", contains: "--accent"},
		{name: "htmx vendoré", path: "/static/vendor/htmx.min.js", contentType: "javascript", contains: "htmx"},
	}

	handler := newHandler(t)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			resp := get(t, handler, tc.path)
			if resp.Status != http.StatusOK {
				t.Fatalf("statut = %d, attendu 200", resp.Status)
			}
			if got := resp.Header.Get("Content-Type"); !strings.Contains(got, tc.contentType) {
				t.Errorf("Content-Type = %q, attendu un type contenant %q", got, tc.contentType)
			}
			if !strings.Contains(resp.Body, tc.contains) {
				t.Errorf("le contenu servi ne contient pas %q", tc.contains)
			}
		})
	}
}

// TestVendoredHTMXIsPinned vérifie que la bibliothèque servie est bien celle
// décrite dans static/vendor/VERSION — un remplacement silencieux du fichier
// serait une modification du code exécuté chez l'utilisateur.
func TestVendoredHTMXIsPinned(t *testing.T) {
	t.Parallel()

	resp := get(t, newHandler(t), "/static/vendor/htmx.min.js")
	if !strings.Contains(resp.Body, `version:"2.0.10"`) {
		t.Error("htmx.min.js ne porte pas la version 2.0.10 annoncée dans static/vendor/VERSION")
	}
}

func TestSecurityHeaders(t *testing.T) {
	t.Parallel()

	resp := get(t, newHandler(t), "/")

	csp := resp.Header.Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("aucune politique de sécurité du contenu")
	}
	for _, want := range []string{"default-src 'self'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP = %q, doit contenir %q", csp, want)
		}
	}
	// Aucune ressource distante, aucun script en ligne : la moindre tolérance
	// ici viderait la politique de son intérêt.
	for _, forbidden := range []string{"unsafe-inline", "unsafe-eval", "http://", "https://"} {
		if strings.Contains(csp, forbidden) {
			t.Errorf("CSP = %q, ne doit pas contenir %q", csp, forbidden)
		}
	}

	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
}

// TestAssetURLsCarryBuildStamp : sans estampille, un navigateur garderait
// l'ancienne feuille de style après une mise à jour du binaire.
func TestAssetURLsCarryBuildStamp(t *testing.T) {
	t.Parallel()

	resp := get(t, newHandler(t), "/")
	if !strings.Contains(resp.Body, "avanti.css?v=v0.0.0-test") {
		t.Error("les URLs d'assets ne portent pas l'estampille de build")
	}
}

// TestUnavailableSectionsAreNotLinks : annoncer une section dans la navigation
// est acceptable, y envoyer l'utilisateur sur un 404 ne l'est pas.
func TestUnavailableSectionsAreNotLinks(t *testing.T) {
	t.Parallel()

	resp := get(t, newHandler(t), "/")

	if strings.Contains(resp.Body, `href="/devis"`) {
		t.Error("la navigation pointe vers /devis, qui n'existe pas encore")
	}
	if !strings.Contains(resp.Body, "Devis") {
		t.Error("la navigation doit tout de même annoncer la section Devis")
	}
}
