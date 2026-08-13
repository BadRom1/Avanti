package web

import (
	"context"
	"errors"
	"fmt"

	"github.com/ory/fosite"

	"github.com/Romain-Badino/Avanti/internal/identity"
)

// oauthVerifier implémente [identity.TokenVerifier] au-dessus du serveur
// d'autorisation.
//
// C'est le seul point où l'adapter web est consommé par un autre adapter, et il
// est délibérément étroit : le futur serveur MCP reçoit une interface du
// domaine, et n'apprend jamais que fosite existe (R4 de docs/ARCHITECTURE.md).
type oauthVerifier struct {
	provider fosite.OAuth2Provider
	accounts *identity.AccountService
}

// VerifyToken traduit un jeton d'accès en acteur.
//
// Trois vérifications s'enchaînent, et l'ordre importe peu — c'est leur
// conjonction qui fait le résultat :
//
//  1. le jeton est valide et actif. fosite recalcule sa signature, vérifie qu'il
//     figure au magasin, qu'il n'a pas expiré et qu'il n'a pas été révoqué ;
//  2. le compte qu'il désigne existe et est actif. C'est ce qui fait qu'une
//     désactivation coupe les jetons en cours au premier usage, sans qu'il
//     faille aller les révoquer un par un ;
//  3. les scopes rendus sont ceux du **jeton**, réduits à ce que le rôle courant
//     autorise encore. Un jeton émis quand le compte était propriétaire ne
//     rouvre donc rien après un passage en collaborateur.
//
// Le troisième point est celui qu'on oublie le plus facilement : reconstruire
// l'acteur depuis le rôle du compte donnerait au porteur du jeton tout ce que
// l'utilisateur détient, et non ce à quoi il a consenti. Le consentement
// n'aurait alors plus aucun effet.
func (v *oauthVerifier) VerifyToken(ctx context.Context, token string) (identity.Actor, error) {
	if token == "" {
		return identity.Actor{}, identity.ErrInvalidToken
	}

	_, request, err := v.provider.IntrospectToken(ctx, token, fosite.AccessToken, new(fosite.DefaultSession))
	if err != nil {
		// Le détail reste au serveur : dire au porteur si le jeton est expiré,
		// révoqué ou inconnu lui apprendrait lesquels de ses essais approchent.
		return identity.Actor{}, identity.ErrInvalidToken
	}

	session := request.GetSession()
	if session == nil {
		return identity.Actor{}, identity.ErrInvalidToken
	}

	user, err := v.accounts.ByID(ctx, identity.ID(session.GetSubject()))
	switch {
	case errors.Is(err, identity.ErrUnknownUser):
		return identity.Actor{}, identity.ErrInvalidToken
	case err != nil:
		// Une panne de lecture n'est pas un jeton invalide. La distinguer évite
		// qu'une base injoignable ne se présente à l'appelant comme un refus
		// d'authentification, qu'il traiterait en redemandant un jeton.
		return identity.Actor{}, fmt.Errorf("lecture du compte porteur du jeton : %w", err)
	}

	actor := user.ActorWithScopes(tokenScopes(request))
	if actor.Anonymous() {
		return identity.Actor{}, identity.ErrInvalidToken
	}

	return actor, nil
}

// tokenScopes traduit les scopes accordés au jeton en scopes du domaine.
//
// Un scope inconnu du domaine est conservé tel quel : il ne peut rien ouvrir,
// puisque [identity.NewActorWithScopes] ne retient que ce que le rôle porte.
func tokenScopes(request fosite.Requester) []identity.Scope {
	granted := request.GetGrantedScopes()

	scopes := make([]identity.Scope, 0, len(granted))
	for _, scope := range granted {
		scopes = append(scopes, identity.Scope(scope))
	}

	return scopes
}
