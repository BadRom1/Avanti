// Test de bout en bout du serveur d'autorisation OAuth 2.1, avec le magasin
// PostgreSQL réel branché sur l'adapter web.
//
// Il vit dans cmd/avanti parce que c'est le seul endroit du dépôt autorisé à
// connaître les deux familles d'adapters à la fois (R4 de docs/ARCHITECTURE.md),
// et parce que c'est exactement ce qu'il vérifie : que le magasin de
// adapters/postgres et le fournisseur de adapters/web fonctionnent **assemblés**.
//
// Ce que les autres suites ne couvrent pas, et qui se joue ici :
//
//   - adapters/web exerce le protocole entier, mais sur un magasin en mémoire.
//     Il dirait que le flux est juste même si le SQL ne l'était pas ;
//   - adapters/postgres exerce le SQL, mais méthode par méthode. Il dirait que
//     le magasin respecte son contrat même si fosite l'appelait autrement.
//
// La base vient de AVANTI_TEST_DATABASE_URL si elle est renseignée — c'est le
// chemin de la CI — et de testcontainers sinon. Sans l'une ni l'autre, le test
// se saute proprement.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alexedwards/scs/pgxstore"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/Romain-Badino/Avanti/internal/adapters/postgres"
	"github.com/Romain-Badino/Avanti/internal/adapters/storage"
	"github.com/Romain-Badino/Avanti/internal/adapters/web"
	"github.com/Romain-Badino/Avanti/internal/document"
	"github.com/Romain-Badino/Avanti/internal/identity"
	"github.com/Romain-Badino/Avanti/internal/platform"
	"github.com/Romain-Badino/Avanti/internal/platform/db"
	"github.com/Romain-Badino/Avanti/internal/platform/logging"
	"github.com/Romain-Badino/Avanti/internal/platform/migrate"
)

const (
	testDatabaseURLEnv = "AVANTI_TEST_DATABASE_URL"
	postgresImage      = "postgres:18-alpine"

	e2eEmail       = "proprietaire@exemple.fr"
	e2ePassword    = "phrase de passe du chantier"
	e2eOAuthSecret = "cle-hmac-de-bout-en-bout-sans-usage-reel"
	e2eRedirectURI = "https://agent.exemple.fr/callback"
	e2eState       = "etat-de-bout-en-bout-assez-long-pour-fosite"
	e2eScope       = "mcp devis:read"
)

// dbCounterE2E distingue les bases créées par ce fichier de celles des autres
// suites, qui vivent dans d'autres paquets et donc d'autres processus.
var dbCounterE2E atomic.Int64

// serverDSN désigne le serveur PostgreSQL de test, ou reste vide.
var serverDSN string

// skipReason explique pourquoi le test se saute, quand il se saute.
var skipReason string

func TestMain(m *testing.M) {
	code := func() int {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()

		if dsn := strings.TrimSpace(os.Getenv(testDatabaseURLEnv)); dsn != "" {
			serverDSN = dsn
			return m.Run()
		}

		container, err := startPostgres(ctx)
		if err != nil {
			skipReason = err.Error()
			return m.Run()
		}
		defer func() {
			if terminateErr := testcontainers.TerminateContainer(container); terminateErr != nil {
				fmt.Fprintf(os.Stderr, "arrêt du conteneur PostgreSQL : %v\n", terminateErr)
			}
		}()

		serverDSN, err = container.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			skipReason = fmt.Sprintf("chaîne de connexion du conteneur indisponible : %v", err)
		}

		return m.Run()
	}()

	os.Exit(code)
}

// startPostgres lève un PostgreSQL jetable, après avoir vérifié que Docker
// répond — sans quoi l'échec serait un long délai d'attente plutôt qu'un saut
// immédiat.
func startPostgres(ctx context.Context) (*tcpostgres.PostgresContainer, error) {
	provider, providerErr := testcontainers.NewDockerProvider()
	if providerErr != nil {
		return nil, fmt.Errorf("Docker indisponible et %s non renseignée : %w", testDatabaseURLEnv, providerErr)
	}
	defer func() {
		if closeErr := provider.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "fermeture du client Docker : %v\n", closeErr)
		}
	}()

	if healthErr := provider.Health(ctx); healthErr != nil {
		return nil, fmt.Errorf("Docker ne répond pas et %s non renseignée : %w", testDatabaseURLEnv, healthErr)
	}

	return tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("avanti_test"),
		tcpostgres.WithUsername("avanti"),
		tcpostgres.WithPassword("change-me-test-only"),
		tcpostgres.BasicWaitStrategies(),
	)
}

// freshDatabase crée une base vierge et la supprime au nettoyage.
func freshDatabase(t *testing.T) string {
	t.Helper()

	if skipReason != "" {
		t.Skip("test d'intégration sauté : " + skipReason)
	}
	if serverDSN == "" {
		t.Skip("test d'intégration sauté : aucune base PostgreSQL de test disponible")
	}

	name := fmt.Sprintf("avanti_oauth_%d_%d", os.Getpid(), dbCounterE2E.Add(1))

	admin, err := pgx.Connect(t.Context(), serverDSN)
	if err != nil {
		t.Fatalf("connexion au serveur PostgreSQL de test : %v", err)
	}
	defer func() {
		if closeErr := admin.Close(t.Context()); closeErr != nil {
			t.Errorf("fermeture de la connexion d'administration : %v", closeErr)
		}
	}()

	// Le nom est fabriqué localement à partir d'un compteur : il ne vient d'aucune
	// entrée externe, et PostgreSQL ne permet pas de paramétrer un identifiant de
	// CREATE DATABASE.
	if _, err := admin.Exec(t.Context(), "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatalf("création de la base de test %s : %v", name, err)
	}

	t.Cleanup(func() {
		// Contexte neuf : celui du test est déjà annulé quand le nettoyage court.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		conn, err := pgx.Connect(ctx, serverDSN)
		if err != nil {
			t.Errorf("reconnexion pour supprimer %s : %v", name, err)
			return
		}
		defer func() { _ = conn.Close(ctx) }() //nolint:errcheck // la base est jetable, une fermeture ratée n'a rien à apprendre au test.

		if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Errorf("suppression de la base de test %s : %v", name, err)
		}
	})

	return replaceDatabase(t, serverDSN, name)
}

// replaceDatabase remplace le nom de base d'une chaîne de connexion.
func replaceDatabase(t *testing.T, dsn, base string) string {
	t.Helper()

	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("chaîne de connexion de test illisible : %v", err)
	}

	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
		config.User, config.Password, config.Host, config.Port, base)
}

// avantiInstance est une instance d'Avanti servie sur un port éphémère, montée
// exactement comme le fait la commande serve.
type avantiInstance struct {
	server   *httptest.Server
	site     *web.Handler
	accounts *identity.AccountService
	client   *http.Client
	owner    identity.User
	// pool sert aux assertions que les réponses HTTP ne permettent pas : ce qui
	// reste réellement en base après une course, par exemple.
	pool *pgxpool.Pool
}

// startAvanti monte l'application entière sur une base neuve.
func startAvanti(t *testing.T) *avantiInstance {
	t.Helper()

	pool := openPool(t, freshDatabase(t))
	applyMigrations(t, pool)

	repo, err := postgres.NewUserRepo(pool)
	if err != nil {
		t.Fatalf("postgres.NewUserRepo() échoué : %v", err)
	}
	oauthStore, err := postgres.NewOAuthStore(pool)
	if err != nil {
		t.Fatalf("postgres.NewOAuthStore() échoué : %v", err)
	}

	accounts, err := identity.NewAccountService(identity.ServiceOptions{
		Repo:   repo,
		Hasher: identity.NewArgon2idHasher(),
	})
	if err != nil {
		t.Fatalf("identity.NewAccountService() échoué : %v", err)
	}

	owner, err := accounts.Create(t.Context(), e2eEmail, "Propriétaire", e2ePassword, identity.RoleProprietaire)
	if err != nil {
		t.Fatalf("création du compte de test : %v", err)
	}

	// Les domaines devis, document et finance sont montés comme en
	// production : l'adapter web les exige, et le bout-en-bout n'a d'intérêt
	// que s'il assemble les mêmes pièces. Seul le stockage diffère — un
	// répertoire jetable du test plutôt que celui de la configuration.
	devisService, err := newDevisService(pool)
	if err != nil {
		t.Fatalf("newDevisService() échoué : %v", err)
	}

	financeService, err := newFinanceService(pool)
	if err != nil {
		t.Fatalf("newFinanceService() échoué : %v", err)
	}

	planningService, err := newPlanningService(pool)
	if err != nil {
		t.Fatalf("newPlanningService() échoué : %v", err)
	}

	documentRepo, err := postgres.NewDocumentRepo(pool)
	if err != nil {
		t.Fatalf("postgres.NewDocumentRepo() échoué : %v", err)
	}
	contentStorage, err := storage.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatalf("storage.NewFilesystem() échoué : %v", err)
	}
	documentsService, err := document.NewService(document.ServiceOptions{
		Repo:    documentRepo,
		Storage: contentStorage,
	})
	if err != nil {
		t.Fatalf("document.NewService() échoué : %v", err)
	}

	sessionStore := pgxstore.NewWithCleanupInterval(pool, web.SessionCleanupInterval)
	t.Cleanup(sessionStore.StopCleanup)

	// L'écouteur est ouvert avant le gestionnaire, parce que l'URL publique est
	// une entrée de la construction : elle décide de l'issuer OAuth, et un issuer
	// qui ne correspondrait pas à l'adresse réellement servie ferait refuser le
	// document de métadonnées par un client conforme.
	server := httptest.NewUnstartedServer(nil)

	baseURL, err := url.Parse("http://" + server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("URL de test illisible : %v", err)
	}

	site, err := web.New(web.Options{
		Logger:       logging.Discard(),
		Build:        platform.BuildInfo{Version: "v0.0.0-e2e"},
		Accounts:     accounts,
		Sessions:     sessionStore,
		BaseURL:      baseURL,
		OAuthStorage: oauthStore,
		OAuthSecret:  []byte(e2eOAuthSecret),
		Devis:        devisService,
		Documents:    documentsService,
		Finance:      financeService,
		Planning:     planningService,
		Exports:      newExports(),
	})
	if err != nil {
		t.Fatalf("web.New() échoué : %v", err)
	}

	server.Config.Handler = site
	server.Start()
	t.Cleanup(server.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New() échoué : %v", err)
	}

	return &avantiInstance{
		server:   server,
		site:     site,
		accounts: accounts,
		owner:    owner,
		pool:     pool,
		client: &http.Client{
			Jar: jar,
			// Les redirections ne sont pas suivies : leur cible fait partie de ce que
			// le test vérifie, et la dernière mène chez le client OAuth, qui n'existe
			// pas.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
			Timeout: 30 * time.Second,
		},
	}
}

func openPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()

	pool, err := db.Open(t.Context(), logging.Discard(), dsn, 30*time.Second)
	if err != nil {
		t.Fatalf("ouverture du pool sur la base de test : %v", err)
	}
	t.Cleanup(pool.Close)

	return pool
}

func applyMigrations(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	sqlDB := db.StdlibDB(pool)
	defer func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("fermeture de la vue database/sql : %v", err)
		}
	}()

	if err := migrate.Up(t.Context(), logging.Discard(), sqlDB); err != nil {
		t.Fatalf("migrate.Up() échoué : %v", err)
	}
}

// --- Requêtes ---------------------------------------------------------------

// response est une réponse déjà lue et refermée.
type response struct {
	Status   int
	Body     string
	Location string
}

func (a *avantiInstance) do(t *testing.T, req *http.Request) response {
	t.Helper()

	result, err := a.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s : %v", req.Method, req.URL.Path, err)
	}
	defer func() {
		if closeErr := result.Body.Close(); closeErr != nil {
			t.Errorf("fermeture du corps de réponse : %v", closeErr)
		}
	}()

	body, err := io.ReadAll(result.Body)
	if err != nil {
		t.Fatalf("lecture du corps de réponse : %v", err)
	}

	return response{
		Status:   result.StatusCode,
		Body:     string(body),
		Location: result.Header.Get("Location"),
	}
}

func (a *avantiInstance) get(t *testing.T, path string) response {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, a.server.URL+path, http.NoBody)
	if err != nil {
		t.Fatalf("construction de la requête : %v", err)
	}

	return a.do(t, req)
}

func (a *avantiInstance) postForm(t *testing.T, path string, fields url.Values) response {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, a.server.URL+path,
		strings.NewReader(fields.Encode()))
	if err != nil {
		t.Fatalf("construction de la requête : %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	return a.do(t, req)
}

func (a *avantiInstance) postJSON(t *testing.T, path string, payload any) response {
	t.Helper()

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("sérialisation du corps JSON : %v", err)
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, a.server.URL+path,
		strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("construction de la requête : %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	return a.do(t, req)
}

// --- Le test ----------------------------------------------------------------

// TestOAuthEndToEnd joue le parcours complet contre une vraie base.
//
// Il suit l'ordre réel des choses, et chaque étape dépend de la précédente : un
// maillon cassé n'importe où fait échouer le reste, ce qui est exactement la
// propriété recherchée d'un test de bout en bout.
func TestOAuthEndToEnd(t *testing.T) {
	t.Parallel()

	app := startAvanti(t)

	// 1. Découverte : le client lit le document de métadonnées, seule chose qu'il
	//    sache du serveur avant de commencer.
	metadata := app.get(t, "/.well-known/oauth-authorization-server")
	if metadata.Status != http.StatusOK {
		t.Fatalf("métadonnées : statut = %d, attendu 200 — corps : %s", metadata.Status, metadata.Body)
	}

	var discovery struct {
		Issuer                string   `json:"issuer"`
		RegistrationEndpoint  string   `json:"registration_endpoint"`
		AuthorizationEndpoint string   `json:"authorization_endpoint"`
		TokenEndpoint         string   `json:"token_endpoint"`
		ChallengeMethods      []string `json:"code_challenge_methods_supported"`
	}
	if err := json.Unmarshal([]byte(metadata.Body), &discovery); err != nil {
		t.Fatalf("métadonnées illisibles : %v — corps : %s", err, metadata.Body)
	}
	if discovery.Issuer != app.server.URL {
		t.Errorf("issuer = %q, attendu %q", discovery.Issuer, app.server.URL)
	}
	if len(discovery.ChallengeMethods) != 1 || discovery.ChallengeMethods[0] != "S256" {
		t.Fatalf("code_challenge_methods_supported = %v, attendu [S256]", discovery.ChallengeMethods)
	}

	// 2. Enregistrement dynamique, sans aucun compte.
	registered := app.postJSON(t, "/oauth/register", map[string]any{
		"client_name":   "Agent de bout en bout",
		"redirect_uris": []string{e2eRedirectURI},
		"grant_types":   []string{"authorization_code", "refresh_token"},
		"scope":         e2eScope,
	})
	if registered.Status != http.StatusCreated {
		t.Fatalf("enregistrement : statut = %d, attendu 201 — corps : %s", registered.Status, registered.Body)
	}

	var client struct {
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal([]byte(registered.Body), &client); err != nil {
		t.Fatalf("réponse d'enregistrement illisible : %v — corps : %s", err, registered.Body)
	}

	verifier, challenge := pkcePairE2E(t)
	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {client.ClientID},
		"redirect_uri":          {e2eRedirectURI},
		"scope":                 {e2eScope},
		"state":                 {e2eState},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		// La spécification MCP demande au client d'envoyer toujours la ressource
		// visée : le serveur doit l'accepter, pas la rejeter.
		"resource": {app.server.URL + "/mcp"},
	}

	// 3. Sans session, la demande renvoie au formulaire de connexion.
	anonymous := app.get(t, "/oauth/authorize?"+params.Encode())
	if anonymous.Status != http.StatusSeeOther {
		t.Fatalf("demande anonyme : statut = %d, attendu 303 — corps : %s", anonymous.Status, anonymous.Body)
	}
	if !strings.HasPrefix(anonymous.Location, "/connexion") {
		t.Fatalf("redirection vers %q, attendu /connexion", anonymous.Location)
	}

	// 4. Connexion réelle, mot de passe haché en argon2id compris.
	login := app.postForm(t, "/connexion", url.Values{
		"email":        {e2eEmail},
		"mot_de_passe": {e2ePassword},
	})
	if login.Status != http.StatusSeeOther {
		t.Fatalf("connexion : statut = %d, attendu 303 — corps : %s", login.Status, login.Body)
	}

	// 5. Page de consentement.
	consent := app.get(t, "/oauth/authorize?"+params.Encode())
	if consent.Status != http.StatusOK {
		t.Fatalf("consentement : statut = %d, attendu 200 — corps : %s", consent.Status, consent.Body)
	}
	if !strings.Contains(consent.Body, "Agent de bout en bout") {
		t.Error("la page de consentement ne nomme pas le client")
	}

	// 6. L'utilisateur autorise, le serveur redirige avec le code.
	granted := app.postForm(t, "/oauth/authorize", url.Values{
		"requete":  {params.Encode()},
		"decision": {"autoriser"},
	})
	if granted.Status != http.StatusSeeOther {
		t.Fatalf("décision : statut = %d, attendu 303 — corps : %s", granted.Status, granted.Body)
	}

	redirected, err := url.Parse(granted.Location)
	if err != nil {
		t.Fatalf("cible de redirection illisible : %v", err)
	}
	if errCode := redirected.Query().Get("error"); errCode != "" {
		t.Fatalf("autorisation refusée : %s — %s", errCode, redirected.Query().Get("error_description"))
	}
	code := redirected.Query().Get("code")
	if code == "" {
		t.Fatalf("aucun code dans %q", granted.Location)
	}
	if got := redirected.Query().Get("state"); got != e2eState {
		t.Errorf("state = %q, attendu %q", got, e2eState)
	}
	if got := redirected.Query().Get("iss"); got != app.server.URL {
		t.Errorf("iss = %q, attendu %q", got, app.server.URL)
	}

	// 7. Échange du code avec le vérificateur PKCE.
	tokens := decodeTokenE2E(t, app.postForm(t, "/oauth/token", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {e2eRedirectURI},
		"client_id":     {client.ClientID},
		"code_verifier": {verifier},
	}))
	if tokens.AccessToken == "" {
		t.Fatalf("aucun jeton d'accès : %s — %s", tokens.Error, tokens.Description)
	}
	if tokens.RefreshToken == "" {
		t.Fatal("aucun jeton de rafraîchissement")
	}

	// 8. Le jeton se vérifie par le port du domaine — celui que le futur adapter
	//    MCP consommera, sans jamais savoir que fosite existe.
	verifier2 := app.site.TokenVerifier()

	actor, err := verifier2.VerifyToken(t.Context(), tokens.AccessToken)
	if err != nil {
		t.Fatalf("VerifyToken() a refusé un jeton fraîchement émis : %v", err)
	}
	if actor.UserID() != app.owner.ID {
		t.Errorf("UserID() = %q, attendu %q", actor.UserID(), app.owner.ID)
	}
	if !actor.Allows(identity.ScopeMCP) || !actor.Allows(identity.ScopeDevisRead) {
		t.Errorf("scopes de l'acteur = %v, attendu mcp et devis:read", actor.Scopes())
	}
	// Le propriétaire les détient ; le jeton ne les porte pas.
	if actor.Allows(identity.ScopeFinanceWrite) {
		t.Error("l'acteur du jeton a un scope qui n'a pas été consenti")
	}

	// 9. Rafraîchissement avec rotation.
	rotated := decodeTokenE2E(t, app.postForm(t, "/oauth/token", url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {tokens.RefreshToken},
		"client_id":     {client.ClientID},
	}))
	if rotated.AccessToken == "" {
		t.Fatalf("rafraîchissement : %s — %s", rotated.Error, rotated.Description)
	}
	if rotated.RefreshToken == tokens.RefreshToken {
		t.Error("le jeton de rafraîchissement n'a pas tourné")
	}
	if _, err := verifier2.VerifyToken(t.Context(), tokens.AccessToken); err == nil {
		t.Error("l'ancien jeton d'accès vaut encore après rotation")
	}
	if _, err := verifier2.VerifyToken(t.Context(), rotated.AccessToken); err != nil {
		t.Errorf("le nouveau jeton d'accès est refusé : %v", err)
	}

	// 10. Le rejeu du jeton déjà tourné fait tomber la famille.
	replayed := decodeTokenE2E(t, app.postForm(t, "/oauth/token", url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {tokens.RefreshToken},
		"client_id":     {client.ClientID},
	}))
	if replayed.AccessToken != "" {
		t.Fatal("un jeton de rafraîchissement déjà tourné a été accepté")
	}
	if _, err := verifier2.VerifyToken(t.Context(), rotated.AccessToken); err == nil {
		t.Error("le jeton en cours vaut encore après un rejeu de rafraîchissement")
	}

	// 11. Une nouvelle autorisation, puis sa révocation explicite.
	fresh := app.authorizeAgain(t, client.ClientID)
	if _, err := verifier2.VerifyToken(t.Context(), fresh.AccessToken); err != nil {
		t.Fatalf("le jeton de la seconde autorisation est refusé : %v", err)
	}

	revoked := app.postForm(t, "/oauth/revoke", url.Values{
		"token":     {fresh.AccessToken},
		"client_id": {client.ClientID},
	})
	if revoked.Status != http.StatusOK {
		t.Fatalf("révocation : statut = %d, attendu 200 — corps : %s", revoked.Status, revoked.Body)
	}
	if _, err := verifier2.VerifyToken(t.Context(), fresh.AccessToken); err == nil {
		t.Error("le jeton vaut encore après révocation")
	}

	// 12. Une désactivation de compte coupe les jetons restants, sans qu'il faille
	//     aller les révoquer un par un.
	final := app.authorizeAgain(t, client.ClientID)
	if _, err := verifier2.VerifyToken(t.Context(), final.AccessToken); err != nil {
		t.Fatalf("le jeton de la troisième autorisation est refusé : %v", err)
	}

	if err := app.accounts.Deactivate(t.Context(), app.owner.ID); err != nil {
		t.Fatalf("désactivation du compte : %v", err)
	}
	if _, err := verifier2.VerifyToken(t.Context(), final.AccessToken); err == nil {
		t.Error("le jeton d'un compte désactivé est encore accepté")
	}
}

// TestOAuthConcurrentRefreshEmitsOnePair présente le même jeton de
// rafraîchissement depuis plusieurs requêtes simultanées.
//
// # Ce que le test protège
//
// La rotation est ce qui donne son sens à la détection de rejeu : un jeton de
// rafraîchissement ne sert qu'une fois, et le représenter fait tomber toute la
// famille. Encore faut-il que « une fois » soit vrai sous concurrence. Si la
// désactivation de l'ancien jeton et l'émission du nouveau ne sont pas atomiques,
// deux requêtes lancées ensemble lisent toutes deux un jeton actif, passent
// toutes deux, et repartent chacune avec une paire neuve — la protection est
// alors contournable en envoyant deux fois la même requête au lieu d'une.
//
// # Ce qu'il vérifie
//
// Exactement une requête aboutit. Les autres reçoivent une erreur du protocole,
// pas une erreur serveur : la course est un cas prévu, et le client doit pouvoir
// la distinguer d'une panne pour savoir qu'il peut réessayer. Et il ne reste au
// plus qu'une paire de jetons neuve en circulation — au plus, parce qu'une
// concurrente arrivée après la rotation déclenche légitimement la détection de
// rejeu, qui révoque la famille entière, gagnante comprise.
func TestOAuthConcurrentRefreshEmitsOnePair(t *testing.T) {
	t.Parallel()

	const racers = 5

	app := startAvanti(t)
	app.login(t)
	clientID := app.registerClient(t, "Agent concurrent")

	tokens := app.authorizeAgain(t, clientID)
	if tokens.RefreshToken == "" {
		t.Fatal("aucun jeton de rafraîchissement à faire tourner")
	}

	var (
		wg      sync.WaitGroup
		start   = make(chan struct{})
		ready   sync.WaitGroup
		results = make([]response, racers)
	)

	for i := range racers {
		wg.Add(1)
		ready.Add(1)

		// Un client HTTP par concurrent. Avec un client partagé, le transport
		// sérialise l'établissement des connexions : les requêtes arrivent alors
		// les unes après les autres, et la fenêtre que ce test vise ne s'ouvre
		// jamais.
		racer := newRacerClient(t)

		go func() {
			defer wg.Done()

			warmUp(racer, app.server.URL, clientID)
			ready.Done()

			<-start
			results[i] = refreshConcurrently(racer, app.server.URL, clientID, tokens.RefreshToken)
		}()
	}

	// Le départ n'est donné qu'une fois toutes les goroutines en place et
	// préchauffées : c'est ce qui rapproche les requêtes assez pour que la fenêtre
	// existe. Mesuré : elles partent à moins de 500 µs les unes des autres, pour
	// un rafraîchissement qui en dure une vingtaine de milliers.
	ready.Wait()
	close(start)
	wg.Wait()

	issued := 0
	for i, result := range results {
		var decoded tokensE2E
		if err := json.Unmarshal([]byte(result.Body), &decoded); err != nil {
			t.Fatalf("réponse %d illisible : %v — corps : %s", i, err, result.Body)
		}

		if decoded.AccessToken != "" {
			issued++
			if result.Status != http.StatusOK {
				t.Errorf("réponse %d : statut = %d avec un jeton émis, attendu 200", i, result.Status)
			}
			continue
		}

		// Le perdant reçoit une erreur du protocole. Un 5xx dirait au client
		// « réessaie, le serveur est cassé » là où il faut lui dire « une autre
		// requête est passée avant toi ».
		if result.Status < 400 || result.Status >= 500 {
			t.Errorf("réponse %d : statut = %d, attendu une erreur 4xx — corps : %s", i, result.Status, result.Body)
		}
		if decoded.Error == "" {
			t.Errorf("réponse %d : aucune erreur OAuth nommée — corps : %s", i, result.Body)
		}
		if decoded.Error == "server_error" {
			t.Errorf("réponse %d : server_error, la course n'est pas une panne — corps : %s", i, result.Body)
		}
	}

	if issued != 1 {
		t.Fatalf("%d paires de jetons émises, attendu exactement 1", issued)
	}

	// L'état de la base tranche ce que les réponses ne disent pas : une rotation
	// non atomique laisserait autant de jetons neufs que de requêtes, actifs ou
	// non, puisque chaque transaction perdante doit être annulée en entier.
	if total := app.countTokens(t, "refresh_token"); total > 2 {
		t.Errorf("%d jetons de rafraîchissement en base, attendu au plus 2 (le présenté et son unique remplaçant)", total)
	}
	if active := app.countActiveTokens(t, "refresh_token"); active > 1 {
		t.Errorf("%d jetons de rafraîchissement encore valides, attendu au plus 1", active)
	}
}

// newRacerClient rend un client HTTP indépendant, sans cookie : les points de
// terminaison machine n'ont pas de session, et un client par concurrent évite
// que le transport partagé ne sérialise leurs requêtes.
func newRacerClient(t *testing.T) *http.Client {
	t.Helper()

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: 30 * time.Second,
	}
	t.Cleanup(client.CloseIdleConnections)

	return client
}

// warmUp joue un rafraîchissement voué à l'échec avant le départ de la course.
//
// Deux coûts sont ainsi payés d'avance, et tous deux dépassent de loin la
// fenêtre que ce test vise : l'établissement de la connexion HTTP, et surtout
// celui d'une connexion PostgreSQL — le pool les ouvre à la demande et une à la
// fois, de sorte qu'une course lancée à froid se transforme en file d'attente
// devant le pool. Le jeton présenté ici est du charabia : il emprunte le même
// chemin (recherche du client, puis du jeton) et repart en invalid_grant sans
// rien modifier.
func warmUp(client *http.Client, baseURL, clientID string) {
	_ = refreshConcurrently(client, baseURL, clientID, "ory_rt_jeton-de-prechauffage.sans-valeur")
}

// refreshConcurrently échange un jeton de rafraîchissement sans passer par
// [avantiInstance.do] : les assertions de test ne sont pas sûres hors de la
// goroutine du test, et une erreur de transport doit être rapportée par la
// réponse plutôt que par un t.Fatalf lancé de côté.
func refreshConcurrently(client *http.Client, baseURL, clientID, refreshToken string) response {
	fields := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		baseURL+"/oauth/token", strings.NewReader(fields.Encode()))
	if err != nil {
		return response{Status: 0, Body: fmt.Sprintf(`{"error":"requete_non_construite","error_description":%q}`, err.Error())}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	result, err := client.Do(req)
	if err != nil {
		return response{Status: 0, Body: fmt.Sprintf(`{"error":"transport","error_description":%q}`, err.Error())}
	}
	defer func() { _ = result.Body.Close() }() //nolint:errcheck // la réponse est déjà lue ; une fermeture ratée n'apprendrait rien au test.

	body, err := io.ReadAll(result.Body)
	if err != nil {
		return response{Status: result.StatusCode, Body: fmt.Sprintf(`{"error":"lecture","error_description":%q}`, err.Error())}
	}

	return response{Status: result.StatusCode, Body: string(body)}
}

// countTokens compte les enregistrements d'une nature, actifs ou non.
func (a *avantiInstance) countTokens(t *testing.T, kind string) int {
	t.Helper()

	return a.countRows(t, "SELECT count(*) FROM oauth_tokens WHERE kind = $1", kind)
}

// countActiveTokens compte les enregistrements encore valides d'une nature.
func (a *avantiInstance) countActiveTokens(t *testing.T, kind string) int {
	t.Helper()

	return a.countRows(t, "SELECT count(*) FROM oauth_tokens WHERE kind = $1 AND active", kind)
}

func (a *avantiInstance) countRows(t *testing.T, query, kind string) int {
	t.Helper()

	var total int
	if err := a.pool.QueryRow(t.Context(), query, kind).Scan(&total); err != nil {
		t.Fatalf("comptage des enregistrements OAuth : %v", err)
	}

	return total
}

// authorizeAgain rejoue une autorisation complète pour le même client, session
// web déjà ouverte.
func (a *avantiInstance) authorizeAgain(t *testing.T, clientID string) tokensE2E {
	t.Helper()

	verifier, challenge := pkcePairE2E(t)
	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {e2eRedirectURI},
		"scope":                 {e2eScope},
		"state":                 {e2eState},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}

	granted := a.postForm(t, "/oauth/authorize", url.Values{
		"requete":  {params.Encode()},
		"decision": {"autoriser"},
	})
	if granted.Status != http.StatusSeeOther {
		t.Fatalf("nouvelle autorisation : statut = %d — corps : %s", granted.Status, granted.Body)
	}

	redirected, err := url.Parse(granted.Location)
	if err != nil {
		t.Fatalf("cible de redirection illisible : %v", err)
	}
	code := redirected.Query().Get("code")
	if code == "" {
		t.Fatalf("aucun code dans %q", granted.Location)
	}

	tokens := decodeTokenE2E(t, a.postForm(t, "/oauth/token", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {e2eRedirectURI},
		"client_id":     {clientID},
		"code_verifier": {verifier},
	}))
	if tokens.AccessToken == "" {
		t.Fatalf("échange : %s — %s", tokens.Error, tokens.Description)
	}

	return tokens
}

// login ouvre la session web du propriétaire.
func (a *avantiInstance) login(t *testing.T) {
	t.Helper()

	result := a.postForm(t, "/connexion", url.Values{
		"email":        {e2eEmail},
		"mot_de_passe": {e2ePassword},
	})
	if result.Status != http.StatusSeeOther {
		t.Fatalf("connexion : statut = %d, attendu 303 — corps : %s", result.Status, result.Body)
	}
}

// registerClient enregistre un client par l'enregistrement dynamique et rend son
// identifiant.
func (a *avantiInstance) registerClient(t *testing.T, name string) string {
	t.Helper()

	result := a.postJSON(t, "/oauth/register", map[string]any{
		"client_name":   name,
		"redirect_uris": []string{e2eRedirectURI},
		"grant_types":   []string{"authorization_code", "refresh_token"},
		"scope":         e2eScope,
	})
	if result.Status != http.StatusCreated {
		t.Fatalf("enregistrement : statut = %d, attendu 201 — corps : %s", result.Status, result.Body)
	}

	var registered struct {
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal([]byte(result.Body), &registered); err != nil {
		t.Fatalf("réponse d'enregistrement illisible : %v — corps : %s", err, result.Body)
	}

	return registered.ClientID
}

// tokensE2E est la réponse du point de terminaison de jeton.
type tokensE2E struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
}

func decodeTokenE2E(t *testing.T, result response) tokensE2E {
	t.Helper()

	var tokens tokensE2E
	if err := json.Unmarshal([]byte(result.Body), &tokens); err != nil {
		t.Fatalf("réponse de jeton illisible : %v — corps : %s", err, result.Body)
	}

	return tokens
}

// pkcePairE2E tire un vérificateur et calcule son défi S256.
func pkcePairE2E(t *testing.T) (verifier, challenge string) {
	t.Helper()

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("tirage du vérificateur PKCE : %v", err)
	}

	verifier = base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))

	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}
