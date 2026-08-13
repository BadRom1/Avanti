package web_test

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ory/fosite"

	"github.com/Romain-Badino/Avanti/internal/identity"
)

// Chemins du serveur d'autorisation, répétés ici à dessein.
//
// Les tests les écrivent en clair plutôt que d'importer les constantes du
// paquet : ce sont des adresses publiques, qu'un client extérieur a codées en
// dur chez lui. Les renommer casse ses intégrations, et un test qui suivrait
// automatiquement le renommage ne le dirait pas.
const (
	metadataPath  = "/.well-known/oauth-authorization-server"
	authorizePath = "/oauth/authorize"
	tokenPath     = "/oauth/token"
	revokePath    = "/oauth/revoke"
	registerPath  = "/oauth/register"
)

// redirectURITest est l'adresse de retour des clients de test. En https, comme
// l'exige l'enregistrement pour tout ce qui n'est pas la boucle locale.
const redirectURITest = "https://agent.exemple.fr/callback"

// stateTest est assez long pour passer l'exigence d'entropie de fosite sur le
// paramètre state.
const stateTest = "etat-de-test-suffisamment-long-pour-fosite"

// allScopesTest est le jeu de scopes que demande un client qui veut tout.
const allScopesTest = "mcp devis:read devis:write planning:read planning:write finance:read finance:write document:read document:write"

// --- Outillage --------------------------------------------------------------

// registeredClient est un client OAuth enregistré, tel qu'un test s'en sert.
type registeredClient struct {
	ID          string
	RedirectURI string
}

// tokenResponse est la réponse du point de terminaison de jeton.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
}

// postJSON soumet un corps JSON, comme le fait un client qui s'enregistre.
func (n *browser) postJSON(target string, payload any) httpResult {
	n.t.Helper()

	body, err := json.Marshal(payload)
	if err != nil {
		n.t.Fatalf("sérialisation du corps JSON : %v", err)
	}

	req := httptest.NewRequestWithContext(n.t.Context(), http.MethodPost, target, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	return n.send(req)
}

// register enregistre un client et échoue le test si l'enregistrement rate.
func (n *browser) register(scope string) registeredClient {
	n.t.Helper()

	result := n.postJSON(registerPath, map[string]any{
		"client_name":   "Agent de test",
		"redirect_uris": []string{redirectURITest},
		"grant_types":   []string{"authorization_code", "refresh_token"},
		"scope":         scope,
	})
	if result.Status != http.StatusCreated {
		n.t.Fatalf("enregistrement dynamique : statut = %d, attendu 201 — corps : %s", result.Status, result.Body)
	}

	var registered struct {
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal([]byte(result.Body), &registered); err != nil {
		n.t.Fatalf("réponse d'enregistrement illisible : %v — corps : %s", err, result.Body)
	}
	if registered.ClientID == "" {
		n.t.Fatalf("réponse d'enregistrement sans client_id : %s", result.Body)
	}

	return registeredClient{ID: registered.ClientID, RedirectURI: redirectURITest}
}

// pkcePair tire un vérificateur et calcule son défi S256, exactement comme le
// ferait un client conforme.
func pkcePair(t *testing.T) (verifier, challenge string) {
	t.Helper()

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("tirage du vérificateur PKCE : %v", err)
	}

	verifier = base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))

	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// authorizeQuery construit les paramètres d'une demande d'autorisation.
func authorizeQuery(client registeredClient, challenge, method, scope string) url.Values {
	params := url.Values{
		"response_type": {"code"},
		"client_id":     {client.ID},
		"redirect_uri":  {client.RedirectURI},
		"scope":         {scope},
		"state":         {stateTest},
	}
	if challenge != "" {
		params.Set("code_challenge", challenge)
	}
	if method != "" {
		params.Set("code_challenge_method", method)
	}

	return params
}

// hiddenRequestPattern extrait le champ caché qui reporte la demande
// d'autorisation d'une requête à la suivante.
var hiddenRequestPattern = regexp.MustCompile(`name="requete" value="([^"]*)"`)

// plainText déséchappe une page avant d'y chercher du texte.
//
// html/template écrit « qu&#39;agent » là où le catalogue dit « qu'agent » :
// chercher la chaîne française dans le HTML brut échouerait sur chaque
// apostrophe. Déséchapper est ce qui permet aux assertions de citer le texte tel
// qu'un lecteur le voit — et c'est aussi la preuve que l'échappement a bien eu
// lieu.
func plainText(body string) string {
	return html.UnescapeString(body)
}

// consentRequest lit dans la page de consentement la demande qu'elle reporte.
//
// L'extraire du HTML plutôt que de réutiliser la chaîne envoyée est ce qui
// vérifie que la page la transmet réellement — un champ caché oublié rendrait le
// consentement inopérant sans qu'aucune autre assertion ne le voie.
func consentRequest(t *testing.T, body string) string {
	t.Helper()

	match := hiddenRequestPattern.FindStringSubmatch(body)
	if match == nil {
		t.Fatalf("la page de consentement ne reporte pas la demande — corps : %s", body)
	}

	return html.UnescapeString(match[1])
}

// consent joue la page de consentement et rend la réponse à la décision.
func (n *browser) consent(params url.Values, decision string) httpResult {
	n.t.Helper()

	page := n.get(authorizePath + "?" + params.Encode())
	if page.Status != http.StatusOK {
		n.t.Fatalf("page de consentement : statut = %d, attendu 200 — corps : %s", page.Status, page.Body)
	}

	return n.post(authorizePath, url.Values{
		"requete":  {consentRequest(n.t, page.Body)},
		"decision": {decision},
	})
}

// codeFrom extrait le code d'autorisation de la redirection vers le client.
func codeFrom(t *testing.T, result httpResult) string {
	t.Helper()

	if result.Status != http.StatusSeeOther {
		t.Fatalf("redirection d'autorisation : statut = %d, attendu 303 — corps : %s", result.Status, result.Body)
	}

	target, err := url.Parse(result.Location())
	if err != nil {
		t.Fatalf("cible de redirection illisible : %v", err)
	}

	query := target.Query()
	if errCode := query.Get("error"); errCode != "" {
		t.Fatalf("le serveur a refusé l'autorisation : %s — %s", errCode, query.Get("error_description"))
	}

	code := query.Get("code")
	if code == "" {
		t.Fatalf("aucun code dans la redirection : %s", result.Location())
	}

	return code
}

// exchange échange un code contre des jetons.
func (n *browser) exchange(client registeredClient, code, verifier string) tokenResponse {
	n.t.Helper()

	fields := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {client.RedirectURI},
		"client_id":    {client.ID},
	}
	if verifier != "" {
		fields.Set("code_verifier", verifier)
	}

	return decodeToken(n.t, n.post(tokenPath, fields))
}

// refresh échange un jeton de rafraîchissement.
func (n *browser) refresh(client registeredClient, refreshToken string) tokenResponse {
	n.t.Helper()

	return decodeToken(n.t, n.post(tokenPath, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {client.ID},
	}))
}

func decodeToken(t *testing.T, result httpResult) tokenResponse {
	t.Helper()

	var response tokenResponse
	if err := json.Unmarshal([]byte(result.Body), &response); err != nil {
		t.Fatalf("réponse de jeton illisible : %v — corps : %s", err, result.Body)
	}

	return response
}

// authorized joue le parcours complet jusqu'aux jetons, pour les tests qui ont
// besoin d'un jeton valide sans réécrire le parcours.
func (s *site) authorized(t *testing.T, scope string) (*browser, registeredClient, tokenResponse) {
	t.Helper()

	n := newBrowser(t, s.handler)
	client := n.register(scope)
	n.login(ownerEmail)

	verifier, challenge := pkcePair(t)
	params := authorizeQuery(client, challenge, "S256", scope)

	tokens := n.exchange(client, codeFrom(t, n.consent(params, "autoriser")), verifier)
	if tokens.AccessToken == "" {
		t.Fatalf("aucun jeton d'accès émis : %s — %s", tokens.Error, tokens.Description)
	}

	return n, client, tokens
}

// --- Métadonnées ------------------------------------------------------------

// TestOAuthMetadataDocument vérifie le document de découverte.
//
// C'est la première chose qu'un client lit, et la seule qu'il lise avant de
// décider s'il peut se connecter du tout : la spécification MCP lui demande de
// renoncer si code_challenge_methods_supported est absent.
func TestOAuthMetadataDocument(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	n := newBrowser(t, s.handler)

	result := n.get(metadataPath)
	if result.Status != http.StatusOK {
		t.Fatalf("statut = %d, attendu 200 — corps : %s", result.Status, result.Body)
	}
	if contentType := result.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Errorf("Content-Type = %q, attendu application/json", contentType)
	}

	var document struct {
		Issuer                 string   `json:"issuer"`
		AuthorizationEndpoint  string   `json:"authorization_endpoint"`
		TokenEndpoint          string   `json:"token_endpoint"`
		RegistrationEndpoint   string   `json:"registration_endpoint"`
		RevocationEndpoint     string   `json:"revocation_endpoint"`
		ScopesSupported        []string `json:"scopes_supported"`
		ResponseTypes          []string `json:"response_types_supported"`
		GrantTypes             []string `json:"grant_types_supported"`
		AuthMethods            []string `json:"token_endpoint_auth_methods_supported"`
		ChallengeMethods       []string `json:"code_challenge_methods_supported"`
		IssuerParameterSupport bool     `json:"authorization_response_iss_parameter_supported"`
	}
	if err := json.Unmarshal([]byte(result.Body), &document); err != nil {
		t.Fatalf("document illisible : %v — corps : %s", err, result.Body)
	}

	if document.Issuer != baseURLTest {
		t.Errorf("issuer = %q, attendu %q", document.Issuer, baseURLTest)
	}
	for _, endpoint := range []struct {
		name, got, want string
	}{
		{"authorization_endpoint", document.AuthorizationEndpoint, baseURLTest + authorizePath},
		{"token_endpoint", document.TokenEndpoint, baseURLTest + tokenPath},
		{"registration_endpoint", document.RegistrationEndpoint, baseURLTest + registerPath},
		{"revocation_endpoint", document.RevocationEndpoint, baseURLTest + revokePath},
	} {
		if endpoint.got != endpoint.want {
			t.Errorf("%s = %q, attendu %q", endpoint.name, endpoint.got, endpoint.want)
		}
	}

	// S256 seul : annoncer « plain » ferait choisir à un client conforme la
	// transformation que ce serveur refuse.
	if len(document.ChallengeMethods) != 1 || document.ChallengeMethods[0] != "S256" {
		t.Errorf("code_challenge_methods_supported = %v, attendu [S256] seul", document.ChallengeMethods)
	}
	if len(document.AuthMethods) != 1 || document.AuthMethods[0] != "none" {
		t.Errorf("token_endpoint_auth_methods_supported = %v, attendu [none] seul", document.AuthMethods)
	}
	if !document.IssuerParameterSupport {
		t.Error("authorization_response_iss_parameter_supported = false, alors que le serveur émet iss")
	}

	// Les flux retirés par OAuth 2.1 ne doivent apparaître nulle part.
	for _, forbidden := range []string{"implicit", "password", "client_credentials"} {
		for _, grant := range document.GrantTypes {
			if grant == forbidden {
				t.Errorf("grant_types_supported contient %q, qu'OAuth 2.1 interdit", forbidden)
			}
		}
		for _, responseType := range document.ResponseTypes {
			if responseType == "token" {
				t.Error("response_types_supported contient \"token\", qui est le flux implicite")
			}
		}
	}

	// Le scope mcp doit être annoncé : sans lui, aucun client ne saurait le
	// demander, et il est obligatoire.
	if !contains(document.ScopesSupported, identity.ScopeMCP.String()) {
		t.Errorf("scopes_supported = %v, doit contenir %q", document.ScopesSupported, identity.ScopeMCP)
	}
}

// --- Parcours complet -------------------------------------------------------

// TestOAuthFullFlow joue le parcours entier, de l'enregistrement du client à la
// révocation du jeton.
//
// C'est le test qui compte le plus du lot : chaque étape dépend de la
// précédente, et un maillon cassé n'importe où le fait échouer. Il est écrit
// linéairement, dans l'ordre où les choses arrivent en vrai.
func TestOAuthFullFlow(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	n := newBrowser(t, s.handler)

	// 1. Le client s'enregistre. Il n'a besoin d'aucun compte pour cela : c'est le
	//    modèle de MCP, où un agent découvre le serveur avant de connaître qui que
	//    ce soit.
	client := n.register(allScopesTest)

	verifier, challenge := pkcePair(t)
	params := authorizeQuery(client, challenge, "S256", allScopesTest)

	// 2. Sans session, la demande d'autorisation renvoie au formulaire de
	//    connexion, en gardant la demande pour y revenir.
	anonymous := n.get(authorizePath + "?" + params.Encode())
	if anonymous.Status != http.StatusSeeOther {
		t.Fatalf("demande anonyme : statut = %d, attendu 303 — corps : %s", anonymous.Status, anonymous.Body)
	}
	next := redirectTarget(t, anonymous.Location())
	if !strings.HasPrefix(next, authorizePath) {
		t.Fatalf("la demande d'autorisation n'est pas reportée après connexion : %q", anonymous.Location())
	}

	// 3. L'utilisateur se connecte.
	n.login(ownerEmail)

	// 4. La page de consentement nomme le client et énumère les droits.
	page := n.get(authorizePath + "?" + params.Encode())
	if page.Status != http.StatusOK {
		t.Fatalf("page de consentement : statut = %d, attendu 200 — corps : %s", page.Status, page.Body)
	}
	if !strings.Contains(plainText(page.Body), "Agent de test") {
		t.Error("la page de consentement ne nomme pas le client")
	}
	if !strings.Contains(plainText(page.Body), "Se connecter en tant qu'agent IA") {
		t.Error("la page de consentement n'énumère pas les droits demandés en clair")
	}

	// 5. L'utilisateur autorise. Le serveur redirige vers le client, avec le code,
	//    l'état inchangé et son propre identifiant (RFC 9207).
	granted := n.post(authorizePath, url.Values{
		"requete":  {consentRequest(t, page.Body)},
		"decision": {"autoriser"},
	})
	code := codeFrom(t, granted)

	redirected, err := url.Parse(granted.Location())
	if err != nil {
		t.Fatalf("cible de redirection illisible : %v", err)
	}
	if got := redirected.Query().Get("state"); got != stateTest {
		t.Errorf("state = %q, attendu %q — un état altéré casse la protection du client", got, stateTest)
	}
	if got := redirected.Query().Get("iss"); got != baseURLTest {
		t.Errorf("iss = %q, attendu %q", got, baseURLTest)
	}

	// 6. Le client échange le code, en prouvant qu'il détient le vérificateur.
	tokens := n.exchange(client, code, verifier)
	if tokens.AccessToken == "" {
		t.Fatalf("aucun jeton d'accès : %s — %s", tokens.Error, tokens.Description)
	}
	if tokens.RefreshToken == "" {
		t.Fatal("aucun jeton de rafraîchissement : un client public doit en recevoir un, sans avoir à demander offline_access")
	}
	if tokens.TokenType != "bearer" {
		t.Errorf("token_type = %q, attendu \"bearer\"", tokens.TokenType)
	}

	// 7. Le jeton se vérifie, et rend un acteur aux droits attendus.
	actor, err := s.handler.TokenVerifier().VerifyToken(t.Context(), tokens.AccessToken)
	if err != nil {
		t.Fatalf("VerifyToken() a refusé un jeton fraîchement émis : %v", err)
	}
	if !actor.Allows(identity.ScopeMCP) {
		t.Error("l'acteur du jeton n'a pas le scope mcp")
	}
	if actor.Anonymous() {
		t.Error("l'acteur du jeton est anonyme")
	}

	// 8. Le rafraîchissement rend un couple neuf, et le jeton présenté cesse de
	//    valoir — c'est la rotation qu'OAuth 2.1 exige d'un client public.
	rotated := n.refresh(client, tokens.RefreshToken)
	if rotated.AccessToken == "" {
		t.Fatalf("le rafraîchissement n'a rien rendu : %s — %s", rotated.Error, rotated.Description)
	}
	if rotated.RefreshToken == tokens.RefreshToken {
		t.Error("le jeton de rafraîchissement n'a pas tourné : il est rendu à l'identique")
	}
	if rotated.AccessToken == tokens.AccessToken {
		t.Error("le jeton d'accès n'a pas changé au rafraîchissement")
	}

	// L'ancien jeton d'accès est mort avec la rotation.
	if _, err := s.handler.TokenVerifier().VerifyToken(t.Context(), tokens.AccessToken); err == nil {
		t.Error("l'ancien jeton d'accès vaut encore après rotation")
	}
	// Le nouveau vit.
	if _, err := s.handler.TokenVerifier().VerifyToken(t.Context(), rotated.AccessToken); err != nil {
		t.Errorf("le jeton d'accès issu du rafraîchissement est refusé : %v", err)
	}

	// 9. La révocation éteint toute la famille.
	revoked := n.post(revokePath, url.Values{
		"token":     {rotated.AccessToken},
		"client_id": {client.ID},
	})
	if revoked.Status != http.StatusOK {
		t.Fatalf("révocation : statut = %d, attendu 200 — corps : %s", revoked.Status, revoked.Body)
	}

	if _, err := s.handler.TokenVerifier().VerifyToken(t.Context(), rotated.AccessToken); err == nil {
		t.Error("le jeton d'accès vaut encore après révocation")
	}
	if after := n.refresh(client, rotated.RefreshToken); after.AccessToken != "" {
		t.Error("le jeton de rafraîchissement vaut encore après révocation du jeton d'accès de la même famille")
	}
}

// redirectTarget extrait le paramètre « suite » d'une redirection vers le
// formulaire de connexion.
func redirectTarget(t *testing.T, location string) string {
	t.Helper()

	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatalf("redirection illisible : %v", err)
	}

	return parsed.Query().Get("suite")
}

func contains(haystack []string, needle string) bool {
	for _, candidate := range haystack {
		if candidate == needle {
			return true
		}
	}
	return false
}

// --- Refus ------------------------------------------------------------------

// TestOAuthAuthorizeRejects couvre les demandes d'autorisation que le serveur
// doit refuser.
//
// Le refus attendu est décrit par le code d'erreur OAuth, pas par un statut
// HTTP : quand l'adresse de retour est valide, fosite redirige l'erreur vers le
// client ; quand elle ne l'est pas, il l'affiche. Les deux sont corrects, et
// c'est justement ce que le test vérifie — une erreur redirigée vers une adresse
// non vérifiée serait une redirection ouverte.
func TestOAuthAuthorizeRejects(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		// mutate altère une demande par ailleurs valide.
		mutate func(params url.Values, client registeredClient)
		// wantError est le code d'erreur OAuth attendu, vide si l'erreur ne peut
		// pas être redirigée vers le client.
		wantError string
		// wantRedirect dit si l'erreur doit repartir vers le client.
		wantRedirect bool
	}{
		{
			name:         "PKCE absent",
			mutate:       func(p url.Values, _ registeredClient) { p.Del("code_challenge") },
			wantError:    "invalid_request",
			wantRedirect: true,
		},
		{
			name:         "PKCE en clair",
			mutate:       func(p url.Values, _ registeredClient) { p.Set("code_challenge_method", "plain") },
			wantError:    "invalid_request",
			wantRedirect: true,
		},
		{
			name:         "méthode de défi inconnue",
			mutate:       func(p url.Values, _ registeredClient) { p.Set("code_challenge_method", "S512") },
			wantError:    "invalid_request",
			wantRedirect: true,
		},
		{
			name:         "scope mcp absent",
			mutate:       func(p url.Values, _ registeredClient) { p.Set("scope", "devis:read") },
			wantError:    "invalid_scope",
			wantRedirect: true,
		},
		{
			name:         "ressource étrangère",
			mutate:       func(p url.Values, _ registeredClient) { p.Set("resource", "https://ailleurs.example/mcp") },
			wantError:    "invalid_target",
			wantRedirect: true,
		},
		{
			name:         "flux implicite",
			mutate:       func(p url.Values, _ registeredClient) { p.Set("response_type", "token") },
			wantError:    "unsupported_response_type",
			wantRedirect: true,
		},
		{
			// Une adresse de retour non enregistrée ne peut pas recevoir l'erreur :
			// la lui envoyer reviendrait à confirmer à un attaquant qu'il a visé le
			// bon serveur, et à en faire un relais de redirection.
			name:         "adresse de retour non enregistrée",
			mutate:       func(p url.Values, _ registeredClient) { p.Set("redirect_uri", "https://attaquant.example/vol") },
			wantRedirect: false,
		},
		{
			name:         "client inconnu",
			mutate:       func(p url.Values, _ registeredClient) { p.Set("client_id", "client-qui-nexiste-pas") },
			wantRedirect: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := newSite(t)
			n := newBrowser(t, s.handler)

			client := n.register(allScopesTest)
			n.login(ownerEmail)

			_, challenge := pkcePair(t)
			params := authorizeQuery(client, challenge, "S256", allScopesTest)
			tc.mutate(params, client)

			result := n.get(authorizePath + "?" + params.Encode())

			if !tc.wantRedirect {
				if result.Status == http.StatusSeeOther {
					t.Fatalf("l'erreur a été redirigée vers %q, ce qui en ferait une redirection ouverte", result.Location())
				}
				if result.Status < 400 {
					t.Fatalf("statut = %d, une erreur était attendue — corps : %s", result.Status, result.Body)
				}
				return
			}

			if result.Status != http.StatusSeeOther {
				t.Fatalf("statut = %d, attendu 303 vers le client — corps : %s", result.Status, result.Body)
			}

			target, err := url.Parse(result.Location())
			if err != nil {
				t.Fatalf("cible de redirection illisible : %v", err)
			}
			if !strings.HasPrefix(result.Location(), client.RedirectURI) {
				t.Fatalf("l'erreur part vers %q, attendu l'adresse enregistrée %q", result.Location(), client.RedirectURI)
			}
			if got := target.Query().Get("error"); got != tc.wantError {
				t.Errorf("error = %q, attendu %q — description : %q",
					got, tc.wantError, target.Query().Get("error_description"))
			}
			if target.Query().Get("code") != "" {
				t.Error("un code a été émis malgré le refus")
			}
		})
	}
}

// TestOAuthConsentRefusalRedirectsAccessDenied vérifie que le refus explicite de
// l'utilisateur est une réponse du protocole, et non une page d'erreur.
//
// Le client doit apprendre que rien ne sera connecté, sans quoi il attendrait un
// retour qui ne viendrait jamais.
func TestOAuthConsentRefusalRedirectsAccessDenied(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	n := newBrowser(t, s.handler)

	client := n.register(allScopesTest)
	n.login(ownerEmail)

	_, challenge := pkcePair(t)
	params := authorizeQuery(client, challenge, "S256", allScopesTest)

	result := n.consent(params, "refuser")
	if result.Status != http.StatusSeeOther {
		t.Fatalf("statut = %d, attendu 303 — corps : %s", result.Status, result.Body)
	}

	target, err := url.Parse(result.Location())
	if err != nil {
		t.Fatalf("cible de redirection illisible : %v", err)
	}
	if got := target.Query().Get("error"); got != "access_denied" {
		t.Errorf("error = %q, attendu access_denied", got)
	}
	if target.Query().Get("code") != "" {
		t.Error("un code a été émis malgré le refus de l'utilisateur")
	}
}

// TestOAuthCollaboratorRefused vérifie qu'un compte sans accès agent IA est
// refusé, et qu'il l'apprend en français.
func TestOAuthCollaboratorRefused(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	n := newBrowser(t, s.handler)

	client := n.register(allScopesTest)
	n.login(collaboratorEmail)

	_, challenge := pkcePair(t)
	params := authorizeQuery(client, challenge, "S256", allScopesTest)

	result := n.get(authorizePath + "?" + params.Encode())
	if result.Status != http.StatusForbidden {
		t.Fatalf("statut = %d, attendu 403 — corps : %s", result.Status, result.Body)
	}
	if !strings.Contains(plainText(result.Body), "n'ouvre pas l'accès agent IA") {
		t.Errorf("la page n'explique pas le refus en français — corps : %s", result.Body)
	}

	// Le refus tient aussi quand la décision est soumise directement, sans passer
	// par la page : c'est le contrôle qui compte, pas l'affichage.
	forced := n.post(authorizePath, url.Values{
		"requete":  {params.Encode()},
		"decision": {"autoriser"},
	})
	if forced.Status != http.StatusForbidden {
		t.Fatalf("décision forcée : statut = %d, attendu 403 — corps : %s", forced.Status, forced.Body)
	}
	if s.oauth.countActive(memKindAuthorizationCode) != 0 {
		t.Error("un code d'autorisation a été émis pour un compte sans accès agent IA")
	}
}

// TestOAuthTokenRejects couvre les échanges de code que le serveur doit refuser.
func TestOAuthTokenRejects(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		// mutate altère la demande d'échange.
		mutate    func(fields url.Values, verifier string)
		wantError string
	}{
		{
			name:      "vérificateur PKCE absent",
			mutate:    func(f url.Values, _ string) { f.Del("code_verifier") },
			wantError: "invalid_grant",
		},
		{
			name:      "vérificateur PKCE faux",
			mutate:    func(f url.Values, _ string) { f.Set("code_verifier", strings.Repeat("z", 43)) },
			wantError: "invalid_grant",
		},
		{
			name: "vérificateur PKCE d'une autre demande",
			mutate: func(f url.Values, _ string) {
				other := make([]byte, 32)
				if _, err := rand.Read(other); err != nil {
					panic(err)
				}
				f.Set("code_verifier", base64.RawURLEncoding.EncodeToString(other))
			},
			wantError: "invalid_grant",
		},
		{
			name:      "code inconnu",
			mutate:    func(f url.Values, _ string) { f.Set("code", "code-invente-de-toutes-pieces") },
			wantError: "invalid_grant",
		},
		{
			name:      "adresse de retour différente de celle de la demande",
			mutate:    func(f url.Values, _ string) { f.Set("redirect_uri", "https://agent.exemple.fr/autre") },
			wantError: "invalid_grant",
		},
		{
			name:      "client différent de celui du code",
			mutate:    func(f url.Values, _ string) { f.Set("client_id", "un-autre-client") },
			wantError: "invalid_client",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := newSite(t)
			n := newBrowser(t, s.handler)

			client := n.register(allScopesTest)
			n.login(ownerEmail)

			verifier, challenge := pkcePair(t)
			params := authorizeQuery(client, challenge, "S256", allScopesTest)
			code := codeFrom(t, n.consent(params, "autoriser"))

			fields := url.Values{
				"grant_type":    {"authorization_code"},
				"code":          {code},
				"redirect_uri":  {client.RedirectURI},
				"client_id":     {client.ID},
				"code_verifier": {verifier},
			}
			tc.mutate(fields, verifier)

			response := decodeToken(t, n.post(tokenPath, fields))
			if response.AccessToken != "" {
				t.Fatalf("un jeton a été émis malgré %s", tc.name)
			}
			if response.Error != tc.wantError {
				t.Errorf("error = %q, attendu %q — description : %q",
					response.Error, tc.wantError, response.Description)
			}
		})
	}
}

// TestOAuthConsentShowsClientIdentity vérifie ce que la page de consentement dit
// du demandeur, au-delà du nom qu'il s'est donné.
//
// L'enregistrement dynamique étant ouvert, le nom ne prouve rien : n'importe qui
// peut s'enregistrer sous celui d'un agent connu. Ce qui distingue vraiment deux
// clients homonymes est ce que le serveur constate — l'identifiant qu'il a
// attribué, la date d'enregistrement, et le fait d'avoir déjà vu ce couple
// compte/client. Sans ces trois repères, la page présente une usurpation
// exactement comme elle présenterait l'original.
func TestOAuthConsentShowsClientIdentity(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	n := newBrowser(t, s.handler)

	client := n.register(allScopesTest)
	n.login(ownerEmail)

	verifier, challenge := pkcePair(t)
	params := authorizeQuery(client, challenge, "S256", allScopesTest)

	first := n.get(authorizePath + "?" + params.Encode())
	if first.Status != http.StatusOK {
		t.Fatalf("page de consentement : statut = %d, attendu 200 — corps : %s", first.Status, first.Body)
	}

	page := plainText(first.Body)
	if !strings.Contains(page, client.ID) {
		t.Errorf("la page n'affiche pas l'identifiant du client %q — corps : %s", client.ID, page)
	}
	if today := time.Now().Local().Format("02/01/2006"); !strings.Contains(page, today) {
		t.Errorf("la page n'affiche pas la date d'enregistrement %q", today)
	}
	if !strings.Contains(page, "Première autorisation de ce client") {
		t.Error("la page n'annonce pas qu'il s'agit d'une première autorisation")
	}
	if strings.Contains(page, "Vous avez déjà autorisé ce client") {
		t.Error("la page annonce un client déjà autorisé alors qu'il ne l'a jamais été")
	}

	// Le consentement est donné pour de bon : c'est lui qui doit être retenu.
	if tokens := n.exchange(client, codeFrom(t, n.consent(params, "autoriser")), verifier); tokens.AccessToken == "" {
		t.Fatalf("aucun jeton émis : %s — %s", tokens.Error, tokens.Description)
	}

	_, secondChallenge := pkcePair(t)
	secondParams := authorizeQuery(client, secondChallenge, "S256", allScopesTest)

	second := n.get(authorizePath + "?" + secondParams.Encode())
	if second.Status != http.StatusOK {
		t.Fatalf("seconde page de consentement : statut = %d, attendu 200 — corps : %s", second.Status, second.Body)
	}

	page = plainText(second.Body)
	if !strings.Contains(page, "Vous avez déjà autorisé ce client") {
		t.Error("la page ne reconnaît pas un client déjà autorisé")
	}
	if strings.Contains(page, "Première autorisation de ce client") {
		t.Error("la page annonce une première autorisation pour un client déjà autorisé")
	}
}

// TestOAuthConsentRefusalIsNotRemembered vérifie qu'un refus ne laisse pas de
// trace de consentement.
//
// C'est ce qui empêche l'indicateur de mentir dans le sens dangereux : un client
// refusé une fois, puis revenu, doit être annoncé comme jamais autorisé — sans
// quoi il suffirait de demander une première fois pour obtenir le « déjà
// autorisé » rassurant de la seconde.
func TestOAuthConsentRefusalIsNotRemembered(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	n := newBrowser(t, s.handler)

	client := n.register(allScopesTest)
	n.login(ownerEmail)

	_, challenge := pkcePair(t)
	params := authorizeQuery(client, challenge, "S256", allScopesTest)

	refused := n.consent(params, "refuser")
	if refused.Status != http.StatusSeeOther {
		t.Fatalf("refus : statut = %d, attendu 303 — corps : %s", refused.Status, refused.Body)
	}

	again := n.get(authorizePath + "?" + params.Encode())
	if !strings.Contains(plainText(again.Body), "Première autorisation de ce client") {
		t.Error("un client refusé est annoncé comme déjà autorisé")
	}
}

// TestOAuthCodeReuseRevokesFamily vérifie la détection de rejeu d'un code.
//
// C'est la défense contre le vol de code : si un attaquant parvient à échanger
// un code avant le client légitime, la seconde tentative fait tomber les jetons
// que la première a obtenus. La victime perd sa session, l'attaquant perd son
// jeton, et l'incident se voit.
func TestOAuthCodeReuseRevokesFamily(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	n := newBrowser(t, s.handler)

	client := n.register(allScopesTest)
	n.login(ownerEmail)

	verifier, challenge := pkcePair(t)
	params := authorizeQuery(client, challenge, "S256", allScopesTest)
	code := codeFrom(t, n.consent(params, "autoriser"))

	first := n.exchange(client, code, verifier)
	if first.AccessToken == "" {
		t.Fatalf("le premier échange a échoué : %s — %s", first.Error, first.Description)
	}

	second := n.exchange(client, code, verifier)
	if second.AccessToken != "" {
		t.Fatal("le code a été échangé deux fois")
	}
	if second.Error != "invalid_grant" {
		t.Errorf("error = %q, attendu invalid_grant", second.Error)
	}

	if _, err := s.handler.TokenVerifier().VerifyToken(t.Context(), first.AccessToken); err == nil {
		t.Error("le jeton du premier échange vaut encore après le rejeu du code")
	}
	if after := n.refresh(client, first.RefreshToken); after.AccessToken != "" {
		t.Error("le jeton de rafraîchissement du premier échange vaut encore après le rejeu du code")
	}
}

// TestOAuthRefreshReuseRevokesFamily vérifie la détection de rejeu d'un jeton de
// rafraîchissement.
func TestOAuthRefreshReuseRevokesFamily(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	n, client, tokens := s.authorized(t, allScopesTest)

	rotated := n.refresh(client, tokens.RefreshToken)
	if rotated.RefreshToken == "" {
		t.Fatalf("le rafraîchissement a échoué : %s — %s", rotated.Error, rotated.Description)
	}

	// Le jeton déjà tourné est rejoué : la famille entière doit tomber.
	replayed := n.refresh(client, tokens.RefreshToken)
	if replayed.AccessToken != "" {
		t.Fatal("un jeton de rafraîchissement déjà tourné a été accepté")
	}
	if replayed.Error != "invalid_grant" {
		t.Errorf("error = %q, attendu invalid_grant", replayed.Error)
	}

	if _, err := s.handler.TokenVerifier().VerifyToken(t.Context(), rotated.AccessToken); err == nil {
		t.Error("le jeton d'accès en cours vaut encore après un rejeu de rafraîchissement")
	}
	if again := n.refresh(client, rotated.RefreshToken); again.AccessToken != "" {
		t.Error("le jeton de rafraîchissement en cours vaut encore après un rejeu")
	}
}

// --- Vérification des jetons ------------------------------------------------

// TestTokenVerifierUsesTokenScopes est le test qui donne son sens au
// consentement.
//
// Un jeton n'ouvre que ce à quoi l'utilisateur a consenti, jamais tout ce qu'il
// détient. Reconstruire l'acteur depuis le rôle du compte — l'erreur naturelle —
// donnerait à l'agent les huit scopes du propriétaire alors qu'il n'en a demandé
// que deux, et rendrait la page de consentement décorative.
func TestTokenVerifierUsesTokenScopes(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	_, _, tokens := s.authorized(t, "mcp devis:read")

	actor, err := s.handler.TokenVerifier().VerifyToken(t.Context(), tokens.AccessToken)
	if err != nil {
		t.Fatalf("VerifyToken() a échoué : %v", err)
	}

	for _, scope := range []identity.Scope{identity.ScopeMCP, identity.ScopeDevisRead} {
		if !actor.Allows(scope) {
			t.Errorf("l'acteur n'a pas le scope consenti %q", scope)
		}
	}
	// Le propriétaire détient ces droits ; le jeton, non.
	for _, scope := range []identity.Scope{identity.ScopeFinanceWrite, identity.ScopeDocumentRead, identity.ScopeDevisWrite} {
		if actor.Allows(scope) {
			t.Errorf("l'acteur a le scope %q, qui n'a pas été consenti", scope)
		}
	}
}

// TestTokenVerifierRejects couvre les jetons qu'un vérificateur doit refuser.
func TestTokenVerifierRejects(t *testing.T) {
	t.Parallel()

	t.Run("jeton vide", func(t *testing.T) {
		t.Parallel()

		s := newSite(t)
		if _, err := s.handler.TokenVerifier().VerifyToken(t.Context(), ""); err == nil {
			t.Error("un jeton vide a été accepté")
		}
	})

	t.Run("jeton inventé", func(t *testing.T) {
		t.Parallel()

		s := newSite(t)
		if _, err := s.handler.TokenVerifier().VerifyToken(t.Context(), "ory_at_pas-un-vrai-jeton"); err == nil {
			t.Error("un jeton inventé a été accepté")
		}
	})

	t.Run("jeton expiré", func(t *testing.T) {
		t.Parallel()

		s := newSite(t)
		_, _, tokens := s.authorized(t, allScopesTest)

		// L'expiration est reculée dans le magasin plutôt que dans l'horloge :
		// fosite lit l'heure du système, et la seule façon de vérifier son contrôle
		// d'expiration est de lui présenter un enregistrement déjà périmé.
		s.oauth.expire(t, memKindAccessToken, fosite.AccessToken, time.Now().Add(-time.Minute))

		if _, err := s.handler.TokenVerifier().VerifyToken(t.Context(), tokens.AccessToken); err == nil {
			t.Error("un jeton expiré a été accepté")
		}
	})

	t.Run("compte désactivé après émission", func(t *testing.T) {
		t.Parallel()

		s := newSite(t)
		_, _, tokens := s.authorized(t, allScopesTest)

		// Le jeton est valide à cet instant.
		if _, err := s.handler.TokenVerifier().VerifyToken(t.Context(), tokens.AccessToken); err != nil {
			t.Fatalf("VerifyToken() a échoué avant désactivation : %v", err)
		}

		s.disable(t, ownerEmail)

		// Désactiver un compte doit couper ses jetons au premier usage, sans qu'il
		// faille aller les révoquer un par un.
		if _, err := s.handler.TokenVerifier().VerifyToken(t.Context(), tokens.AccessToken); err == nil {
			t.Error("le jeton d'un compte désactivé a été accepté")
		}
	})
}

// --- Frontières des routes --------------------------------------------------

// TestOAuthMachinePathsBypassSession vérifie que les points de terminaison
// machine répondent sans session.
//
// Un agent qui vient chercher un jeton n'en a pas : le rediriger vers /connexion
// lui servirait une page HTML là où il attend du JSON, et il n'aurait aucun moyen
// de comprendre ce qui lui arrive.
func TestOAuthMachinePathsBypassSession(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	n := newBrowser(t, s.handler)

	for _, target := range []string{metadataPath, tokenPath, revokePath, registerPath} {
		var result httpResult
		if target == metadataPath {
			result = n.get(target)
		} else {
			result = n.post(target, url.Values{"vide": {"1"}})
		}

		if result.Status == http.StatusSeeOther && strings.HasPrefix(result.Location(), "/connexion") {
			t.Errorf("%s renvoie vers le formulaire de connexion", target)
		}
	}
}

// TestOAuthAuthorizeRequiresSession est le pendant du test précédent :
// /oauth/authorize, lui, exige une session, puisque c'est là que l'utilisateur
// consent.
func TestOAuthAuthorizeRequiresSession(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	n := newBrowser(t, s.handler)

	result := n.get(authorizePath + "?response_type=code&client_id=peu-importe")
	if result.Status != http.StatusSeeOther {
		t.Fatalf("statut = %d, attendu 303 vers le formulaire de connexion", result.Status)
	}
	if !strings.HasPrefix(result.Location(), "/connexion") {
		t.Errorf("redirection vers %q, attendu /connexion", result.Location())
	}
}

// TestOAuthTokenEndpointResistsCrossSite vérifie que la dispense de protection
// intersites accordée aux points de terminaison machine ne s'étend pas à la page
// de consentement.
//
// C'est la moitié qui compte : /oauth/authorize est un formulaire soumis par un
// navigateur porteur de session, exactement la cible d'une attaque intersites.
func TestOAuthTokenEndpointResistsCrossSite(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	n := newBrowser(t, s.handler)

	client := n.register(allScopesTest)
	n.login(ownerEmail)

	_, challenge := pkcePair(t)
	params := authorizeQuery(client, challenge, "S256", allScopesTest)

	forged := n.post(authorizePath, url.Values{
		"requete":  {params.Encode()},
		"decision": {"autoriser"},
	}, "Sec-Fetch-Site", "cross-site")

	if forged.Status == http.StatusSeeOther {
		t.Fatalf("un consentement intersites a été accepté, redirigé vers %q", forged.Location())
	}
	if forged.Status != http.StatusForbidden {
		t.Errorf("statut = %d, attendu 403 — corps : %s", forged.Status, forged.Body)
	}
}
