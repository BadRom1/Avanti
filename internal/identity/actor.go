package identity

// Actor est l'identité de l'appelant telle que le reste de l'application la
// manipule pour autoriser une action.
//
// C'est le seul objet d'identity que les domaines métier verront, et ils le
// verront par valeur, en paramètre. Ils n'importent donc pas ce package : R1 de
// docs/ARCHITECTURE.md tient sans exception, et un service de devis reste
// testable sans base de comptes.
//
// Les champs sont privés à dessein. Un Actor se construit par [NewActor], à
// partir d'un rôle, et ses scopes ne sont plus modifiables ensuite — sans quoi
// un appelant pourrait ajouter [ScopeMCP] à un collaborateur en une ligne, à
// l'autre bout du dépôt, sans que rien ne le signale.
type Actor struct {
	userID ID
	role   Role
	scopes map[Scope]struct{}
}

// NewActor construit l'acteur d'un compte à partir de son identifiant et de son
// rôle. Les scopes sont ceux du rôle, jamais autre chose.
//
// Un rôle inconnu donne un acteur sans aucun scope plutôt qu'une erreur : refuser
// tout est le comportement sûr, et la validité du rôle est déjà vérifiée à la
// création du compte.
func NewActor(userID ID, role Role) Actor {
	scopes := scopesByRole[role]

	granted := make(map[Scope]struct{}, len(scopes))
	for _, scope := range scopes {
		granted[scope] = struct{}{}
	}

	return Actor{userID: userID, role: role, scopes: granted}
}

// NewActorWithScopes construit un acteur borné à une partie des scopes de son
// rôle.
//
// C'est ce dont un porteur de jeton a besoin : un jeton OAuth ne porte que les
// scopes consentis, qui sont souvent plus étroits que ceux du rôle. Le résultat
// est l'*intersection* des deux, jamais leur réunion, et cette direction est
// l'essentiel :
//
//   - un jeton ne peut pas élargir les droits de son porteur. Un jeton émis
//     quand le compte était propriétaire ne rouvre rien après un passage en
//     collaborateur, parce que l'intersection se recalcule à chaque
//     vérification ;
//   - un jeton ne peut pas non plus les usurper. Les scopes accordés arrivent
//     d'une base de données, donc d'un endroit qu'un attaquant pourrait
//     atteindre avant le code ; les filtrer par la table du domaine fait que
//     même une ligne trafiquée ne donne rien que le rôle n'accorde déjà.
//
// Un scope inconnu de la table du rôle est ignoré silencieusement, pour la même
// raison : il ne peut rien ouvrir, il n'y a donc rien à signaler.
func NewActorWithScopes(userID ID, role Role, granted []Scope) Actor {
	fromRole := NewActor(userID, role)

	restricted := make(map[Scope]struct{}, len(granted))
	for _, scope := range granted {
		if fromRole.Allows(scope) {
			restricted[scope] = struct{}{}
		}
	}

	return Actor{userID: userID, role: role, scopes: restricted}
}

// UserID renvoie l'identifiant du compte porté par l'acteur. C'est cet
// identifiant que les domaines métier consignent quand ils datent une action.
func (a Actor) UserID() ID {
	return a.userID
}

// Role renvoie le rôle de l'acteur.
func (a Actor) Role() Role {
	return a.role
}

// Allows indique si l'acteur détient le scope demandé.
//
// L'acteur nul — celui d'une requête non authentifiée — n'autorise rien : sa
// carte de scopes est vide, et l'absence d'une clé est un refus.
func (a Actor) Allows(scope Scope) bool {
	_, ok := a.scopes[scope]
	return ok
}

// Scopes renvoie les scopes de l'acteur dans l'ordre stable de
// [AllScopes] — pour l'afficher ou le journaliser, pas pour décider :
// [Actor.Allows] est fait pour cela.
func (a Actor) Scopes() []Scope {
	granted := make([]Scope, 0, len(a.scopes))
	for _, scope := range allScopes {
		if _, ok := a.scopes[scope]; ok {
			granted = append(granted, scope)
		}
	}
	return granted
}

// Anonymous indique que l'acteur ne désigne aucun compte. C'est la valeur nulle du
// type, celle qu'on obtient d'un contexte de requête sans session.
func (a Actor) Anonymous() bool {
	return a.userID == ""
}
