package web_test

import (
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/alexedwards/scs/v2/memstore"

	"github.com/Romain-Badino/Avanti/internal/adapters/web"
	"github.com/Romain-Badino/Avanti/internal/platform/logging"
)

func TestNewRejectsMissingDependency(t *testing.T) {
	t.Parallel()

	full := newSite(t)
	baseURL, err := url.Parse(baseURLTest)
	if err != nil {
		t.Fatalf("URL de test illisible : %v", err)
	}

	cases := map[string]web.Options{
		"sans rien": {},
		"sans journal": {
			Accounts: full.accounts, Sessions: memstore.New(), BaseURL: baseURL,
		},
		"sans service de comptes": {
			Logger: logging.Discard(), Sessions: memstore.New(), BaseURL: baseURL,
		},
		"sans magasin de sessions": {
			Logger: logging.Discard(), Accounts: full.accounts, BaseURL: baseURL,
		},
		"sans URL publique": {
			Logger: logging.Discard(), Accounts: full.accounts, Sessions: memstore.New(),
		},
		"sans service de devis": {
			Logger: logging.Discard(), Accounts: full.accounts, Sessions: memstore.New(),
			BaseURL: baseURL, OAuthStorage: full.oauth, OAuthSecret: []byte(oauthSecretTest),
		},
	}

	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := web.New(opts); err == nil {
				t.Error("web.New() doit refuser une dépendance manquante")
			}
		})
	}
}

// TestProtectedRoutesRedirect est le test de fond de l'intergiciel : tout ce
// qui n'est pas explicitement public envoie l'anonyme au formulaire.
func TestProtectedRoutesRedirect(t *testing.T) {
	t.Parallel()

	targets := []string{
		"/",
		"/devis",
		"/finances",
		"/une/page/inventee",
		// Une page inexistante redirige elle aussi, au lieu de répondre 404 : dire
		// à un visiteur non authentifié quelles URLs existent lui apprendrait la
		// forme de l'application avant même qu'il ait un compte.
		"/.env",
	}

	browser := newBrowser(t, newSite(t).handler)

	for _, target := range targets {
		result := browser.get(target)
		if result.Status != http.StatusSeeOther {
			t.Errorf("GET %s = %d, attendu 303 vers /connexion", target, result.Status)
			continue
		}
		if location := result.Location(); !strings.HasPrefix(location, "/connexion") {
			t.Errorf("GET %s redirige vers %q, attendu /connexion", target, location)
		}
	}
}

// TestPublicRoutes : le formulaire et les assets restent joignables sans
// session, sinon la page de connexion s'afficherait sans sa feuille de style.
func TestPublicRoutes(t *testing.T) {
	t.Parallel()

	browser := newBrowser(t, newSite(t).handler)

	for _, target := range []string{"/connexion", "/static/css/avanti.css", "/static/vendor/htmx.min.js"} {
		if result := browser.get(target); result.Status != http.StatusOK {
			t.Errorf("GET %s = %d, attendu 200", target, result.Status)
		}
	}
}

// TestRedirectKeepsRequestedPage : après connexion, on revient là où on
// allait.
func TestRedirectKeepsRequestedPage(t *testing.T) {
	t.Parallel()

	browser := newBrowser(t, newSite(t).handler)

	result := browser.get("/devis?statut=accepte")
	want := "/connexion?suite=" + url.QueryEscape("/devis?statut=accepte")
	if result.Location() != want {
		t.Errorf("redirection vers %q, attendu %q", result.Location(), want)
	}
}

// TestOpenRedirectRejected est le test de sécurité de la reprise de
// navigation : une cible hors de l'application est écartée, et la connexion
// ramène à l'accueil.
func TestOpenRedirectRejected(t *testing.T) {
	t.Parallel()

	hostile := []string{
		"https://phishing.example/connexion",
		"//phishing.example/",
		`/\phishing.example/`,
		"http://phishing.example",
		"javascript:alert(1)",
	}

	for _, hostile := range hostile {
		t.Run(hostile, func(t *testing.T) {
			t.Parallel()

			browser := newBrowser(t, newSite(t).handler)

			result := browser.post("/connexion", url.Values{
				"email":        {ownerEmail},
				"mot_de_passe": {password},
				"suite":        {hostile},
			})
			if result.Status != http.StatusSeeOther {
				t.Fatalf("statut = %d, attendu 303", result.Status)
			}
			if location := result.Location(); location != "/" {
				t.Errorf("la connexion redirige vers %q, l'accueil était attendu", location)
			}
		})
	}
}

// TestLoginSuccess vérifie le chemin nominal de bout en bout, cookie compris.
func TestLoginSuccess(t *testing.T) {
	t.Parallel()

	browser := newBrowser(t, newSite(t).handler)

	// Une visite préalable crée une session anonyme : c'est la situation dans
	// laquelle la fixation de session serait exploitable, et donc celle où le
	// renouvellement du jeton doit avoir lieu.
	browser.get("/connexion")
	before := browser.sessionCookie()

	result := browser.login(ownerEmail)
	if location := result.Location(); location != "/" {
		t.Errorf("la connexion redirige vers %q, attendu /", location)
	}

	cookie := browser.sessionCookie()
	if cookie == nil {
		t.Fatal("aucun cookie de session après connexion")
	}
	if before != nil && cookie.Value == before.Value {
		t.Error("le jeton de session n'a pas changé à la connexion : la fixation de session reste possible")
	}

	home := browser.get("/")
	if home.Status != http.StatusOK {
		t.Fatalf("GET / après connexion = %d, attendu 200", home.Status)
	}
	if !strings.Contains(home.Body, "Romain Badino") {
		t.Error("l'accueil n'affiche pas le nom de la personne connectée")
	}
}

func TestSessionCookieIsHardened(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		baseURL    string
		wantSecure bool
	}{
		{name: "en http, Secure serait un cookie jamais envoyé", baseURL: "http://avanti.test", wantSecure: false},
		{name: "en https, Secure est posé", baseURL: "https://avanti.test", wantSecure: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			browser := newBrowser(t, newSiteWithBaseURL(t, tc.baseURL).handler)
			browser.login(ownerEmail)

			cookie := browser.sessionCookie()
			if cookie == nil {
				t.Fatal("aucun cookie de session après connexion")
			}
			if !cookie.HttpOnly {
				t.Error("le cookie de session doit être HttpOnly")
			}
			if cookie.SameSite != http.SameSiteLaxMode {
				t.Errorf("SameSite = %v, attendu Lax", cookie.SameSite)
			}
			if cookie.Path != "/" {
				t.Errorf("Path = %q, attendu /", cookie.Path)
			}
			if cookie.Secure != tc.wantSecure {
				t.Errorf("Secure = %t, attendu %t pour %s", cookie.Secure, tc.wantSecure, tc.baseURL)
			}
			if strings.Contains(cookie.Value, "@") {
				t.Errorf("le cookie porte %q : un identifiant de session ne doit rien révéler du compte", cookie.Value)
			}
		})
	}
}

// TestLoginFailuresAreIndistinguishable est le pendant côté interface de
// l'indistinguabilité du domaine : le message affiché est le même mot pour mot,
// que l'adresse existe ou non.
func TestLoginFailuresAreIndistinguishable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		email    string
		password string
	}{
		{name: "email inconnu", email: "personne@exemple.fr", password: password},
		{name: "email malformé", email: "pas-un-email", password: password},
		{name: "mauvais mot de passe", email: ownerEmail, password: "un autre mot de passe"},
		{name: "mot de passe vide", email: ownerEmail, password: ""},
	}

	body := make(map[string]string, len(cases))

	for _, tc := range cases {
		// Un site par cas : le garde-fou anti-force-brute compte par compte et par
		// adresse, et quatre échecs de suite déclencheraient le blocage.
		browser := newBrowser(t, newSite(t).handler)

		result := browser.post("/connexion", url.Values{
			"email":        {tc.email},
			"mot_de_passe": {tc.password},
		})

		if result.Status != http.StatusUnauthorized {
			t.Errorf("%s : statut = %d, attendu 401", tc.name, result.Status)
		}
		if browser.sessionCookie() != nil && result.Status == http.StatusUnauthorized {
			// Une session anonyme peut exister ; ce qui compte est qu'elle n'ouvre rien.
			if after := browser.get("/"); after.Status == http.StatusOK {
				t.Errorf("%s : l'accueil est accessible après un échec de connexion", tc.name)
			}
		}

		body[tc.name] = errorMessage(t, result.Body)
	}

	baseline := body["email inconnu"]
	if baseline == "" {
		t.Fatal("aucun message d'erreur affiché sur un email inconnu")
	}
	for name, message := range body {
		if message != baseline {
			t.Errorf("%s affiche %q, alors que « email inconnu » affiche %q — les deux doivent être identiques", name, message, baseline)
		}
	}
}

// TestDisabledAccount : le message diffère, mais il faut avoir prouvé qu'on
// connaît le mot de passe pour l'obtenir.
func TestDisabledAccount(t *testing.T) {
	t.Parallel()

	site := newSite(t)
	browser := newBrowser(t, site.handler)

	result := browser.post("/connexion", url.Values{
		"email":        {disabledEmail},
		"mot_de_passe": {password},
	})
	if result.Status != http.StatusForbidden {
		t.Fatalf("statut = %d, attendu 403", result.Status)
	}
	if message := errorMessage(t, result.Body); !strings.Contains(message, "désactivé") {
		t.Errorf("message = %q, il devrait mentionner la désactivation", message)
	}
	if browser.get("/").Status == http.StatusOK {
		t.Error("un compte désactivé ne doit pas ouvrir l'accueil")
	}

	// Avec un mauvais mot de passe, en revanche, rien ne filtre de l'état du compte.
	other := newBrowser(t, newSite(t).handler)
	refused := other.post("/connexion", url.Values{
		"email":        {disabledEmail},
		"mot_de_passe": {"mauvais mot de passe"},
	})
	if refused.Status != http.StatusUnauthorized {
		t.Errorf("statut = %d, attendu 401 : l'état du compte ne doit pas se deviner sans le mot de passe", refused.Status)
	}
}

// TestDeactivationEndsLiveSessions : le rôle et l'état du compte sont
// relus à chaque requête, ce qui fait qu'une désactivation prend effet tout de
// suite au lieu d'attendre l'expiration des sessions ouvertes.
func TestDeactivationEndsLiveSessions(t *testing.T) {
	t.Parallel()

	site := newSite(t)
	browser := newBrowser(t, site.handler)

	browser.login(ownerEmail)
	if browser.get("/").Status != http.StatusOK {
		t.Fatal("la session n'est pas ouverte")
	}

	site.disable(t, ownerEmail)

	if result := browser.get("/"); result.Status != http.StatusSeeOther {
		t.Errorf("GET / = %d après désactivation, attendu 303 vers /connexion", result.Status)
	}
}

func TestLogout(t *testing.T) {
	t.Parallel()

	browser := newBrowser(t, newSite(t).handler)
	browser.login(ownerEmail)

	result := browser.post("/deconnexion", nil)
	if result.Status != http.StatusSeeOther {
		t.Fatalf("POST /deconnexion = %d, attendu 303", result.Status)
	}
	if location := result.Location(); !strings.HasPrefix(location, "/connexion") {
		t.Errorf("la déconnexion redirige vers %q, attendu /connexion", location)
	}

	if after := browser.get("/"); after.Status != http.StatusSeeOther {
		t.Errorf("GET / = %d après déconnexion, attendu 303", after.Status)
	}

	// La page de connexion annonce la déconnexion plutôt que de laisser croire à
	// une expiration.
	form := browser.get("/connexion?deconnexion")
	if !strings.Contains(html.UnescapeString(form.Body), "Vous avez été déconnecté") {
		t.Error("la page de connexion n'annonce pas la déconnexion")
	}
}

// TestLogoutRejectsGet : une déconnexion est un changement d'état. Une
// image ou un lien préchargé pointant sur une URL en GET suffirait à déconnecter
// quelqu'un à son insu.
func TestLogoutRejectsGet(t *testing.T) {
	t.Parallel()

	browser := newBrowser(t, newSite(t).handler)
	browser.login(ownerEmail)

	if result := browser.get("/deconnexion"); result.Status == http.StatusSeeOther && result.Location() == "/connexion?deconnexion" {
		t.Fatal("GET /deconnexion a déconnecté")
	}
	if browser.get("/").Status != http.StatusOK {
		t.Error("la session a été perdue à la suite d'un GET sur /deconnexion")
	}
}

// TestLoginFormRedirectsWhenLoggedIn : proposer de se connecter à quelqu'un
// qui l'est déjà n'a pas de sens.
func TestLoginFormRedirectsWhenLoggedIn(t *testing.T) {
	t.Parallel()

	browser := newBrowser(t, newSite(t).handler)
	browser.login(ownerEmail)

	result := browser.get("/connexion")
	if result.Status != http.StatusSeeOther || result.Location() != "/" {
		t.Errorf("GET /connexion connecté = %d vers %q, attendu 303 vers /", result.Status, result.Location())
	}
}

// TestBruteForceGuard : après cinq échecs sur le même couple compte + adresse,
// les tentatives suivantes sont refusées sans même consulter le mot de passe.
func TestBruteForceGuard(t *testing.T) {
	t.Parallel()

	browser := newBrowser(t, newSite(t).handler)

	wrong := url.Values{"email": {ownerEmail}, "mot_de_passe": {"mauvais mot de passe"}}

	for attempt := 1; attempt <= 5; attempt++ {
		if result := browser.post("/connexion", wrong); result.Status != http.StatusUnauthorized {
			t.Fatalf("tentative %d : statut = %d, attendu 401", attempt, result.Status)
		}
	}

	blocked := browser.post("/connexion", wrong)
	if blocked.Status != http.StatusTooManyRequests {
		t.Fatalf("sixième tentative : statut = %d, attendu 429", blocked.Status)
	}

	// Le blocage porte sur les tentatives, pas sur la validité du mot de passe :
	// le bon mot de passe est refusé lui aussi, sinon le garde-fou ne servirait à
	// rien contre un script qui finit par tomber juste.
	withGoodPassword := browser.post("/connexion", url.Values{
		"email":        {ownerEmail},
		"mot_de_passe": {password},
	})
	if withGoodPassword.Status != http.StatusTooManyRequests {
		t.Errorf("statut = %d avec le bon mot de passe pendant le blocage, attendu 429", withGoodPassword.Status)
	}

	// Un autre compte depuis la même adresse n'est pas affecté : la clé est le
	// couple, pas l'adresse seule.
	otherAccount := browser.post("/connexion", url.Values{
		"email":        {collaboratorEmail},
		"mot_de_passe": {password},
	})
	if otherAccount.Status != http.StatusSeeOther {
		t.Errorf("statut = %d pour un autre compte, le blocage ne doit pas s'étendre à toute l'adresse", otherAccount.Status)
	}
}

// TestLoginSuccessResetsTheCounter : une connexion réussie efface les
// échecs qui l'ont précédée, sans quoi une faute de frappe le matin bloquerait
// l'après-midi.
func TestLoginSuccessResetsTheCounter(t *testing.T) {
	t.Parallel()

	browser := newBrowser(t, newSite(t).handler)
	wrong := url.Values{"email": {ownerEmail}, "mot_de_passe": {"mauvais mot de passe"}}

	for range 4 {
		browser.post("/connexion", wrong)
	}
	browser.login(ownerEmail)
	browser.post("/deconnexion", nil)

	for range 4 {
		if result := browser.post("/connexion", wrong); result.Status != http.StatusUnauthorized {
			t.Fatalf("statut = %d, attendu 401 : le compteur devait être remis à zéro", result.Status)
		}
	}
}

// TestCrossOriginProtection : la protection de la bibliothèque standard refuse un
// POST annoncé comme venant d'un autre site.
func TestCrossOriginProtection(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		headers  []string
		rejected bool
	}{
		{
			name:     "POST annoncé cross-site",
			headers:  []string{"Sec-Fetch-Site", "cross-site"},
			rejected: true,
		},
		{
			name:     "POST annoncé same-site",
			headers:  []string{"Sec-Fetch-Site", "same-site"},
			rejected: true,
		},
		{
			name:     "POST avec une Origin étrangère",
			headers:  []string{"Origin", "https://phishing.example"},
			rejected: true,
		},
		{
			name:     "POST same-origin",
			headers:  []string{"Sec-Fetch-Site", "same-origin"},
			rejected: false,
		},
		{
			name:     "POST avec l'Origin de l'instance",
			headers:  []string{"Origin", baseURLTest},
			rejected: false,
		},
		{
			name: "POST sans en-tête, comme un client non navigateur",
			// Sans Sec-Fetch-Site ni Origin, la requête est supposée non
			// navigateur — c'est le comportement documenté de la bibliothèque, et
			// c'est ce qui laisse curl fonctionner.
			headers:  nil,
			rejected: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			browser := newBrowser(t, newSite(t).handler)

			result := browser.post("/connexion", url.Values{
				"email":        {ownerEmail},
				"mot_de_passe": {password},
			}, tc.headers...)

			rejected := result.Status == http.StatusForbidden
			if rejected != tc.rejected {
				t.Errorf("statut = %d (refusé : %t), refus attendu : %t", result.Status, rejected, tc.rejected)
			}
		})
	}
}

// TestCrossOriginProtectionAllowsReads : GET reste une méthode sûre,
// sinon aucun lien externe vers Avanti ne fonctionnerait.
func TestCrossOriginProtectionAllowsReads(t *testing.T) {
	t.Parallel()

	browser := newBrowser(t, newSite(t).handler)

	result := browser.get("/connexion", "Sec-Fetch-Site", "cross-site")
	if result.Status != http.StatusOK {
		t.Errorf("GET /connexion depuis un autre site = %d, attendu 200", result.Status)
	}
}

// errorMessage extrait le contenu du bloc d'avis d'erreur de la page.
func errorMessage(t *testing.T, body string) string {
	t.Helper()

	const marker = `role="alert">`

	start := strings.Index(body, marker)
	if start < 0 {
		return ""
	}
	rest := body[start+len(marker):]

	end := strings.Index(rest, "<")
	if end < 0 {
		t.Fatalf("bloc d'avis non refermé dans la page : %s", rest)
	}

	return strings.TrimSpace(html.UnescapeString(rest[:end]))
}
