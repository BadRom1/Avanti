package main

import (
	"context"
	"io"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Romain-Badino/Avanti/internal/adapters/postgres"
	"github.com/Romain-Badino/Avanti/internal/identity"
	"github.com/Romain-Badino/Avanti/internal/platform"
	"github.com/Romain-Badino/Avanti/internal/platform/config"
	"github.com/Romain-Badino/Avanti/internal/platform/db"
	"github.com/Romain-Badino/Avanti/internal/platform/logging"
	"github.com/Romain-Badino/Avanti/internal/platform/migrate"
)

// instance rassemble ce que toute sous-commande a besoin d'ouvrir : la
// configuration, le journal, la base, et les services de domaine branchés sur
// elle.
//
// C'est ici que se fait le choix des implémentations concrètes des ports — le
// travail propre à cmd/avanti, seul endroit du dépôt autorisé à connaître les
// domaines et les adapters à la fois (R4 de docs/ARCHITECTURE.md).
type instance struct {
	cfg    *config.Config
	logger *slog.Logger
	build  platform.BuildInfo
	pool   *pgxpool.Pool

	// accounts est le service du domaine identity, monté sur le dépôt PostgreSQL et
	// le hacheur argon2id.
	accounts *identity.AccountService
}

// openInstance monte tout ce qui précède, dans l'ordre de ses dépendances.
//
// La fonction de fermeture rendue est à appeler dans tous les cas, y compris sur
// erreur du reste de la commande : c'est elle qui rend le pool de connexions.
func openInstance(ctx context.Context, stderr io.Writer) (*instance, func(), error) {
	cfg, err := config.LoadFromEnv()
	if err != nil {
		return nil, func() {}, err
	}

	// Les journaux vont sur la sortie d'erreur : la sortie standard reste
	// disponible pour ce qu'une commande produit réellement.
	logger := logging.New(stderr, cfg)

	pool, err := db.Open(ctx, logger, cfg.DatabaseURL, cfg.DBConnectTimeout)
	if err != nil {
		return nil, func() {}, err
	}

	if schemaErr := applySchema(ctx, cfg, logger, pool); schemaErr != nil {
		pool.Close()
		return nil, func() {}, schemaErr
	}

	repo, err := postgres.NewUserRepo(pool)
	if err != nil {
		pool.Close()
		return nil, func() {}, err
	}

	accounts, err := identity.NewAccountService(identity.ServiceOptions{
		Repo: repo,
		// Le hacheur de production, celui dont la lenteur est la fonction. Sa
		// construction est le seul endroit du dépôt qui le nomme : partout ailleurs,
		// c'est le port identity.Hasher qui circule.
		Hasher: identity.NewArgon2idHasher(),
	})
	if err != nil {
		pool.Close()
		return nil, func() {}, err
	}

	return &instance{
		cfg:      cfg,
		logger:   logger,
		build:    platform.Build(),
		pool:     pool,
		accounts: accounts,
	}, pool.Close, nil
}

// applySchema rejoue les migrations manquantes, si la configuration le
// permet.
//
// Les sous-commandes de gestion des comptes passent par ici comme `serve` : c'est
// ainsi que `avanti user add` fonctionne sur une base fraîchement créée, avant
// que le serveur n'ait jamais tourné — l'ordre naturel d'une première
// installation.
func applySchema(ctx context.Context, cfg *config.Config, logger *slog.Logger, pool *pgxpool.Pool) error {
	if !cfg.MigrateOnStart {
		logger.Warn("migrations désactivées, le schéma doit déjà être à jour")
		return nil
	}

	// goose ne parle que database/sql : on emprunte une vue sur le pool le temps
	// de la migration, puis on la rend — le pool reste ouvert.
	sqlDB := db.StdlibDB(pool)
	migrateErr := migrate.Up(ctx, logger, sqlDB)
	if closeErr := sqlDB.Close(); closeErr != nil {
		logger.Warn("fermeture de la vue database/sql du pool",
			slog.String("error", closeErr.Error()))
	}

	return migrateErr
}
