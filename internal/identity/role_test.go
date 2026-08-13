package identity_test

import (
	"slices"
	"testing"

	"github.com/Romain-Badino/Avanti/internal/identity"
)

// TestScopesByRole fige la table d'autorisation. C'est le test le plus
// important du domaine : c'est lui qui échouera si quelqu'un élargit les droits
// du collaborateur sans le décider.
func TestScopesByRole(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		role   identity.Role
		scopes []identity.Scope
	}{
		{
			name:   "le propriétaire a tous les scopes",
			role:   identity.RoleProprietaire,
			scopes: identity.AllScopes(),
		},
		{
			name: "le collaborateur est borné aux devis et au planning",
			role: identity.RoleCollaborateur,
			scopes: []identity.Scope{
				identity.ScopeDevisRead,
				identity.ScopeDevisWrite,
				identity.ScopePlanningRead,
				identity.ScopePlanningWrite,
			},
		},
		{
			name:   "un rôle inconnu n'a aucun scope",
			role:   identity.Role("administrateur"),
			scopes: nil,
		},
		{
			name:   "la valeur nulle de Role n'a aucun scope",
			role:   identity.Role(""),
			scopes: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			gots := tc.role.Scopes()
			if !sameScopes(gots, tc.scopes) {
				t.Errorf("%s.Scopes() = %v, attendu %v", tc.role, gots, tc.scopes)
			}
		})
	}
}

// TestCollaboratorLacksForbiddenScopes nomme un par un les scopes que le
// collaborateur ne doit pas avoir. Le test précédent compare des listes ; celui-ci
// dit ce qui est en jeu, de sorte qu'un échec se lise sans consulter la table.
func TestCollaboratorLacksForbiddenScopes(t *testing.T) {
	t.Parallel()

	forbidden := []identity.Scope{
		identity.ScopeFinanceRead,
		identity.ScopeFinanceWrite,
		identity.ScopeDocumentRead,
		identity.ScopeDocumentWrite,
		identity.ScopeMCP,
	}

	actor := identity.NewActor("compte-01", identity.RoleCollaborateur)

	for _, scope := range forbidden {
		if actor.Allows(scope) {
			t.Errorf("le collaborateur ne doit pas détenir %s", scope)
		}
	}
}

func TestAllowsMCP(t *testing.T) {
	t.Parallel()

	cases := map[identity.Role]bool{
		identity.RoleProprietaire:  true,
		identity.RoleCollaborateur: false,
		identity.Role("inconnu"):   false,
	}

	for role, want := range cases {
		if got := role.AllowsMCP(); got != want {
			t.Errorf("%s.AllowsMCP() = %t, attendu %t", role, got, want)
		}
	}
}

func TestRoleKnown(t *testing.T) {
	t.Parallel()

	for _, role := range identity.AllRoles() {
		if !role.Known() {
			t.Errorf("%s figure dans AllRoles() mais Known() dit le contraire", role)
		}
	}

	for _, role := range []identity.Role{"", "Proprietaire", "PROPRIETAIRE", "admin"} {
		if role.Known() {
			t.Errorf("Role(%q).Known() doit être faux", role)
		}
	}
}

// TestScopesIsACopy vérifie qu'un appelant ne peut pas élargir les droits
// d'un rôle pour tout le processus en modifiant la tranche qu'on lui rend.
func TestScopesIsACopy(t *testing.T) {
	t.Parallel()

	scopes := identity.RoleCollaborateur.Scopes()
	for i := range scopes {
		scopes[i] = identity.ScopeMCP
	}

	if identity.RoleCollaborateur.AllowsMCP() {
		t.Fatal("modifier la tranche rendue par Scopes() a modifié la table d'autorisation")
	}
	if slices.Contains(identity.RoleCollaborateur.Scopes(), identity.ScopeMCP) {
		t.Fatal("la table d'autorisation du collaborateur a été contaminée")
	}
}

func TestScopeKnown(t *testing.T) {
	t.Parallel()

	for _, scope := range identity.AllScopes() {
		if !scope.Known() {
			t.Errorf("%s figure dans AllScopes() mais Known() dit le contraire", scope)
		}
	}

	for _, scope := range []identity.Scope{"", "devis", "devis:admin", "DEVIS:READ", "*"} {
		if scope.Known() {
			t.Errorf("Scope(%q).Known() doit être faux", scope)
		}
	}
}

// TestAllScopesCoversTheFourDomains garde le catalogue complet : chaque
// domaine métier a bien sa paire lecture / écriture, et le scope MCP est à part.
func TestAllScopesCoversTheFourDomains(t *testing.T) {
	t.Parallel()

	wants := []identity.Scope{
		"devis:read", "devis:write",
		"planning:read", "planning:write",
		"finance:read", "finance:write",
		"document:read", "document:write",
		"mcp",
	}

	if !sameScopes(identity.AllScopes(), wants) {
		t.Errorf("AllScopes() = %v, attendu %v", identity.AllScopes(), wants)
	}
}

// sameScopes compare deux jeux de scopes sans tenir compte de l'ordre.
func sameScopes(a, b []identity.Scope) bool {
	left, right := slices.Clone(a), slices.Clone(b)
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right)
}
