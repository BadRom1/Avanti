package identity_test

import (
	"testing"

	"github.com/Romain-Badino/Avanti/internal/identity"
)

// TestNewActorWithScopesIntersects vérifie la propriété qui donne son sens au
// consentement : un jeton n'ouvre que ce qui lui a été accordé, jamais tout ce
// que son porteur détient.
func TestNewActorWithScopesIntersects(t *testing.T) {
	t.Parallel()

	actor := identity.NewActorWithScopes("compte-1", identity.RoleProprietaire, []identity.Scope{
		identity.ScopeMCP,
		identity.ScopeDevisRead,
	})

	for _, scope := range []identity.Scope{identity.ScopeMCP, identity.ScopeDevisRead} {
		if !actor.Allows(scope) {
			t.Errorf("Allows(%q) = false, le scope a pourtant été accordé", scope)
		}
	}

	// Le propriétaire détient ces droits ; l'acteur du jeton, non.
	for _, scope := range []identity.Scope{
		identity.ScopeDevisWrite,
		identity.ScopeFinanceRead,
		identity.ScopeDocumentWrite,
	} {
		if actor.Allows(scope) {
			t.Errorf("Allows(%q) = true, alors que ce scope n'a pas été accordé", scope)
		}
	}
}

// TestNewActorWithScopesCannotExceedRole vérifie la direction de
// l'intersection.
//
// C'est la protection contre une base trafiquée : les scopes accordés arrivent
// d'un jeton, donc d'une ligne qu'un attaquant pourrait vouloir réécrire. Les
// filtrer par la table du domaine fait qu'une ligne mensongère ne donne rien de
// plus que le rôle.
func TestNewActorWithScopesCannotExceedRole(t *testing.T) {
	t.Parallel()

	// Un collaborateur à qui un jeton prétendrait accorder les droits d'un
	// propriétaire.
	actor := identity.NewActorWithScopes("compte-2", identity.RoleCollaborateur, identity.AllScopes())

	for _, scope := range []identity.Scope{
		identity.ScopeMCP,
		identity.ScopeFinanceRead,
		identity.ScopeFinanceWrite,
		identity.ScopeDocumentRead,
		identity.ScopeDocumentWrite,
	} {
		if actor.Allows(scope) {
			t.Errorf("Allows(%q) = true, alors que le rôle collaborateur ne le porte pas", scope)
		}
	}

	// Ce que le rôle porte reste ouvert.
	for _, scope := range []identity.Scope{identity.ScopeDevisRead, identity.ScopePlanningWrite} {
		if !actor.Allows(scope) {
			t.Errorf("Allows(%q) = false, le rôle collaborateur le porte pourtant", scope)
		}
	}
}

// TestNewActorWithScopesIgnoresUnknown vérifie qu'un scope inventé n'ouvre rien.
func TestNewActorWithScopesIgnoresUnknown(t *testing.T) {
	t.Parallel()

	actor := identity.NewActorWithScopes("compte-3", identity.RoleProprietaire, []identity.Scope{
		"tout:pouvoir",
		"",
		identity.ScopeMCP,
	})

	if actor.Allows("tout:pouvoir") {
		t.Error("Allows(\"tout:pouvoir\") = true, un scope inventé ne doit rien ouvrir")
	}
	if actor.Allows("") {
		t.Error("Allows(\"\") = true, le scope vide ne doit rien ouvrir")
	}
	if !actor.Allows(identity.ScopeMCP) {
		t.Error("Allows(mcp) = false, les scopes inconnus ont contaminé le résultat")
	}
	if got := len(actor.Scopes()); got != 1 {
		t.Errorf("Scopes() en compte %d, attendu 1", got)
	}
}

// TestNewActorWithScopesEmpty vérifie qu'un jeton sans scope n'autorise rien,
// tout en désignant bien un compte.
func TestNewActorWithScopesEmpty(t *testing.T) {
	t.Parallel()

	actor := identity.NewActorWithScopes("compte-4", identity.RoleProprietaire, nil)

	if actor.Anonymous() {
		t.Error("Anonymous() = true, l'acteur désigne pourtant un compte")
	}
	for _, scope := range identity.AllScopes() {
		if actor.Allows(scope) {
			t.Errorf("Allows(%q) = true pour un jeton sans scope", scope)
		}
	}
}

// TestUserActorWithScopes vérifie que la désactivation l'emporte sur tout.
//
// C'est la même garantie que pour [identity.User.Actor], et elle doit tenir sur
// les deux chemins : un compte fermé ne doit pas rester ouvert par le jeton d'un
// agent qu'on aurait oublié de révoquer.
func TestUserActorWithScopes(t *testing.T) {
	t.Parallel()

	user := identity.User{
		ID:     "compte-5",
		Role:   identity.RoleProprietaire,
		Active: true,
	}

	actor := user.ActorWithScopes([]identity.Scope{identity.ScopeMCP})
	if !actor.Allows(identity.ScopeMCP) {
		t.Error("un compte actif doit rendre un acteur aux scopes du jeton")
	}

	user.Active = false

	closed := user.ActorWithScopes([]identity.Scope{identity.ScopeMCP})
	if !closed.Anonymous() {
		t.Error("un compte désactivé doit rendre un acteur anonyme")
	}
	if closed.Allows(identity.ScopeMCP) {
		t.Error("un compte désactivé garde un scope accordé par jeton")
	}
}
