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
