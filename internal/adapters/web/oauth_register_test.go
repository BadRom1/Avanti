package web_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// registrationBody est le corps d'une demande d'enregistrement par ailleurs
// valide, que chaque cas altère.
func registrationBody() map[string]any {
	return map[string]any{
		"client_name":                "Agent de test",
		"redirect_uris":              []string{redirectURITest},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"scope":                      allScopesTest,
		"token_endpoint_auth_method": "none",
	}
}

// TestOAuthRegistrationAccepts vérifie ce que l'enregistrement dynamique rend à
// un client conforme.
//
// La réponse est ce dont le client se sert pour tout le reste : un client_id
// manquant ou un token_endpoint_auth_method inattendu le fait échouer plus tard,
// dans le flux, où la cause est bien plus difficile à voir.
func TestOAuthRegistrationAccepts(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	n := newBrowser(t, s.handler)

	result := n.postJSON(registerPath, registrationBody())
	if result.Status != http.StatusCreated {
		t.Fatalf("statut = %d, attendu 201 — corps : %s", result.Status, result.Body)
	}

	var response struct {
		ClientID     string   `json:"client_id"`
		ClientSecret string   `json:"client_secret"`
		IssuedAt     int64    `json:"client_id_issued_at"`
		ClientName   string   `json:"client_name"`
		RedirectURIs []string `json:"redirect_uris"`
		GrantTypes   []string `json:"grant_types"`
		AuthMethod   string   `json:"token_endpoint_auth_method"`
	}
	if err := json.Unmarshal([]byte(result.Body), &response); err != nil {
		t.Fatalf("réponse illisible : %v — corps : %s", err, result.Body)
	}

	if response.ClientID == "" {
		t.Error("client_id absent")
	}
	// Un client public n'a pas de secret. En rendre un donnerait à croire qu'il
	// authentifie quelque chose, alors qu'il serait lisible par quiconque a
	// installé l'agent.
	if response.ClientSecret != "" {
		t.Errorf("client_secret = %q, un client public ne doit pas en recevoir", response.ClientSecret)
	}
	if response.AuthMethod != "none" {
		t.Errorf("token_endpoint_auth_method = %q, attendu \"none\"", response.AuthMethod)
	}
	if response.IssuedAt == 0 {
		t.Error("client_id_issued_at absent")
	}
	if response.ClientName != "Agent de test" {
		t.Errorf("client_name = %q, attendu le nom déclaré", response.ClientName)
	}
	if len(response.RedirectURIs) != 1 || response.RedirectURIs[0] != redirectURITest {
		t.Errorf("redirect_uris = %v, attendu l'adresse déclarée", response.RedirectURIs)
	}
	if !contains(response.GrantTypes, "refresh_token") {
		t.Errorf("grant_types = %v, doit contenir refresh_token", response.GrantTypes)
	}
}

// TestOAuthRegistrationDefaults vérifie qu'un client minimal est accepté.
//
// C'est le cas réel : la RFC 7591 ne rend obligatoire que redirect_uris, et un
// client qui s'en tient au minimum doit obtenir des valeurs par défaut
// utilisables, pas un refus.
func TestOAuthRegistrationDefaults(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	n := newBrowser(t, s.handler)

	result := n.postJSON(registerPath, map[string]any{
		"redirect_uris": []string{redirectURITest},
	})
	if result.Status != http.StatusCreated {
		t.Fatalf("statut = %d, attendu 201 — corps : %s", result.Status, result.Body)
	}

	var response struct {
		ClientName string `json:"client_name"`
		GrantTypes []string
		Scope      string `json:"scope"`
	}
	if err := json.Unmarshal([]byte(result.Body), &response); err != nil {
		t.Fatalf("réponse illisible : %v — corps : %s", err, result.Body)
	}

	// Un client anonyme reçoit un nom d'affichage plutôt qu'une ligne vide sur la
	// page de consentement.
	if response.ClientName == "" {
		t.Error("client_name vide : la page de consentement afficherait un trou")
	}
	// Sans scope demandé, le client peut demander tout ce que l'instance connaît —
	// ce qu'il obtiendra reste borné par les droits du compte qui consent.
	if !strings.Contains(response.Scope, "mcp") {
		t.Errorf("scope = %q, doit contenir mcp par défaut", response.Scope)
	}
}

// TestOAuthRegistrationRejects couvre les métadonnées que l'enregistrement doit
// refuser.
//
// Les adresses de retour sont le cœur du sujet : c'est là que le code
// d'autorisation est livré, et une validation laxiste transforme le serveur en
// distributeur de codes pour qui a su enregistrer la bonne adresse.
func TestOAuthRegistrationRejects(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		mutate    func(body map[string]any)
		wantError string
	}{
		{
			name:      "aucune adresse de retour",
			mutate:    func(b map[string]any) { delete(b, "redirect_uris") },
			wantError: "invalid_redirect_uri",
		},
		{
			name:      "liste d'adresses vide",
			mutate:    func(b map[string]any) { b["redirect_uris"] = []string{} },
			wantError: "invalid_redirect_uri",
		},
		{
			name:      "adresse en http hors boucle locale",
			mutate:    func(b map[string]any) { b["redirect_uris"] = []string{"http://agent.exemple.fr/callback"} },
			wantError: "invalid_redirect_uri",
		},
		{
			name:      "adresse avec joker",
			mutate:    func(b map[string]any) { b["redirect_uris"] = []string{"https://*.exemple.fr/callback"} },
			wantError: "invalid_redirect_uri",
		},
		{
			name:      "adresse avec fragment",
			mutate:    func(b map[string]any) { b["redirect_uris"] = []string{"https://agent.exemple.fr/cb#jeton"} },
			wantError: "invalid_redirect_uri",
		},
		{
			name:      "adresse relative",
			mutate:    func(b map[string]any) { b["redirect_uris"] = []string{"/callback"} },
			wantError: "invalid_redirect_uri",
		},
		{
			name:      "adresse avec identifiants",
			mutate:    func(b map[string]any) { b["redirect_uris"] = []string{"https://legit.example@attaquant.example/cb"} },
			wantError: "invalid_redirect_uri",
		},
		{
			name:      "schéma exotique",
			mutate:    func(b map[string]any) { b["redirect_uris"] = []string{"javascript:alert(1)"} },
			wantError: "invalid_redirect_uri",
		},
		{
			name: "trop d'adresses",
			mutate: func(b map[string]any) {
				uris := make([]string, 0, 6)
				for i := range 6 {
					uris = append(uris, fmt.Sprintf("https://agent.exemple.fr/cb%d", i))
				}
				b["redirect_uris"] = uris
			},
			wantError: "invalid_redirect_uri",
		},
		{
			name:      "client confidentiel demandé",
			mutate:    func(b map[string]any) { b["token_endpoint_auth_method"] = "client_secret_basic" },
			wantError: "invalid_client_metadata",
		},
		{
			name:      "flux retiré par OAuth 2.1",
			mutate:    func(b map[string]any) { b["grant_types"] = []string{"implicit"} },
			wantError: "invalid_client_metadata",
		},
		{
			name:      "identifiants de mot de passe",
			mutate:    func(b map[string]any) { b["grant_types"] = []string{"password"} },
			wantError: "invalid_client_metadata",
		},
		{
			name:      "type de réponse du flux implicite",
			mutate:    func(b map[string]any) { b["response_types"] = []string{"token"} },
			wantError: "invalid_client_metadata",
		},
		{
			name:      "scope inconnu",
			mutate:    func(b map[string]any) { b["scope"] = "mcp tout:pouvoir" },
			wantError: "invalid_client_metadata",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := newSite(t)
			n := newBrowser(t, s.handler)

			body := registrationBody()
			tc.mutate(body)

			result := n.postJSON(registerPath, body)
			if result.Status != http.StatusBadRequest {
				t.Fatalf("statut = %d, attendu 400 — corps : %s", result.Status, result.Body)
			}

			var response struct {
				Error       string `json:"error"`
				Description string `json:"error_description"`
			}
			if err := json.Unmarshal([]byte(result.Body), &response); err != nil {
				t.Fatalf("réponse d'erreur illisible : %v — corps : %s", err, result.Body)
			}
			if response.Error != tc.wantError {
				t.Errorf("error = %q, attendu %q — description : %q",
					response.Error, tc.wantError, response.Description)
			}
			if response.Description == "" {
				t.Error("error_description vide : le client n'apprend pas ce qui cloche")
			}
		})
	}
}

// TestOAuthRegistrationLoopbackAccepted vérifie l'exception de la boucle locale.
//
// Un agent installé sur le poste de l'utilisateur reçoit son code sur
// http://127.0.0.1 : il n'y a pas de réseau à écouter, et lui imposer https
// l'obligerait à embarquer un certificat (RFC 8252).
func TestOAuthRegistrationLoopbackAccepted(t *testing.T) {
	t.Parallel()

	for _, uri := range []string{
		"http://127.0.0.1:47821/callback",
		"http://localhost:47821/callback",
		"http://[::1]:47821/callback",
	} {
		t.Run(uri, func(t *testing.T) {
			t.Parallel()

			s := newSite(t)
			n := newBrowser(t, s.handler)

			body := registrationBody()
			body["redirect_uris"] = []string{uri}

			result := n.postJSON(registerPath, body)
			if result.Status != http.StatusCreated {
				t.Fatalf("statut = %d, attendu 201 pour %s — corps : %s", result.Status, uri, result.Body)
			}
		})
	}
}

// TestOAuthRegistrationRejectsMalformedBody couvre ce qui n'est même pas une
// demande.
func TestOAuthRegistrationRejectsMalformedBody(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		contentType string
		body        string
	}{
		{name: "JSON invalide", contentType: "application/json", body: `{"redirect_uris": [`},
		{name: "corps vide", contentType: "application/json", body: ``},
		{name: "formulaire au lieu de JSON", contentType: "application/x-www-form-urlencoded", body: `redirect_uris=x`},
		{
			name:        "corps démesuré",
			contentType: "application/json",
			body:        `{"client_name":"` + strings.Repeat("a", 9000) + `"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := newSite(t)
			n := newBrowser(t, s.handler)

			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, registerPath,
				bytes.NewReader([]byte(tc.body)))
			req.Header.Set("Content-Type", tc.contentType)

			result := n.send(req)
			if result.Status != http.StatusBadRequest {
				t.Fatalf("statut = %d, attendu 400 — corps : %s", result.Status, result.Body)
			}
		})
	}
}

// TestOAuthRegistrationRateLimited vérifie la limite de débit.
//
// Le point de terminaison est ouvert par construction — c'est le modèle de MCP,
// où un agent découvre le serveur avant de connaître qui que ce soit. Ouvert ne
// veut pas dire sans limite : sans compteur, une boucle remplirait la table en
// quelques secondes.
func TestOAuthRegistrationRateLimited(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	n := newBrowser(t, s.handler)

	// Cinq demandes passent depuis une même adresse ; la sixième est refusée.
	for i := range 5 {
		result := n.postJSON(registerPath, registrationBody())
		if result.Status != http.StatusCreated {
			t.Fatalf("enregistrement %d : statut = %d, attendu 201 — corps : %s", i+1, result.Status, result.Body)
		}
	}

	blocked := n.postJSON(registerPath, registrationBody())
	if blocked.Status != http.StatusTooManyRequests {
		t.Fatalf("statut = %d, attendu 429 — corps : %s", blocked.Status, blocked.Body)
	}

	// Une autre adresse n'est pas affectée : la limite vise l'auteur du flot, pas
	// le point de terminaison.
	other := newBrowser(t, s.handler)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, registerPath, bytes.NewReader(mustJSON(t, registrationBody())))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "198.51.100.7:40000"

	if result := other.send(req); result.Status != http.StatusCreated {
		t.Fatalf("depuis une autre adresse : statut = %d, attendu 201 — corps : %s", result.Status, result.Body)
	}
}

// TestOAuthRegistrationCapped vérifie le plafond de clients enregistrés.
//
// La limite de débit ralentit un flot venu d'une adresse ; elle n'arrête pas un
// flot venu de mille. Le plafond est la seconde borne, celle qui tient quelle que
// soit l'origine des demandes — le test change donc d'adresse à chaque groupe,
// exactement comme le ferait ce qu'il protège.
func TestOAuthRegistrationCapped(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	n := newBrowser(t, s.handler)

	// La cinquantaine de clients autorisés passe, la suivante est refusée.
	const clientCap = 50

	for i := range clientCap {
		if result := registerFrom(t, n, i); result.Status != http.StatusCreated {
			t.Fatalf("enregistrement %d : statut = %d, attendu 201 — corps : %s", i+1, result.Status, result.Body)
		}
	}

	blocked := registerFrom(t, n, clientCap)
	if blocked.Status != http.StatusForbidden {
		t.Fatalf("au-delà du plafond : statut = %d, attendu 403 — corps : %s", blocked.Status, blocked.Body)
	}

	var response struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(blocked.Body), &response); err != nil {
		t.Fatalf("réponse d'erreur illisible : %v — corps : %s", err, blocked.Body)
	}
	if response.Error != "access_denied" {
		t.Errorf("error = %q, attendu access_denied", response.Error)
	}
}

// registerFrom enregistre un client depuis une adresse qui change tous les cinq
// appels, pour contourner la limite de débit sans la désactiver.
func registerFrom(t *testing.T, n *browser, attempt int) httpResult {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, registerPath,
		bytes.NewReader(mustJSON(t, registrationBody())))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = fmt.Sprintf("203.0.113.%d:40000", attempt/5)

	return n.send(req)
}

func mustJSON(t *testing.T, payload any) []byte {
	t.Helper()

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("sérialisation : %v", err)
	}

	return body
}
