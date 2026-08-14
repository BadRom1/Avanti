// Tests de l'indicateur de ressource (RFC 8707) : les formes équivalentes à
// l'URL canonique du serveur MCP qui doivent passer, et le contrôle rejoué au
// point de terminaison de jeton. Les refus, eux, vivent dans la table de
// TestOAuthAuthorizeRejects.
package web_test

import (
	"net/http"
	"net/url"
	"testing"
)

// TestOAuthAuthorizeResourceAcceptedForms vérifie que les seules variations de
// forme que la RFC 3986 déclare équivalentes — plus la barre finale — sont
// acceptées : un client qui écrit le port par défaut en toutes lettres désigne
// le même serveur.
func TestOAuthAuthorizeResourceAcceptedForms(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		baseURL  string
		resource string
	}{
		{
			name:     "barre finale",
			baseURL:  baseURLTest,
			resource: baseURLTest + "/mcp/",
		},
		{
			name:     "port 80 explicite en http",
			baseURL:  baseURLTest,
			resource: "http://avanti.test:80/mcp",
		},
		{
			name:     "port 443 explicite en https",
			baseURL:  "https://avanti.test",
			resource: "https://avanti.test:443/mcp",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := newSiteWithBaseURL(t, tc.baseURL)
			n := newBrowser(t, s.handler)

			client := n.register(allScopesTest)
			n.login(ownerEmail)

			_, challenge := pkcePair(t)
			params := authorizeQuery(client, challenge, "S256", allScopesTest)
			params.Set("resource", tc.resource)

			page := n.get(authorizePath + "?" + params.Encode())
			if page.Status != http.StatusOK {
				t.Fatalf("statut = %d, attendu 200 (page de consentement) — corps : %s",
					page.Status, page.Body)
			}
		})
	}
}

// TestOAuthTokenChecksResource vérifie que le point de terminaison de jeton
// rejoue le contrôle de la RFC 8707 : une ressource présente et différente de
// la canonique est refusée en invalid_target, la canonique passe.
//
// Deux autorisations distinctes : un code est consommé par sa première
// présentation, même refusée ensuite pour sa ressource — et ce sens-là est le
// bon, un échange douteux ne doit pas laisser le code rejouable.
func TestOAuthTokenChecksResource(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	n := newBrowser(t, s.handler)

	client := n.register(allScopesTest)
	n.login(ownerEmail)

	exchangeWith := func(resource string) tokenResponse {
		verifier, challenge := pkcePair(t)
		params := authorizeQuery(client, challenge, "S256", allScopesTest)
		code := codeFrom(t, n.consent(params, "autoriser"))

		return decodeToken(t, n.post(tokenPath, url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {code},
			"redirect_uri":  {client.RedirectURI},
			"client_id":     {client.ID},
			"code_verifier": {verifier},
			"resource":      {resource},
		}))
	}

	// L'instance nue au jeton : refusée, aucun jeton émis.
	refused := exchangeWith(baseURLTest)
	if refused.AccessToken != "" {
		t.Fatal("un jeton a été émis pour une ressource qui n'est pas le serveur MCP")
	}
	if refused.Error != "invalid_target" {
		t.Errorf("error = %q, attendu invalid_target — description : %s", refused.Error, refused.Description)
	}

	// La canonique — celle que le client MCP du SDK envoie : jeton émis.
	granted := exchangeWith(baseURLTest + "/mcp")
	if granted.AccessToken == "" {
		t.Fatalf("aucun jeton pour la ressource canonique : %s — %s", granted.Error, granted.Description)
	}
}
