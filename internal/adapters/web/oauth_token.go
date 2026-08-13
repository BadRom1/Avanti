package web

import (
	"net/http"

	"github.com/ory/fosite"
)

// handleOAuthToken échange un code d'autorisation ou un jeton de
// rafraîchissement contre un jeton d'accès.
//
// Le corps du gestionnaire est court parce que tout ce qui compte est déjà
// décidé ailleurs : fosite vérifie le code, le vérificateur PKCE, l'identité du
// client, puis relit les scopes accordés dans l'enregistrement du code — ce qui
// signifie qu'aucun scope ne peut être élargi ici, même par un client qui en
// redemanderait d'autres.
//
// La rotation des jetons de rafraîchissement est faite par le magasin : le jeton
// présenté et le jeton d'accès qu'il accompagnait cessent de valoir à l'instant
// où leurs remplaçants sont émis. Rejouer un jeton déjà tourné fait tomber toute
// la famille.
func (h *Handler) handleOAuthToken(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	setOAuthCORS(w.Header())

	// La session passée est un réceptacle vide : le magasin y désérialise celle
	// qui a été gelée à l'autorisation, sujet compris.
	request, err := h.oauth.provider.NewAccessRequest(ctx, r, new(fosite.DefaultSession))
	if err != nil {
		h.oauth.provider.WriteAccessError(ctx, w, request, err)
		return
	}

	response, err := h.oauth.provider.NewAccessResponse(ctx, request)
	if err != nil {
		h.oauth.provider.WriteAccessError(ctx, w, request, err)
		return
	}

	h.oauth.provider.WriteAccessResponse(ctx, w, request, response)
}

// handleOAuthRevoke traite une demande de révocation (RFC 7009).
//
// La réponse est 200 même pour un jeton inconnu, et la RFC l'exige : distinguer
// « révoqué » de « n'a jamais existé » offrirait à qui essaie des jetons au
// hasard un moyen de savoir lesquels sont réels. Le client, lui, n'a rien à
// faire de la différence — ce qu'il voulait, que le jeton ne vaille plus rien,
// est vrai dans les deux cas.
//
// Révoquer un jeton révoque toute sa famille : le jeton d'accès et le jeton de
// rafraîchissement issus d'une même autorisation tombent ensemble, quel que soit
// celui des deux qui a été présenté.
func (h *Handler) handleOAuthRevoke(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	setOAuthCORS(w.Header())

	h.oauth.provider.WriteRevocationResponse(ctx, w, h.oauth.provider.NewRevocationRequest(ctx, r))
}
