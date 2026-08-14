package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"

	"github.com/Romain-Badino/Avanti/internal/identity"
)

// Les sous-commandes de `avanti user`.
const (
	subcmdUserAdd         = "add"
	subcmdUserList        = "list"
	subcmdUserDisable     = "disable"
	subcmdUserEnable      = "enable"
	subcmdUserSetPassword = "set-password"
	subcmdUserSetRole     = "set-role"
)

// runUser aiguille `avanti user …`.
//
// C'est par ici que naissent les deux premiers comptes d'une instance : Avanti
// n'a pas de page d'inscription, et n'en aura pas. Une instance auto-hébergée par
// deux personnes n'a personne à inscrire, et un formulaire d'inscription ouvert
// sur Internet serait une porte à surveiller pour un besoin qui n'existe pas.
func runUser(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		usageUser(stderr)
		return errors.New("commande user : sous-commande manquante")
	}

	switch args[0] {
	case subcmdUserAdd:
		return userAdd(ctx, args[1:], stdout, stderr)
	case subcmdUserList:
		return userList(ctx, args[1:], stdout, stderr)
	case subcmdUserDisable:
		return userSetActive(ctx, subcmdUserDisable, args[1:], stdout, stderr)
	case subcmdUserEnable:
		return userSetActive(ctx, subcmdUserEnable, args[1:], stdout, stderr)
	case subcmdUserSetPassword:
		return userSetPassword(ctx, args[1:], stdout, stderr)
	case subcmdUserSetRole:
		return userSetRole(ctx, args[1:], stdout, stderr)
	default:
		usageUser(stderr)
		return fmt.Errorf("commande user : sous-commande inconnue %q", args[0])
	}
}

func usageUser(out io.Writer) {
	help := &sink{w: out}
	help.printf(`Usage : avanti user <sous-commande> [options]

Sous-commandes
  add           Crée un compte
  list          Affiche les comptes existants
  disable       Désactive un compte : il ne peut plus se connecter
  enable        Réactive un compte désactivé
  set-password  Réinitialise le mot de passe d'un compte (mot de passe perdu)
  set-role      Change le rôle d'un compte, à effet immédiat

Options de « add »
  --email     Adresse email, qui sert d'identifiant de connexion (obligatoire)
  --nom       Nom d'affichage (obligatoire)
  --role      %[1]s (obligatoire)
  --generate  Engendre le mot de passe et l'affiche une fois, au lieu de le demander

Options de « disable » et « enable »
  --email     Adresse email du compte visé (obligatoire)

Options de « set-password »
  --email     Adresse email du compte visé (obligatoire)
  --generate  Engendre le nouveau mot de passe au lieu de le demander

Options de « set-role »
  --email     Adresse email du compte visé (obligatoire)
  --role      Nouveau rôle : %[1]s (obligatoire)

Sans --generate, le mot de passe est demandé au terminal, sans écho, puis
redemandé pour confirmation. Il faut donc un vrai terminal ; dans un script,
utilisez --generate.

Cette CLI est la voie d'administration des comptes, et la seule : elle
s'exécute sur la machine qui héberge l'instance, qui est la racine de
confiance — il n'y a ni page d'inscription, ni réinitialisation en ligne.
« set-password » répare un mot de passe perdu (Avanti ne peut pas le
retrouver, seule son empreinte est stockée).

Un compte n'est jamais supprimé : les actions qu'il a signées dans les autres
domaines continuent de le désigner. « disable » est la façon de fermer un accès.
`, availableRoles())
	// L'aide est le dernier recours d'une commande qui a déjà échoué : si même
	// elle ne s'écrit pas, il n'y a plus rien à en dire.
}

// userAdd crée un compte.
func userAdd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("avanti user add", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { usageUser(stderr) }

	email := flags.String("email", "", "adresse email, identifiant de connexion")
	name := flags.String("nom", "", "nom d'affichage")
	role := flags.String("role", "", "profil du compte : "+availableRoles())
	generate := flags.Bool("generate", false, "engendre le mot de passe au lieu de le demander")

	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("analyse des arguments : %w", err)
	}

	if *email == "" || *name == "" || *role == "" {
		usageUser(stderr)
		return errors.New("user add : --email, --nom et --role sont obligatoires")
	}
	if !identity.Role(*role).Known() {
		return fmt.Errorf("user add : rôle %q inconnu, attendu %s", *role, availableRoles())
	}

	// Le mot de passe est obtenu avant d'ouvrir la base : rien ne sert de joindre
	// PostgreSQL pour découvrir ensuite que les deux saisies diffèrent.
	password, generated, err := getPassword(*generate, stdout)
	if err != nil {
		return err
	}

	app, cleanup, err := openInstance(ctx, stderr)
	if err != nil {
		return err
	}
	defer cleanup()

	user, err := app.accounts.Create(ctx, *email, *name, password, identity.Role(*role))
	if err != nil {
		return fmt.Errorf("user add : %w", err)
	}

	out := &sink{w: stdout}
	out.printf("Compte créé.\n  identifiant : %s\n  email       : %s\n  nom         : %s\n  rôle        : %s\n",
		user.ID, user.Email, user.DisplayName, user.Role)

	if generated {
		// Le mot de passe engendré est affiché une fois et une seule : il n'existe
		// nulle part ailleurs, seule son empreinte est stockée. Il sort sur la
		// sink standard, séparée des journaux, pour rester redirigeable vers un
		// gestionnaire de mots de passe sans les lignes de démarrage au milieu.
		out.printf("\nMot de passe engendré, à noter maintenant — il ne sera plus affiché :\n\n  %s\n\n", password)
	}

	return out.err
}

// userList affiche les comptes.
func userList(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("avanti user list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { usageUser(stderr) }

	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("analyse des arguments : %w", err)
	}

	app, cleanup, err := openInstance(ctx, stderr)
	if err != nil {
		return err
	}
	defer cleanup()

	accounts, err := app.accounts.List(ctx)
	if err != nil {
		return fmt.Errorf("user list : %w", err)
	}

	out := &sink{w: stdout}
	if len(accounts) == 0 {
		out.printf("Aucun compte. Créez le premier avec « avanti user add ».\n")
		return out.err
	}

	// Les empreintes ne sont pas affichées, pas même tronquées « pour
	// information » : une empreinte n'a aucun usage à l'écran, et une moitié
	// d'empreinte non plus.
	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	rows := &sink{w: table}

	rows.printf("EMAIL\tNOM\tRÔLE\tÉTAT\tAGENT IA\tCRÉÉ LE\n")
	for _, user := range accounts {
		rows.printf("%s\t%s\t%s\t%s\t%s\t%s\n",
			user.Email, user.DisplayName, user.Role,
			accountState(user.Active), yesNo(user.Role.AllowsMCP()),
			user.CreatedAt.Format("2006-01-02"))
	}
	if rows.err != nil {
		return rows.err
	}

	if err := table.Flush(); err != nil {
		return fmt.Errorf("user list : écriture du tableau : %w", err)
	}

	return out.err
}

// userSetActive ouvre ou ferme un accès, selon la sous-commande.
//
// Les deux opérations partagent leur code parce qu'elles ne diffèrent que d'un
// appel : même désignation du compte, même idempotence, même sortie. Les séparer
// donnerait deux fonctions identiques à un mot près.
func userSetActive(ctx context.Context, subcmd string, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("avanti user "+subcmd, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { usageUser(stderr) }

	email := flags.String("email", "", "adresse email du compte visé")

	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("analyse des arguments : %w", err)
	}
	if *email == "" {
		usageUser(stderr)
		return fmt.Errorf("user %s : --email est obligatoire", subcmd)
	}

	app, cleanup, err := openInstance(ctx, stderr)
	if err != nil {
		return err
	}
	defer cleanup()

	user, err := app.accounts.ByEmail(ctx, *email)
	if err != nil {
		return fmt.Errorf("user %s : %w", subcmd, err)
	}

	disable := subcmd == subcmdUserDisable
	if disable {
		err = app.accounts.Deactivate(ctx, user.ID)
	} else {
		err = app.accounts.Reactivate(ctx, user.ID)
	}
	if err != nil {
		return fmt.Errorf("user %s : %w", subcmd, err)
	}

	out := &sink{w: stdout}
	out.printf("Compte %s : %s.\n", user.Email, accountState(!disable))
	if disable {
		// Le rôle et l'état du compte sont relus à chaque requête web : la
		// désactivation ferme aussi les sessions déjà ouvertes, sans attendre leur
		// expiration. Le dire évite qu'on aille redémarrer le serveur pour rien.
		out.printf("Les sessions web en cours sur ce compte sont closes.\n")
	}

	return out.err
}

// userSetPassword réinitialise le mot de passe d'un compte, sans demander
// l'ancien : c'est l'opération d'administration du mot de passe perdu, et qui
// peut exécuter cette commande sur l'hôte détient déjà la base — voir
// identity.AccountService.ResetPassword pour le raisonnement complet.
func userSetPassword(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("avanti user set-password", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { usageUser(stderr) }

	email := flags.String("email", "", "adresse email du compte visé")
	generate := flags.Bool("generate", false, "engendre le nouveau mot de passe au lieu de le demander")

	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("analyse des arguments : %w", err)
	}
	if *email == "" {
		usageUser(stderr)
		return errors.New("user set-password : --email est obligatoire")
	}

	// Le mot de passe est obtenu avant d'ouvrir la base, comme pour « add » :
	// rien ne sert de joindre PostgreSQL pour découvrir que les saisies diffèrent.
	password, generated, err := getPassword(*generate, stdout)
	if err != nil {
		return err
	}

	app, cleanup, err := openInstance(ctx, stderr)
	if err != nil {
		return err
	}
	defer cleanup()

	user, err := app.accounts.ByEmail(ctx, *email)
	if err != nil {
		return fmt.Errorf("user set-password : %w", err)
	}
	if err := app.accounts.ResetPassword(ctx, user.ID, password); err != nil {
		return fmt.Errorf("user set-password : %w", err)
	}

	out := &sink{w: stdout}
	out.printf("Mot de passe réinitialisé pour %s.\n", user.Email)
	if generated {
		// Même règle que pour « add » : affiché une fois et une seule, sur la
		// sortie standard, redirigeable vers un gestionnaire de mots de passe.
		out.printf("\nMot de passe engendré, à noter maintenant — il ne sera plus affiché :\n\n  %s\n\n", password)
	}
	// L'ancien mot de passe ne fonctionne plus, mais les sessions web déjà
	// ouvertes restent valides : si le compte est peut-être compromis,
	// enchaîner disable puis enable les ferme toutes.
	out.printf("Les sessions web déjà ouvertes restent valides ; « user disable » puis « enable » les ferme au besoin.\n")

	return out.err
}

// userSetRole change le rôle d'un compte.
func userSetRole(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("avanti user set-role", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { usageUser(stderr) }

	email := flags.String("email", "", "adresse email du compte visé")
	role := flags.String("role", "", "nouveau rôle : "+availableRoles())

	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("analyse des arguments : %w", err)
	}
	if *email == "" || *role == "" {
		usageUser(stderr)
		return errors.New("user set-role : --email et --role sont obligatoires")
	}
	if !identity.Role(*role).Known() {
		return fmt.Errorf("user set-role : rôle %q inconnu, attendu %s", *role, availableRoles())
	}

	app, cleanup, err := openInstance(ctx, stderr)
	if err != nil {
		return err
	}
	defer cleanup()

	user, err := app.accounts.ByEmail(ctx, *email)
	if err != nil {
		return fmt.Errorf("user set-role : %w", err)
	}
	if err := app.accounts.ChangeRole(ctx, user.ID, identity.Role(*role)); err != nil {
		return fmt.Errorf("user set-role : %w", err)
	}

	out := &sink{w: stdout}
	out.printf("Compte %s : rôle %s.\n", user.Email, *role)
	// Le compte est relu à chaque requête web et les scopes d'un jeton MCP sont
	// recalculés à chaque vérification : le dire évite d'aller redémarrer le
	// serveur ou d'attendre une expiration pour rien.
	out.printf("Effet immédiat : le rôle est relu à chaque requête, sessions web et jetons d'agent compris.\n")

	return out.err
}

func accountState(active bool) string {
	if active {
		return "actif"
	}
	return "désactivé"
}

func yesNo(yes bool) string {
	if yes {
		return "oui"
	}
	return "non"
}

// availableRoles énumère les rôles pour les messages d'aide, en les demandant
// au domaine plutôt qu'en les recopiant : un rôle ajouté apparaît ici sans qu'on
// y pense.
func availableRoles() string {
	roles := identity.AllRoles()

	names := make([]string, 0, len(roles))
	for _, role := range roles {
		names = append(names, string(role))
	}

	return strings.Join(names, " ou ")
}

// getPassword engendre le mot de passe ou le demande au terminal.
//
// Le second booléen rendu dit s'il a été engendré, et donc s'il faut l'afficher :
// un mot de passe saisi à la main, la personne le connaît déjà, et le réafficher
// n'aurait pour effet que de le laisser à l'écran.
func getPassword(generate bool, stdout io.Writer) (password string, generated bool, err error) {
	if generate {
		password, err = identity.GeneratePassword()
		return password, true, err
	}

	password, err = promptPassword(stdout)

	return password, false, err
}

// promptPassword lit un mot de passe au terminal, sans écho, et le fait
// confirmer.
//
// L'absence de terminal est une erreur nette, et non un repli sur la lecture
// d'une ligne ordinaire : sans terminal, il n'y a pas d'écho à couper, et le mot
// de passe s'afficherait — dans un journal de CI, dans un enregistrement de
// session, dans l'historique du shell.
func promptPassword(stdout io.Writer) (string, error) {
	// Fd() rend un uintptr par contrat de l'API POSIX ; un descripteur de fichier
	// est un petit entier positif, la conversion ne peut pas déborder.
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New("saisie du mot de passe : aucun terminal sur l'entrée standard — utilisez --generate")
	}

	prompt := &sink{w: stdout}

	prompt.printf("Mot de passe (%d caractères au minimum, aucune autre contrainte) : ", identity.MinPasswordLength)
	first, err := term.ReadPassword(fd)
	prompt.printf("\n")
	if err != nil {
		return "", fmt.Errorf("saisie du mot de passe : %w", err)
	}

	// La politique est vérifiée avant la confirmation : faire retaper deux fois un
	// mot de passe pour le refuser ensuite serait inutilement pénible.
	if policyErr := identity.CheckPassword(string(first)); policyErr != nil {
		return "", policyErr
	}

	prompt.printf("Confirmation : ")
	second, err := term.ReadPassword(fd)
	prompt.printf("\n")
	if err != nil {
		return "", fmt.Errorf("confirmation du mot de passe : %w", err)
	}

	// bytes.Equal plutôt qu'une comparaison de chaînes : les deux saisies restent
	// des tranches d'octets, sans copie supplémentaire du secret en mémoire.
	if !bytes.Equal(first, second) {
		return "", errors.New("saisie du mot de passe : les deux saisies diffèrent")
	}
	if prompt.err != nil {
		return "", prompt.err
	}

	return string(first), nil
}

// sortie écrit sur un io.Writer en retenant la première erreur rencontrée.
//
// C'est le même principe que l'erreur collante de bufio.Writer, et il existe pour
// la même raison : une commande qui affiche six lignes n'a pas à tester six fois
// si la sortie standard est toujours là. L'erreur se lit une fois, à la fin.
type sink struct {
	w   io.Writer
	err error
}

// printf écrit une ligne formatée. Le f final est la convention Go des fonctions
// à format, et goprintffuncname l'exige.
func (s *sink) printf(format string, args ...any) {
	if s.err != nil {
		return
	}
	if _, err := fmt.Fprintf(s.w, format, args...); err != nil {
		s.err = fmt.Errorf("écriture de la sortie : %w", err)
	}
}

// Write fait de sortie un io.Writer, pour le passer à un tabwriter.
func (s *sink) Write(p []byte) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	n, err := s.w.Write(p)
	if err != nil {
		s.err = fmt.Errorf("écriture de la sortie : %w", err)
	}
	return n, err
}
