// Commande avanti : point d'entrée unique de l'application.
//
// C'est le seul endroit du dépôt autorisé à connaître à la fois les domaines et
// les adapters (R4 de docs/ARCHITECTURE.md) : il lit la configuration, choisit
// les implémentations concrètes des ports et les injecte. Tout ce qui est en
// dessous ignore ce qui est branché sur lui.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/alexedwards/scs/pgxstore"

	"github.com/Romain-Badino/Avanti/internal/adapters/mcp"
	"github.com/Romain-Badino/Avanti/internal/adapters/web"
	"github.com/Romain-Badino/Avanti/internal/platform"
	"github.com/Romain-Badino/Avanti/internal/platform/db"
	"github.com/Romain-Badino/Avanti/internal/platform/server"
)

// Les sous-commandes reconnues.
const (
	commandServe   = "serve"
	commandVersion = "version"
	commandUser    = "user"
)

func main() {
	// L'annulation du contexte est le seul mécanisme d'arrêt du programme : le
	// socle ne pose pas de gestionnaire de signal, c'est ici qu'on décide de la
	// vie du processus.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)

	// stop() est appelé avant os.Exit, qui ne déroule aucun defer : laisser le
	// gestionnaire de signal en place derrière soi rendrait le prochain signal
	// inopérant sur un processus déjà condamné.
	stop()

	if err != nil {
		fmt.Fprintf(os.Stderr, "avanti : %v\n", err)
		os.Exit(1)
	}
}

// run contient le corps du programme, isolé de os.Exit pour rester testable.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("avanti", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { usage(stderr, flags) }

	showVersion := flags.Bool("version", false, "affiche l'identité du binaire puis quitte")

	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("analyse des arguments : %w", err)
	}

	command := commandServe
	if flags.NArg() > 0 {
		command = flags.Arg(0)
	}
	if *showVersion {
		command = commandVersion
	}

	switch command {
	case commandVersion:
		return printVersion(stdout)
	case commandServe:
		return serve(ctx, stderr)
	case commandUser:
		// flags.Args()[1:] : ce qui suit « user » appartient à la sous-commande, et
		// non au jeu de drapeaux global.
		return runUser(ctx, flags.Args()[1:], stdout, stderr)
	default:
		usage(stderr, flags)
		return fmt.Errorf("commande inconnue : %q", command)
	}
}

func usage(out io.Writer, flags *flag.FlagSet) {
	_, err := fmt.Fprint(out, `Usage : avanti [options] [commande]

Commandes :
  serve     Démarre le serveur HTTP (commande par défaut)
  user      Gère les comptes (« avanti user » pour le détail)
  version   Affiche l'identité du binaire puis quitte

La configuration passe par des variables d'environnement préfixées AVANTI_ ;
.env.example en donne la liste commentée.

Options :
`)
	if err != nil {
		// La sortie d'aide est injoignable : il n'y a rien de plus à en dire,
		// et l'erreur de la commande elle-même reste la seule qui compte.
		return
	}

	flags.PrintDefaults()
}

func printVersion(stdout io.Writer) error {
	if _, err := fmt.Fprintln(stdout, platform.Build()); err != nil {
		return fmt.Errorf("écriture de la version : %w", err)
	}
	return nil
}

// serve assemble l'application et la sert jusqu'à l'annulation de ctx.
//
// L'ordre de montage est celui des dépendances : configuration, journal, base,
// schéma, domaines, sessions, interface, serveur. Chaque étape échoue bruyamment
// plutôt que de laisser démarrer une instance à moitié fonctionnelle.
func serve(ctx context.Context, stderr io.Writer) error {
	app, cleanup, err := openInstance(ctx, stderr)
	if err != nil {
		return err
	}
	defer cleanup()

	app.logger.Info("démarrage d'Avanti",
		slog.String("version", app.build.Version),
		slog.String("commit", app.build.Commit),
		slog.Any("config", app.cfg))

	// Le magasin de sessions est choisi ici, et lui seul connaît pgx : l'adapter
	// web ne reçoit que l'interface scs.Store, ce qui le laisse ignorant du
	// pilote de base (R4).
	sessionStore := pgxstore.NewWithCleanupInterval(app.pool, web.SessionCleanupInterval)
	defer sessionStore.StopCleanup()

	// Le magasin OAuth suit le même principe que celui des sessions : il est
	// choisi ici, et l'adapter web ne reçoit que les interfaces de fosite.
	// C'est ce qui permet aux deux familles d'adapters de s'ignorer (R4).
	//
	// L'URL canonique du serveur MCP est définie par l'adapter mcp et transmise
	// à l'adapter web pour le contrôle de l'indicateur de ressource (RFC 8707) :
	// les deux familles ne s'importent pas, c'est ici que la valeur circule.
	site, err := web.New(web.Options{
		Logger:       app.logger,
		Build:        app.build,
		Accounts:     app.accounts,
		Sessions:     sessionStore,
		BaseURL:      app.cfg.BaseURL,
		OAuthStorage: app.oauthStore,
		Devis:        app.devisService,
		Documents:    app.documentsService,
		Finance:      app.financeService,
		Planning:     app.planningService,
		Exports:      newExports(),
		OAuthSecret:  app.cfg.OAuthSecret,
		MCPResource:  mcp.CanonicalServerURL(app.cfg.BaseURL),
	})
	if err != nil {
		return err
	}

	// L'adapter MCP reçoit le vérificateur de jetons que l'adapter web expose
	// par le port identity.TokenVerifier : c'est la seule jonction entre les
	// deux familles, et elle se fait ici, dans cmd/avanti (R4) — l'adapter MCP
	// ne sait pas que fosite existe.
	agent, err := mcp.New(mcp.Options{
		Logger:    app.logger,
		Build:     app.build,
		BaseURL:   app.cfg.BaseURL,
		Verifier:  site.TokenVerifier(),
		Devis:     app.devisService,
		Documents: app.documentsService,
		Finance:   app.financeService,
		Planning:  app.planningService,
	})
	if err != nil {
		return err
	}

	stopPurge := startOAuthPurge(ctx, app)
	defer stopPurge()

	httpServer, err := server.New(server.Options{
		Config:  app.cfg,
		Logger:  app.logger,
		Handler: composeRoot(site, agent),
		Ready:   func(ctx context.Context) error { return db.Ping(ctx, app.pool) },
	})
	if err != nil {
		return err
	}

	return httpServer.Run(ctx)
}

// composeRoot assemble le routage racine de l'instance : le point de
// terminaison MCP et son document de découverte vers l'adapter mcp, tout le
// reste vers l'adapter web.
//
// La composition vit ici parce que c'est cmd/avanti qui connaît les deux
// familles (R4). Les chemins MCP sont des correspondances EXACTES : l'adapter
// web garde son propre « tout le reste » — en-têtes de sécurité, page 404 — et
// ne peut pas avaler le document RFC 9728, servi sans session ni CSRF comme
// les métadonnées RFC 8414 qu'il jouxte sous /.well-known.
func composeRoot(site *web.Handler, agent *mcp.Handler) http.Handler {
	root := http.NewServeMux()
	root.Handle(mcp.ServerPath, agent)
	root.Handle(mcp.ProtectedResourceMetadataPath, agent)
	root.Handle(mcp.ProtectedResourceMetadataPathMCP, agent)
	root.Handle("/", site)

	return root
}
