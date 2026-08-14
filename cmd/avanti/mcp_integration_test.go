// Test de bout en bout du serveur MCP : le flow OAuth complet du lot 4b, puis
// de vraies requêtes MCP — initialize, tools/list, tools/call — jouées avec le
// jeton obtenu, contre le PostgreSQL réel et le routage racine de la commande
// serve.
//
// Il vit dans cmd/avanti pour la même raison que le bout-en-bout OAuth : c'est
// le seul endroit du dépôt autorisé à connaître les deux familles d'adapters,
// et c'est précisément leur assemblage — le vérificateur de jetons de l'adapter
// web injecté dans l'adapter mcp — qui est sous test.
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	adaptermcp "github.com/Romain-Badino/Avanti/internal/adapters/mcp"
	"github.com/Romain-Badino/Avanti/internal/devis"
	"github.com/Romain-Badino/Avanti/internal/identity"
)

// e2eMCPScope est ce que l'agent demande : le canal, la lecture et l'écriture
// des devis — assez pour un tool de consultation ET un tool d'écriture, sans
// les finances, ce qui donne au même test le refus par scope de domaine.
const e2eMCPScope = "mcp devis:read devis:write"

// bearerTransportE2E ajoute le jeton d'accès à chaque requête du client MCP.
type bearerTransportE2E struct {
	token string
}

func (t bearerTransportE2E) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(clone)
}

func TestMCPEndToEnd(t *testing.T) {
	t.Parallel()

	app := startAvanti(t)

	// 1. Le document RFC 9728 est public et désigne l'URL canonique du serveur
	//    MCP et le serveur d'autorisation embarqué.
	metadata := app.get(t, "/.well-known/oauth-protected-resource")
	if metadata.Status != http.StatusOK {
		t.Fatalf("métadonnées de ressource : statut = %d, attendu 200 — corps : %s",
			metadata.Status, metadata.Body)
	}
	var resourceDoc struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	if err := json.Unmarshal([]byte(metadata.Body), &resourceDoc); err != nil {
		t.Fatalf("document RFC 9728 illisible : %v — corps : %s", err, metadata.Body)
	}
	if want := app.server.URL + "/mcp"; resourceDoc.Resource != want {
		t.Errorf("resource = %q, attendu %q", resourceDoc.Resource, want)
	}
	if len(resourceDoc.AuthorizationServers) != 1 || resourceDoc.AuthorizationServers[0] != app.server.URL {
		t.Errorf("authorization_servers = %v, attendu [%s]", resourceDoc.AuthorizationServers, app.server.URL)
	}

	// La forme normative avec chemin (RFC 9728 §3.1) sert le même document, et
	// n'est pas avalée par le « tout le reste » de l'adapter web.
	metadataMCP := app.get(t, "/.well-known/oauth-protected-resource/mcp")
	if metadataMCP.Status != http.StatusOK || metadataMCP.Body != metadata.Body {
		t.Errorf("métadonnées avec chemin : statut = %d, corps identique = %t — attendu 200 et le même document",
			metadataMCP.Status, metadataMCP.Body == metadata.Body)
	}

	// 2. Sans jeton, le serveur MCP refuse en 401 et dit où se trouve le
	//    document de découverte.
	status, header := app.postMCPRaw(t)
	if status != http.StatusUnauthorized {
		t.Fatalf("POST /mcp sans jeton : statut = %d, attendu 401", status)
	}
	if authenticate := header.Get("WWW-Authenticate"); !strings.Contains(authenticate,
		`resource_metadata="`+app.server.URL+`/.well-known/oauth-protected-resource"`) {
		t.Fatalf("WWW-Authenticate = %q, ne désigne pas le document RFC 9728", authenticate)
	}

	// 3. Un collaborateur ne peut pas ouvrir d'accès agent : le refus tombe dès
	//    l'autorisation — son rôle ne porte pas le scope mcp, aucun jeton MCP ne
	//    peut donc exister pour lui, et la « connexion MCP » est refusée en
	//    amont de toute requête MCP.
	app.checkCollaborateurRefused(t)

	// 4. Flow OAuth complet du propriétaire : enregistrement dynamique,
	//    consentement, échange du code — resource = URL canonique du serveur MCP.
	app.login(t)
	clientID := app.registerClientWithScope(t, "Agent MCP de bout en bout", e2eMCPScope)
	tokens := app.authorizeMCP(t, clientID)

	// 5. Données réelles : une demande et un devis semés par le domaine.
	demande, err := app.devisService.CreateDemande(t.Context(), devis.DemandeInput{
		Lot:    "Charpente",
		SentAt: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		By:     devis.ActeurID(app.owner.ID.String()),
	})
	if err != nil {
		t.Fatalf("création de la demande : %v", err)
	}
	if _, seedErr := app.devisService.RecordDevis(t.Context(), devis.DevisInput{
		DemandeID:  demande.ID,
		Artisan:    devis.Artisan{Entreprise: "Charpentes du Val"},
		Montant:    1_180_050,
		ReceivedAt: time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC),
		By:         devis.ActeurID(app.owner.ID.String()),
	}); seedErr != nil {
		t.Fatalf("enregistrement du devis semé : %v", seedErr)
	}

	// 6. Session MCP réelle : initialize par le client du SDK, avec le jeton.
	client := sdk.NewClient(&sdk.Implementation{Name: "agent-e2e", Version: "v0.0.0"}, nil)
	session, err := client.Connect(t.Context(), &sdk.StreamableClientTransport{
		Endpoint: app.server.URL + adaptermcp.ServerPath,
		HTTPClient: &http.Client{
			Transport: bearerTransportE2E{token: tokens.AccessToken},
			Timeout:   30 * time.Second,
		},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("initialize MCP : %v", err)
	}
	defer func() {
		if closeErr := session.Close(); closeErr != nil {
			t.Errorf("fermeture de la session MCP : %v", closeErr)
		}
	}()

	// 7. tools/list annonce les tools français.
	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("tools/list : %v", err)
	}
	names := make(map[string]bool, len(tools.Tools))
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	if !names["devis_liste"] || !names["devis_enregistrer"] || !names["assurance_preparer_envoi"] {
		t.Fatalf("tools/list = %v, tools attendus manquants", names)
	}

	// 8. Consultation : devis_liste rend le semé.
	liste, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: "devis_liste"})
	if err != nil {
		t.Fatalf("tools/call devis_liste : %v", err)
	}
	if liste.IsError {
		t.Fatalf("devis_liste refusé : %s", textOf(liste))
	}
	if body := textOf(liste); !strings.Contains(body, "Charpentes du Val") {
		t.Errorf("devis_liste ne rend pas le devis semé : %s", body)
	}

	// 9. Écriture : devis_enregistrer atteint le domaine, signé de l'acteur du
	//    jeton.
	written, err := session.CallTool(t.Context(), &sdk.CallToolParams{
		Name: "devis_enregistrer",
		Arguments: map[string]any{
			"demande_id":       demande.ID.String(),
			"entreprise":       "Toitures Réunies",
			"montant_centimes": 1_050_000,
			"recu_le":          "2026-05-12",
		},
	})
	if err != nil {
		t.Fatalf("tools/call devis_enregistrer : %v", err)
	}
	if written.IsError {
		t.Fatalf("devis_enregistrer refusé : %s", textOf(written))
	}

	all, err := app.devisService.AllDevis(t.Context())
	if err != nil {
		t.Fatalf("relecture des devis : %v", err)
	}
	recorded := false
	for _, proposition := range all {
		if proposition.Artisan.Entreprise == "Toitures Réunies" {
			recorded = true
			if string(proposition.RecordedBy) != app.owner.ID.String() {
				t.Errorf("RecordedBy = %q, attendu l'acteur du jeton %q",
					proposition.RecordedBy, app.owner.ID)
			}
		}
	}
	if !recorded {
		t.Fatal("le devis enregistré par MCP n'est pas en base")
	}

	// 10. Refus par scope de domaine : le jeton ne porte pas finance:read, le
	//     tool le dit — jamais un résultat vide.
	refused, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: "finances_factures"})
	if err != nil {
		t.Fatalf("tools/call finances_factures : %v", err)
	}
	if !refused.IsError {
		t.Fatal("finances_factures a répondu à un jeton sans finance:read")
	}
	if body := textOf(refused); !strings.Contains(body, "finance:read") {
		t.Errorf("le refus ne nomme pas le scope manquant : %s", body)
	}
}

// textOf rassemble le texte d'un résultat de tool.
func textOf(result *sdk.CallToolResult) string {
	var out strings.Builder
	for _, content := range result.Content {
		if text, ok := content.(*sdk.TextContent); ok {
			out.WriteString(text.Text)
		}
	}
	return out.String()
}

// postMCPRaw joue un POST nu sur /mcp, sans jeton, et rend statut et en-têtes.
func (a *avantiInstance) postMCPRaw(t *testing.T) (int, http.Header) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		a.server.URL+adaptermcp.ServerPath, strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("construction de la requête : %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	result, err := a.client.Do(req)
	if err != nil {
		t.Fatalf("POST /mcp : %v", err)
	}
	defer func() {
		if closeErr := result.Body.Close(); closeErr != nil {
			t.Errorf("fermeture du corps de réponse : %v", closeErr)
		}
	}()
	if _, err := io.Copy(io.Discard, result.Body); err != nil {
		t.Fatalf("lecture du corps de réponse : %v", err)
	}

	return result.StatusCode, result.Header
}

// registerClientWithScope enregistre un client dynamique avec le scope donné.
func (a *avantiInstance) registerClientWithScope(t *testing.T, name, scope string) string {
	t.Helper()

	result := a.postJSON(t, "/oauth/register", map[string]any{
		"client_name":   name,
		"redirect_uris": []string{e2eRedirectURI},
		"grant_types":   []string{"authorization_code", "refresh_token"},
		"scope":         scope,
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

// authorizeMCP joue une autorisation complète pour le scope MCP, resource
// comprise — l'URL canonique du serveur MCP, seule valeur acceptée.
func (a *avantiInstance) authorizeMCP(t *testing.T, clientID string) tokensE2E {
	t.Helper()

	verifier, challenge := pkcePairE2E(t)
	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {e2eRedirectURI},
		"scope":                 {e2eMCPScope},
		"state":                 {e2eState},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"resource":              {a.server.URL + adaptermcp.ServerPath},
	}

	granted := a.postForm(t, "/oauth/authorize", url.Values{
		"requete":  {params.Encode()},
		"decision": {"autoriser"},
	})
	if granted.Status != http.StatusSeeOther {
		t.Fatalf("autorisation MCP : statut = %d — corps : %s", granted.Status, granted.Body)
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

// checkCollaborateurRefused vérifie qu'un collaborateur ne peut pas ouvrir
// d'accès agent : la page d'autorisation lui répond 403 — le refus est celui du
// compte, pas de la demande, et aucun jeton MCP n'existera jamais pour lui.
//
// Le refus « jeton MCP valable mais sans scope mcp » n'est, lui, pas
// constructible par le vrai flow — le scope mcp est exigé à la demande ET
// détenu par tout compte autorisable ; il est couvert au niveau unitaire de
// l'adapter mcp, avec un acteur forgé.
func (a *avantiInstance) checkCollaborateurRefused(t *testing.T) {
	t.Helper()

	const collabEmail = "architecte@exemple.fr"
	if _, err := a.accounts.Create(t.Context(), collabEmail, "Architecte",
		e2ePassword, identity.RoleCollaborateur); err != nil {
		t.Fatalf("création du compte collaborateur : %v", err)
	}

	// Un navigateur à part : la session du collaborateur ne doit pas écraser
	// celle que le propriétaire ouvrira ensuite.
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New() échoué : %v", err)
	}
	collabClient := &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: 30 * time.Second,
	}

	login, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		a.server.URL+"/connexion", strings.NewReader(url.Values{
			"email":        {collabEmail},
			"mot_de_passe": {e2ePassword},
		}.Encode()))
	if err != nil {
		t.Fatalf("construction de la connexion : %v", err)
	}
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	logged, err := collabClient.Do(login)
	if err != nil {
		t.Fatalf("connexion du collaborateur : %v", err)
	}
	if closeErr := logged.Body.Close(); closeErr != nil {
		t.Errorf("fermeture du corps de réponse : %v", closeErr)
	}
	if logged.StatusCode != http.StatusSeeOther {
		t.Fatalf("connexion du collaborateur : statut = %d, attendu 303", logged.StatusCode)
	}

	clientID := a.registerClientWithScope(t, "Agent du collaborateur", e2eMCPScope)
	_, challenge := pkcePairE2E(t)
	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {e2eRedirectURI},
		"scope":                 {e2eMCPScope},
		"state":                 {e2eState},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"resource":              {a.server.URL + adaptermcp.ServerPath},
	}

	authorize, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		a.server.URL+"/oauth/authorize?"+params.Encode(), http.NoBody)
	if err != nil {
		t.Fatalf("construction de la demande d'autorisation : %v", err)
	}

	result, err := collabClient.Do(authorize)
	if err != nil {
		t.Fatalf("demande d'autorisation du collaborateur : %v", err)
	}
	defer func() {
		if closeErr := result.Body.Close(); closeErr != nil {
			t.Errorf("fermeture du corps de réponse : %v", closeErr)
		}
	}()
	if _, err := io.Copy(io.Discard, result.Body); err != nil {
		t.Fatalf("lecture du corps de réponse : %v", err)
	}

	if result.StatusCode != http.StatusForbidden {
		t.Fatalf("autorisation du collaborateur : statut = %d, attendu 403", result.StatusCode)
	}
}
