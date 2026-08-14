// Test d'intégration de l'administration des comptes par la CLI réelle :
// set-role et set-password joués par run() entier, configuration par
// l'environnement, contre un PostgreSQL réel — le chemin exact d'un exploitant
// sur l'hôte, qui est la racine de confiance du modèle (pas de page
// d'inscription ni de réinitialisation en ligne).
//
// Pas de t.Parallel() : t.Setenv configure le processus entier.
package main

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Romain-Badino/Avanti/internal/adapters/postgres"
	"github.com/Romain-Badino/Avanti/internal/identity"
)

// generatedPasswordLine isole le mot de passe engendré dans la sortie de la
// CLI : la ligne indentée qui suit l'avertissement « à noter maintenant ».
var generatedPasswordLine = regexp.MustCompile(`(?m)^ {2}(\S+)$`)

func TestUserAdministrationEndToEnd(t *testing.T) {
	dsn := freshDatabase(t)

	t.Setenv("AVANTI_ENV", "development")
	t.Setenv("AVANTI_DATABASE_URL", dsn)
	t.Setenv("AVANTI_OAUTH_SECRET", strings.Repeat("k", 44))
	t.Setenv("AVANTI_DOCUMENTS_DIR", t.TempDir())

	const email = "amelie@exemple.fr"

	// 1. Un compte collaborateur, créé par la CLI.
	var stdout, stderr bytes.Buffer
	if err := run(t.Context(), []string{
		"user", "add", "--email", email, "--nom", "Amélie Dupré", "--role", "collaborateur", "--generate",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("user add échoué : %v — stderr : %s", err, stderr.String())
	}

	// 2. set-role le promeut ; l'effet se relit en base par le service.
	stdout.Reset()
	stderr.Reset()
	if err := run(t.Context(), []string{
		"user", "set-role", "--email", email, "--role", "proprietaire",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("user set-role échoué : %v — stderr : %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "proprietaire") || !strings.Contains(stdout.String(), "immédiat") {
		t.Errorf("sortie de set-role = %q, doit nommer le rôle et rappeler l'effet immédiat", stdout.String())
	}

	pool := openPool(t, dsn)
	accounts := accountServiceOn(t, pool)

	promoted, err := accounts.ByEmail(t.Context(), email)
	if err != nil {
		t.Fatalf("relecture du compte : %v", err)
	}
	if promoted.Role != identity.RoleProprietaire {
		t.Errorf("Role = %q, attendu proprietaire", promoted.Role)
	}

	// 3. set-password --generate pose un nouveau mot de passe, qui authentifie,
	//    et l'acteur porte les scopes du rôle promu.
	stdout.Reset()
	stderr.Reset()
	if resetErr := run(t.Context(), []string{
		"user", "set-password", "--email", email, "--generate",
	}, &stdout, &stderr); resetErr != nil {
		t.Fatalf("user set-password échoué : %v — stderr : %s", resetErr, stderr.String())
	}

	match := generatedPasswordLine.FindStringSubmatch(stdout.String())
	if match == nil {
		t.Fatalf("mot de passe engendré introuvable dans la sortie :\n%s", stdout.String())
	}
	newPassword := match[1]

	actor, err := accounts.Authenticate(t.Context(), email, newPassword)
	if err != nil {
		t.Fatalf("le nouveau mot de passe est refusé : %v", err)
	}
	if !actor.Allows(identity.ScopeMCP) {
		t.Error("le compte promu proprietaire doit porter le scope mcp")
	}

	// 4. Un compte inconnu est refusé avec l'erreur du domaine, pas une panne.
	stdout.Reset()
	stderr.Reset()
	if err := run(t.Context(), []string{
		"user", "set-password", "--email", "inconnu@exemple.fr", "--generate",
	}, &stdout, &stderr); err == nil {
		t.Fatal("set-password sur un compte inconnu doit échouer")
	}
}

// accountServiceOn monte le service identity sur le pool, comme le fait
// openInstance, pour relire ce que la CLI a écrit.
func accountServiceOn(t *testing.T, pool *pgxpool.Pool) *identity.AccountService {
	t.Helper()

	repo, err := postgres.NewUserRepo(pool)
	if err != nil {
		t.Fatalf("postgres.NewUserRepo() échoué : %v", err)
	}

	accounts, err := identity.NewAccountService(identity.ServiceOptions{
		Repo:   repo,
		Hasher: identity.NewArgon2idHasher(),
	})
	if err != nil {
		t.Fatalf("identity.NewAccountService() échoué : %v", err)
	}

	return accounts
}
