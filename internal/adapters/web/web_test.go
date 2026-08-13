package web_test

import (
	"html"
	"net/http"
	"strings"
	"testing"
)

func TestHomePage(t *testing.T) {
	t.Parallel()

	browser := newBrowser(t, newSite(t).handler)
	browser.login(ownerEmail)

	result := browser.get("/")
	if result.Status != http.StatusOK {
		t.Fatalf("statut = %d, attendu 200", result.Status)
	}
	if got := result.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Content-Type = %q, attendu text/html", got)
	}

	for _, want := range []string{
		"/static/css/avanti.css",
		"/static/vendor/htmx.min.js",
		"Tableau de bord",
	} {
		if !strings.Contains(result.Body, want) {
			t.Errorf("la page d'accueil ne contient pas %q", want)
		}
	}
}

// TestHTMLPagesAreNotCacheable : toutes dépendent de qui est connecté. Un
// cache intermédiaire, ou le simple bouton « précédent » sur un poste partagé,
// montrerait sinon le tableau de bord d'une personne à la suivante.
func TestHTMLPagesAreNotCacheable(t *testing.T) {
	t.Parallel()

	browser := newBrowser(t, newSite(t).handler)

	if form := browser.get("/connexion"); form.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("/connexion : Cache-Control = %q, attendu no-store", form.Header.Get("Cache-Control"))
	}

	browser.login(ownerEmail)

	if home := browser.get("/"); home.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("/ : Cache-Control = %q, attendu no-store", home.Header.Get("Cache-Control"))
	}
}

// TestPagesHaveNoMissingTranslation : le catalogue est la seule source du texte
// affiché. Un identifiant absent se rend en marqueur !comme.ceci!, et ce test le
// refuse — sur les pages publiques comme sur les pages connectées.
func TestPagesHaveNoMissingTranslation(t *testing.T) {
	t.Parallel()

	browser := newBrowser(t, newSite(t).handler)

	publicPaths := []string{"/connexion", "/connexion?deconnexion", "/connexion?suite=%2Fdevis"}
	for _, target := range publicPaths {
		if marker := findMarker(browser.get(target).Body); marker != "" {
			t.Errorf("%s affiche une traduction manquante : %s", target, marker)
		}
	}

	// Le formulaire en échec, pour couvrir les messages d'erreur du catalogue.
	failure := browser.post("/connexion", invalidForm())
	if marker := findMarker(failure.Body); marker != "" {
		t.Errorf("le formulaire en échec affiche une traduction manquante : %s", marker)
	}

	// Puis les pages qui demandent une session, pour les deux rôles : le
	// récapitulatif des accès n'affiche pas les mêmes lignes selon les scopes.
	for _, email := range []string{ownerEmail, collaboratorEmail} {
		loggedIn := newBrowser(t, newSite(t).handler)
		loggedIn.login(email)

		for _, target := range []string{"/", "/page-inexistante"} {
			if marker := findMarker(loggedIn.get(target).Body); marker != "" {
				t.Errorf("%s (rôle de %s) affiche une traduction manquante : %s", target, email, marker)
			}
		}
	}
}

func invalidForm() map[string][]string {
	return map[string][]string{"email": {"personne@exemple.fr"}, "mot_de_passe": {"mauvais mot de passe"}}
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

// TestUnknownPathRendersFrenchNotFound : une fois connecté, une URL inconnue donne bien un
// 404 en français, et non une redirection de plus.
func TestUnknownPathRendersFrenchNotFound(t *testing.T) {
	t.Parallel()

	browser := newBrowser(t, newSite(t).handler)
	browser.login(ownerEmail)

	result := browser.get("/une/page/qui/n/existe/pas")
	if result.Status != http.StatusNotFound {
		t.Fatalf("statut = %d, attendu 404", result.Status)
	}
	// html/template échappe les apostrophes : la comparaison se fait sur le texte
	// tel que l'utilisateur le lit, pas tel qu'il est encodé.
	if !strings.Contains(html.UnescapeString(result.Body), "Cette page n'existe pas") {
		t.Errorf("la page 404 ne porte pas le message français attendu : %s", result.Body)
	}
}

// TestHomeIsExactPath : « / » ne doit pas se comporter en préfixe, sinon
// toutes les URLs inconnues rendraient le tableau de bord.
func TestHomeIsExactPath(t *testing.T) {
	t.Parallel()

	browser := newBrowser(t, newSite(t).handler)
	browser.login(ownerEmail)

	if result := browser.get("/quelque-chose"); result.Status == http.StatusOK {
		t.Error("une URL inconnue rend le tableau de bord au lieu d'un 404")
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

	browser := newBrowser(t, newSite(t).handler)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := browser.get(tc.path)
			if result.Status != http.StatusOK {
				t.Fatalf("statut = %d, attendu 200", result.Status)
			}
			if got := result.Header.Get("Content-Type"); !strings.Contains(got, tc.contentType) {
				t.Errorf("Content-Type = %q, attendu un type contenant %q", got, tc.contentType)
			}
			if !strings.Contains(result.Body, tc.contains) {
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

	browser := newBrowser(t, newSite(t).handler)

	if !strings.Contains(browser.get("/static/vendor/htmx.min.js").Body, `version:"2.0.10"`) {
		t.Error("htmx.min.js ne porte pas la version 2.0.10 annoncée dans static/vendor/VERSION")
	}
}

func TestSecurityHeaders(t *testing.T) {
	t.Parallel()

	browser := newBrowser(t, newSite(t).handler)
	result := browser.get("/connexion")

	csp := result.Header.Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("aucune politique de sécurité du contenu")
	}
	for _, directive := range []string{"default-src 'self'", "frame-ancestors 'none'", "form-action 'self'", "base-uri 'none'"} {
		if !strings.Contains(csp, directive) {
			t.Errorf("CSP sans la directive %q : %s", directive, csp)
		}
	}
	for _, forbidden := range []string{"unsafe-inline", "unsafe-eval", "*"} {
		if strings.Contains(csp, forbidden) {
			t.Errorf("CSP avec %q : %s", forbidden, csp)
		}
	}

	for header, want := range map[string]string{
		"X-Content-Type-Options":     "nosniff",
		"Referrer-Policy":            "same-origin",
		"Cross-Origin-Opener-Policy": "same-origin",
	} {
		if got := result.Header.Get(header); got != want {
			t.Errorf("%s = %q, attendu %q", header, got, want)
		}
	}
}

// TestAssetURLsCarryBuildStamp : une mise à jour du binaire doit invalider
// le cache du navigateur sans intervention.
func TestAssetURLsCarryBuildStamp(t *testing.T) {
	t.Parallel()

	browser := newBrowser(t, newSite(t).handler)

	if !strings.Contains(browser.get("/connexion").Body, "avanti.css?v=v0.0.0-test") {
		t.Error("les URLs d'assets ne portent pas l'estampille de build")
	}
}
