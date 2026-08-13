package identity

import "slices"

// Role est le profil d'un compte. C'est lui, et lui seul, qui détermine les
// scopes : Avanti n'offre pas de permission accordée à l'unité.
//
// Ce choix est celui d'une application à deux propriétaires et un intervenant
// extérieur. Des scopes réglables compte par compte y coûteraient une UI
// d'administration, des tests de combinaisons, et l'occasion de se tromper — pour
// un besoin qui n'existe pas. Si un troisième profil apparaît, il s'ajoute ici,
// en une constante et une ligne de table.
type Role string

// Les rôles reconnus.
const (
	// RoleProprietaire est le profil des deux personnes qui reconstruisent la
	// maison : accès complet à tous les domaines, accès agent IA compris.
	RoleProprietaire Role = "proprietaire"
	// RoleCollaborateur est le profil d'un intervenant extérieur — l'architecte.
	// Il travaille sur les devis et le planning par l'UI web, et n'a ni accès
	// aux finances, ni aux pièces du dossier, ni au serveur MCP.
	RoleCollaborateur Role = "collaborateur"
)

// scopesByRole est la table d'autorisation du domaine. Elle est volontairement
// la seule source de vérité : aucun code ne doit déduire un droit d'un test
// « si le rôle est propriétaire », sous peine de voir la règle se disperser.
var scopesByRole = map[Role][]Scope{
	RoleProprietaire: {
		ScopeDevisRead,
		ScopeDevisWrite,
		ScopePlanningRead,
		ScopePlanningWrite,
		ScopeFinanceRead,
		ScopeFinanceWrite,
		ScopeDocumentRead,
		ScopeDocumentWrite,
		ScopeMCP,
	},
	RoleCollaborateur: {
		ScopeDevisRead,
		ScopeDevisWrite,
		ScopePlanningRead,
		ScopePlanningWrite,
	},
}

// AllRoles renvoie les rôles reconnus, dans l'ordre décroissant de droits.
func AllRoles() []Role {
	return []Role{RoleProprietaire, RoleCollaborateur}
}

// Known indique si le rôle fait partie de ceux que le domaine reconnaît. Un
// rôle inconnu n'a aucun scope : la valeur nulle de Role ne donne aucun droit.
func (r Role) Known() bool {
	_, ok := scopesByRole[r]
	return ok
}

// Scopes renvoie les scopes du rôle, dans un ordre stable.
//
// La tranche renvoyée est une copie : un appelant qui la modifie ne peut pas
// élargir les droits du rôle pour tout le processus.
func (r Role) Scopes() []Scope {
	return slices.Clone(scopesByRole[r])
}

// AllowsMCP indique si le rôle peut passer par le serveur MCP, donc être
// piloté par un agent IA. C'est une lecture de la table, pas une règle
// parallèle : le jour où un rôle gagne ScopeMCP, cette méthode suit.
func (r Role) AllowsMCP() bool {
	return slices.Contains(scopesByRole[r], ScopeMCP)
}

// String rend le rôle tel qu'il est stocké en base.
func (r Role) String() string {
	return string(r)
}
