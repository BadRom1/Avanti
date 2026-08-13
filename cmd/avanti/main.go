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
	"os"
	"os/signal"
	"syscall"

	"github.com/Romain-Badino/Avanti/internal/adapters/web"
	"github.com/Romain-Badino/Avanti/internal/platform"
	"github.com/Romain-Badino/Avanti/internal/platform/config"
	"github.com/Romain-Badino/Avanti/internal/platform/db"
	"github.com/Romain-Badino/Avanti/internal/platform/logging"
	"github.com/Romain-Badino/Avanti/internal/platform/migrate"
	"github.com/Romain-Badino/Avanti/internal/platform/server"
)

// Les sous-commandes reconnues.
const (
	commandServe   = "serve"
	commandVersion = "version"
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
	default:
		usage(stderr, flags)
		return fmt.Errorf("commande inconnue : %q", command)
	}
}

func usage(out io.Writer, flags *flag.FlagSet) {
	_, err := fmt.Fprint(out, `Usage : avanti [options] [commande]

Commandes :
  serve     Démarre le serveur HTTP (commande par défaut)
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
// schéma, interface, serveur. Chaque étape échoue bruyamment plutôt que de
// laisser démarrer une instance à moitié fonctionnelle.
func serve(ctx context.Context, stderr io.Writer) error {
	cfg, err := config.LoadFromEnv()
	if err != nil {
		return err
	}

	// Les journaux vont sur la sortie d'erreur : la sortie standard reste
	// disponible pour ce qu'une commande produit réellement.
	logger := logging.New(stderr, cfg)
	build := platform.Build()

	logger.Info("démarrage d'Avanti",
		slog.String("version", build.Version),
		slog.String("commit", build.Commit),
		slog.Any("config", cfg))

	pool, err := db.Open(ctx, logger, cfg.DatabaseURL, cfg.DBConnectTimeout)
	if err != nil {
		return err
	}
	defer pool.Close()

	if cfg.MigrateOnStart {
		// goose ne parle que database/sql : on emprunte une vue sur le pool le
		// temps de la migration, puis on la rend — le pool reste ouvert.
		sqlDB := db.StdlibDB(pool)
		migrateErr := migrate.Up(ctx, logger, sqlDB)
		if closeErr := sqlDB.Close(); closeErr != nil {
			logger.Warn("fermeture de la vue database/sql du pool",
				slog.String("error", closeErr.Error()))
		}
		if migrateErr != nil {
			return migrateErr
		}
	} else {
		logger.Warn("migrations désactivées au démarrage, le schéma doit déjà être à jour")
	}

	site, err := web.New(web.Options{Logger: logger, Build: build})
	if err != nil {
		return err
	}

	httpServer, err := server.New(server.Options{
		Config:  cfg,
		Logger:  logger,
		Handler: site,
		Ready:   func(ctx context.Context) error { return db.Ping(ctx, pool) },
	})
	if err != nil {
		return err
	}

	return httpServer.Run(ctx)
}
