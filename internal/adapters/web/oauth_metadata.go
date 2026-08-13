package web

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/Romain-Badino/Avanti/internal/identity"
)

// oauthMetadata est le document de découverte de la RFC 8414.
//
// C'est la seule chose qu'un client sache d'Avanti avant de commencer : il y
// lit les adresses des points de terminaison et ce que le serveur accepte. Un
// champ manquant ne provoque pas une erreur, il provoque un client qui renonce —
// la spécification MCP demande par exemple aux clients de refuser de continuer
// si code_challenge_methods_supported est absent, faute de pouvoir vérifier que
// le serveur fait bien du PKCE.
//
// Les noms de champs sont ceux de la RFC, en anglais et en snake_case ; ils ne
// se traduisent pas plus que ceux d'un en-tête HTTP.
type oauthMetadata struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	RegistrationEndpoint  string `json:"registration_endpoint"`
	RevocationEndpoint    string `json:"revocation_endpoint"`

	ScopesSupported        []string `json:"scopes_supported"`
	ResponseTypesSupported []string `json:"response_types_supported"`
	ResponseModesSupported []string `json:"response_modes_supported"`
	GrantTypesSupported    []string `json:"grant_types_supported"`

	TokenEndpointAuthMethodsSupported      []string `json:"token_endpoint_auth_methods_supported"`
	RevocationEndpointAuthMethodsSupported []string `json:"revocation_endpoint_auth_methods_supported"`

	CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported"`

	// AuthorizationResponseIssParameterSupported annonce que la réponse
	// d'autorisation porte le paramètre iss (RFC 9207). Le déclarer engage :
	// un client conforme comparera le iss reçu à l'issuer ci-dessus, et refusera
	// le code si les deux diffèrent.
	AuthorizationResponseIssParameterSupported bool `json:"authorization_response_iss_parameter_supported"`
}

// oauthGrantTypes énumère les flux acceptés.
//
// La liste est courte, et ce qu'elle *ne* contient pas est l'essentiel :
//
//   - pas d'implicit ni de password, qu'OAuth 2.1 a retirés — le premier
//     expose le jeton dans l'URL, le second demande au client le mot de passe de
//     l'utilisateur ;
//   - pas de client_credentials, qui autoriserait un logiciel sans qu'aucun
//     humain n'ait rien consenti. Avanti n'a pas d'usage machine-à-machine, et
//     l'ajouter avant d'en avoir un reviendrait à ouvrir une porte pour voir.
var oauthGrantTypes = []string{"authorization_code", "refresh_token"}

// handleOAuthMetadata sert le document de découverte.
func (h *Handler) handleOAuthMetadata(w http.ResponseWriter, r *http.Request) {
	document := oauthMetadata{
		Issuer:                h.oauth.issuer,
		AuthorizationEndpoint: h.oauth.issuer + oauthAuthorizePath,
		TokenEndpoint:         h.oauth.issuer + oauthTokenPath,
		RegistrationEndpoint:  h.oauth.issuer + oauthRegisterPath,
		RevocationEndpoint:    h.oauth.issuer + oauthRevokePath,

		ScopesSupported:        scopeStrings(identity.AllScopes()),
		ResponseTypesSupported: []string{"code"},
		ResponseModesSupported: []string{"query"},
		GrantTypesSupported:    oauthGrantTypes,

		// « none » seule : les clients d'Avanti sont publics. Un agent IA tourne
		// chez un tiers, il ne peut rien garder de confidentiel, et lui remettre un
		// secret ne ferait que donner l'illusion d'une authentification. Ce qui
		// l'authentifie est PKCE, plus la redirection vers une adresse enregistrée.
		TokenEndpointAuthMethodsSupported:      []string{"none"},
		RevocationEndpointAuthMethodsSupported: []string{"none"},

		CodeChallengeMethodsSupported:              []string{"S256"},
		AuthorizationResponseIssParameterSupported: true,
	}

	setOAuthCORS(w.Header())
	h.writeJSON(w, r, http.StatusOK, document)
}

// writeJSON écrit une réponse JSON, en sérialisant dans un tampon avant de
// toucher au client.
//
// Le détour par le tampon est celui de [Handler.write], et pour la même raison :
// une erreur de sérialisation survenant en cours d'écriture produirait un
// document tronqué servi avec un code 200, que plus rien ne permettrait de
// remplacer une fois l'en-tête parti.
func (h *Handler) writeJSON(w http.ResponseWriter, r *http.Request, status int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		h.fail(r, fmt.Errorf("sérialisation de la réponse JSON : %w", err))
		http.Error(w, `{"error":"server_error"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// Ces documents décrivent l'état du serveur d'autorisation à l'instant de la
	// demande ; un cache intermédiaire qui les garderait ferait suivre à un
	// client des adresses ou des capacités périmées.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)

	if _, err := w.Write(body); err != nil {
		h.fail(r, fmt.Errorf("écriture de la réponse JSON : %w", err))
	}
}
