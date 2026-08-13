package identity

import "context"

// TokenVerifier traduit un jeton d'accès en [Actor].
//
// C'est le port par lequel un canal non interactif — le serveur MCP, demain —
// obtient l'identité de son appelant, sans rien savoir de la mécanique qui a
// émis le jeton. L'implémentation vit dans l'adapter web, où le serveur
// d'autorisation OAuth 2.1 est monté ; c'est cmd/avanti qui la branche sur son
// consommateur (R4 de docs/ARCHITECTURE.md). Le domaine, lui, ne voit jamais
// fosite.
//
// Le contrat tient en une phrase : un jeton valide rend l'acteur qu'il autorise,
// tout le reste rend [ErrInvalidToken]. « Tout le reste » comprend le jeton
// absent, illisible, expiré, révoqué, et celui d'un compte devenu inactif entre
// l'émission et l'usage — un vérificateur qui distinguerait ces cas dans son
// erreur offrirait un oracle à qui essaie des jetons au hasard.
type TokenVerifier interface {
	// VerifyToken rend l'acteur que le jeton autorise.
	//
	// Les scopes de l'acteur rendu sont ceux du *jeton*, qui peuvent être plus
	// étroits que ceux du rôle : c'est le sens du consentement, où l'utilisateur
	// n'accorde qu'une partie de ce qu'il détient.
	VerifyToken(ctx context.Context, token string) (Actor, error)
}
