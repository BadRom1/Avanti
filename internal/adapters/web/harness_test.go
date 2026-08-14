// Harnais des tests de l'adapter web.
//
// Les tests exercent le gestionnaire complet — intergiciels compris — sans ouvrir
// de port : c'est la seule façon de vérifier que la protection intersites, le
// chargement de session et l'authentification sont bien empilés dans le bon ordre.
// Un test qui appellerait les gestionnaires de route directement passerait à côté
// de tout ce qui compte ici.
package web_test

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alexedwards/scs/v2/memstore"

	"github.com/Romain-Badino/Avanti/internal/adapters/web"
	"github.com/Romain-Badino/Avanti/internal/devis"
	"github.com/Romain-Badino/Avanti/internal/document"
	"github.com/Romain-Badino/Avanti/internal/identity"
	"github.com/Romain-Badino/Avanti/internal/platform"
	"github.com/Romain-Badino/Avanti/internal/platform/logging"
)

// Identifiants des comptes que le harness crée systématiquement.
const (
	ownerEmail        = "romain@exemple.fr"
	collaboratorEmail = "architecte@exemple.fr"
	disabledEmail     = "ancien@exemple.fr"
	password          = "phrase de passe du chantier"
)

// baseURLTest est l'URL publique de l'instance de test. En http, donc le cookie
// de session n'est pas Secure — c'est exactement le comportement attendu en
// développement, et un test le vérifie dans les deux schémas.
const baseURLTest = "http://avanti.test"

// plainHasher est un [identity.Hasher] instantané : les tests web n'ont rien
// à apprendre de la lenteur d'argon2id, et la payer à chaque connexion rendrait
// cette suite inutilisable.
type plainHasher struct{}

func (plainHasher) Hash(password string) (identity.PasswordHash, error) {
	return identity.PasswordHash("trivial:" + password), nil
}

func (plainHasher) Verify(hash identity.PasswordHash, password string) (bool, error) {
	return string(hash) == "trivial:"+password, nil
}

// memRepo est un [identity.UserRepository] en mémoire.
type memRepo struct {
	accounts map[identity.ID]identity.User
}

func (d *memRepo) Create(_ context.Context, user identity.User) error {
	for _, existing := range d.accounts {
		if existing.Email == user.Email {
			return fmt.Errorf("%w : %s", identity.ErrEmailTaken, user.Email)
		}
	}
	d.accounts[user.ID] = user
	return nil
}

func (d *memRepo) ByEmail(_ context.Context, email string) (identity.User, error) {
	for _, user := range d.accounts {
		if user.Email == email {
			return user, nil
		}
	}
	return identity.User{}, identity.ErrUnknownUser
}

func (d *memRepo) ByID(_ context.Context, id identity.ID) (identity.User, error) {
	user, ok := d.accounts[id]
	if !ok {
		return identity.User{}, identity.ErrUnknownUser
	}
	return user, nil
}

func (d *memRepo) Update(_ context.Context, user identity.User) error {
	if _, ok := d.accounts[user.ID]; !ok {
		return identity.ErrUnknownUser
	}
	d.accounts[user.ID] = user
	return nil
}

func (d *memRepo) List(_ context.Context) ([]identity.User, error) {
	accounts := slices.Collect(maps.Values(d.accounts))
	slices.SortFunc(accounts, func(a, b identity.User) int {
		return strings.Compare(a.Email, b.Email)
	})
	return accounts, nil
}

// oauthSecretTest est la clé HMAC des tests. Sa seule contrainte est de faire au
// moins trente-deux octets, comme celle de production.
const oauthSecretTest = "cle-hmac-de-test-sans-aucun-usage-reel"

// site est le gestionnaire web sous test, avec ce qu'il faut pour agir sur son
// état — désactiver un compte, par exemple.
type site struct {
	handler   *web.Handler
	accounts  *identity.AccountService
	repo      *memRepo
	oauth     *memOAuthStore
	devis     *memDevisRepo
	documents *memDocumentRepo
	storage   *memDocumentStorage
}

// newSite monte l'adapter avec trois comptes : un propriétaire, un
// collaborateur et un compte désactivé.
func newSite(t *testing.T) *site {
	t.Helper()

	return newSiteWithBaseURL(t, baseURLTest)
}

func newSiteWithBaseURL(t *testing.T, raw string) *site {
	t.Helper()

	repo := &memRepo{accounts: make(map[identity.ID]identity.User)}

	accounts, err := identity.NewAccountService(identity.ServiceOptions{
		Repo:   repo,
		Hasher: plainHasher{},
	})
	if err != nil {
		t.Fatalf("identity.NewAccountService() échoué : %v", err)
	}

	for _, account := range []struct {
		email string
		name  string
		role  identity.Role
	}{
		{email: ownerEmail, name: "Romain Badino", role: identity.RoleProprietaire},
		{email: collaboratorEmail, name: "Amélie Dupré", role: identity.RoleCollaborateur},
		{email: disabledEmail, name: "Ancien Compte", role: identity.RoleProprietaire},
	} {
		user, createErr := accounts.Create(t.Context(), account.email, account.name, password, account.role)
		if createErr != nil {
			t.Fatalf("création du compte %s échouée : %v", account.email, createErr)
		}
		if account.email == disabledEmail {
			if disableErr := accounts.Deactivate(t.Context(), user.ID); disableErr != nil {
				t.Fatalf("désactivation du compte %s échouée : %v", account.email, disableErr)
			}
		}
	}

	baseURL, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("URL de test illisible : %v", err)
	}

	oauthStore := newMemOAuthStore()

	devisRepo := newMemDevisRepo()
	devisService, err := devis.NewService(devis.ServiceOptions{Repo: devisRepo})
	if err != nil {
		t.Fatalf("devis.NewService() échoué : %v", err)
	}

	documentRepo := newMemDocumentRepo()
	documentStorage := newMemDocumentStorage()
	documentsService, err := document.NewService(document.ServiceOptions{
		Repo:    documentRepo,
		Storage: documentStorage,
	})
	if err != nil {
		t.Fatalf("document.NewService() échoué : %v", err)
	}

	handler, err := web.New(web.Options{
		Logger:       logging.Discard(),
		Build:        platform.BuildInfo{Version: "v0.0.0-test"},
		Accounts:     accounts,
		Sessions:     memstore.New(),
		BaseURL:      baseURL,
		OAuthStorage: oauthStore,
		OAuthSecret:  []byte(oauthSecretTest),
		Devis:        devisService,
		Documents:    documentsService,
	})
	if err != nil {
		t.Fatalf("web.New() échoué : %v", err)
	}

	return &site{
		handler:   handler,
		accounts:  accounts,
		repo:      repo,
		oauth:     oauthStore,
		devis:     devisRepo,
		documents: documentRepo,
		storage:   documentStorage,
	}
}

// disable ferme le compte portant cet email.
func (s *site) disable(t *testing.T, email string) {
	t.Helper()

	user, err := s.repo.ByEmail(t.Context(), email)
	if err != nil {
		t.Fatalf("compte %s introuvable : %v", email, err)
	}
	if err := s.accounts.Deactivate(t.Context(), user.ID); err != nil {
		t.Fatalf("désactivation de %s échouée : %v", email, err)
	}
}

// browser exerce le gestionnaire en conservant les cookies d'une requête à la
// suivante, comme le ferait un navigateur — sans quoi rien de ce qui concerne les
// sessions ne serait vérifiable.
//
// Il ne suit délibérément pas les redirections : leur cible fait partie de ce que
// les tests vérifient.
type browser struct {
	t       *testing.T
	handler http.Handler
	cookies map[string]*http.Cookie
}

func newBrowser(t *testing.T, handler http.Handler) *browser {
	t.Helper()
	return &browser{t: t, handler: handler, cookies: make(map[string]*http.Cookie)}
}

// httpResult est une réponse déjà lue et refermée.
type httpResult struct {
	Status  int
	Header  http.Header
	Body    string
	Cookies []*http.Cookie
}

// Location rend la cible de la redirection.
func (r httpResult) Location() string {
	return r.Header.Get("Location")
}

func (n *browser) get(target string, headers ...string) httpResult {
	n.t.Helper()
	return n.send(httptest.NewRequestWithContext(n.t.Context(), http.MethodGet, target, http.NoBody), headers...)
}

// post soumet un formulaire encodé en application/x-www-form-urlencoded.
func (n *browser) post(target string, fields url.Values, headers ...string) httpResult {
	n.t.Helper()

	req := httptest.NewRequestWithContext(n.t.Context(), http.MethodPost, target, strings.NewReader(fields.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	return n.send(req, headers...)
}

// send joue la requête, y ajoute les cookies mémorisés et récolte les
// nouveaux. entetes est une suite clé, valeur, clé, valeur…
func (n *browser) send(req *http.Request, headers ...string) httpResult {
	n.t.Helper()

	if len(headers)%2 != 0 {
		n.t.Fatalf("entetes : %d arguments, un nombre pair est attendu", len(headers))
	}
	for i := 0; i < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}

	// RemoteAddr sert de clé au garde-fou anti-force-brute : sans elle, httptest
	// laisse le champ vide et tous les tests partageraient le même compteur.
	if req.RemoteAddr == "" {
		req.RemoteAddr = "192.0.2.10:54321"
	}

	for _, cookie := range n.cookies {
		req.AddCookie(cookie)
	}

	rec := httptest.NewRecorder()
	n.handler.ServeHTTP(rec, req)

	result := rec.Result()
	defer func() {
		if err := result.Body.Close(); err != nil {
			n.t.Errorf("fermeture du corps de réponse : %v", err)
		}
	}()

	body, err := io.ReadAll(result.Body)
	if err != nil {
		n.t.Fatalf("lecture du corps de réponse : %v", err)
	}

	for _, cookie := range result.Cookies() {
		// MaxAge négatif ou date passée : le serveur demande la suppression.
		if cookie.MaxAge < 0 || (!cookie.Expires.IsZero() && cookie.Expires.Before(time.Now())) {
			delete(n.cookies, cookie.Name)
			continue
		}
		n.cookies[cookie.Name] = cookie
	}

	return httpResult{
		Status:  result.StatusCode,
		Header:  result.Header,
		Body:    string(body),
		Cookies: result.Cookies(),
	}
}

// sessionCookie rend le cookie de session mémorisé, ou nil.
func (n *browser) sessionCookie() *http.Cookie {
	return n.cookies["avanti_session"]
}

// login fait passer le navigateur par le formulaire, et échoue le test si
// la connexion ne réussit pas.
func (n *browser) login(email string) httpResult {
	n.t.Helper()

	result := n.post("/connexion", url.Values{
		"email":        {email},
		"mot_de_passe": {password},
	})
	if result.Status != http.StatusSeeOther {
		n.t.Fatalf("connexion de %s : statut = %d, attendu 303 — corps : %s", email, result.Status, result.Body)
	}

	return result
}
