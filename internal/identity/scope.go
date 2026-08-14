package identity

import "slices"

// Scope est une permission élémentaire. Sa valeur suit la forme
// « <domaine>:<action> » pour les quatre domaines métier, et « mcp » pour le
// seul accès qui ne soit pas un domaine mais un canal : celui du serveur MCP.
//
// Le type est nommé plutôt qu'une simple chaîne pour que le compilateur refuse
// une constante inventée sur place. Une permission nouvelle s'ajoute ici, pas au
// point d'appel.
type Scope string

// Les scopes reconnus. Lecture et écriture sont séparées parce qu'un droit de
// consulter n'implique pas celui de modifier — c'est la granularité qu'un
// consentement OAuth affiche et qu'un jeton MCP porte — même si la table des
// rôles (voir role.go) donne aujourd'hui les deux ensemble ou pas du tout :
// un troisième profil pourra les dissocier sans toucher aux scopes.
const (
	// ScopeDevisRead autorise la consultation des devis et des offres.
	ScopeDevisRead Scope = "devis:read"
	// ScopeDevisWrite autorise la création et la modification des devis.
	ScopeDevisWrite Scope = "devis:write"
	// ScopePlanningRead autorise la consultation des étapes et des jalons.
	ScopePlanningRead Scope = "planning:read"
	// ScopePlanningWrite autorise la modification du planning.
	ScopePlanningWrite Scope = "planning:write"
	// ScopeFinanceRead autorise la consultation des factures et acomptes.
	ScopeFinanceRead Scope = "finance:read"
	// ScopeFinanceWrite autorise la saisie des factures et acomptes.
	ScopeFinanceWrite Scope = "finance:write"
	// ScopeDocumentRead autorise la consultation des pièces du dossier.
	ScopeDocumentRead Scope = "document:read"
	// ScopeDocumentWrite autorise le dépôt et le classement des pièces.
	ScopeDocumentWrite Scope = "document:write"
	// ScopeMCP autorise l'accès par le serveur MCP, donc par un agent IA. Il est
	// distinct des scopes de domaine : disposer d'un droit sur les devis ne dit
	// rien du canal par lequel on l'exerce, et l'inverse est également vrai.
	ScopeMCP Scope = "mcp"
)

// allScopes énumère les scopes dans l'ordre où ils sont présentés à
// l'utilisateur. C'est aussi la référence de [Scope.Known] : un scope absent de
// cette liste n'existe pas.
var allScopes = []Scope{
	ScopeDevisRead,
	ScopeDevisWrite,
	ScopePlanningRead,
	ScopePlanningWrite,
	ScopeFinanceRead,
	ScopeFinanceWrite,
	ScopeDocumentRead,
	ScopeDocumentWrite,
	ScopeMCP,
}

// AllScopes renvoie la liste des scopes reconnus, dans un ordre stable.
//
// La tranche renvoyée est une copie : la modifier ne change rien au domaine.
func AllScopes() []Scope {
	return slices.Clone(allScopes)
}

// Known indique si le scope fait partie de ceux que le domaine reconnaît.
func (s Scope) Known() bool {
	return slices.Contains(allScopes, s)
}

// String rend le scope tel qu'il circule dans un jeton ou un journal.
func (s Scope) String() string {
	return string(s)
}
